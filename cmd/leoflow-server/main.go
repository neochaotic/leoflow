// Command leoflow-server runs the Leoflow control plane: the HTTP API, auth,
// metrics, and (when enabled) the scheduler.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	leoflow "github.com/neochaotic/leoflow"
	"github.com/neochaotic/leoflow/internal/agentrpc"
	"github.com/neochaotic/leoflow/internal/alerts"
	"github.com/neochaotic/leoflow/internal/api"
	"github.com/neochaotic/leoflow/internal/auth"
	"github.com/neochaotic/leoflow/internal/config"
	"github.com/neochaotic/leoflow/internal/dispatch"
	"github.com/neochaotic/leoflow/internal/domain"
	"github.com/neochaotic/leoflow/internal/executor"
	"github.com/neochaotic/leoflow/internal/failurealert"
	"github.com/neochaotic/leoflow/internal/logs"
	"github.com/neochaotic/leoflow/internal/observability"
	"github.com/neochaotic/leoflow/internal/scheduler"
	"github.com/neochaotic/leoflow/internal/secrets"
	"github.com/neochaotic/leoflow/internal/storage"
	"github.com/neochaotic/leoflow/internal/ui"
	"github.com/neochaotic/leoflow/internal/version"
	"github.com/neochaotic/leoflow/internal/workspace"
	"github.com/neochaotic/leoflow/internal/xcom"
	agentv1 "github.com/neochaotic/leoflow/proto/agent/v1"
)

// usage is printed for `--help`. leoflow-server takes no positional args; it is
// configured entirely via environment and an optional LEOFLOW_CONFIG file.
const usage = `leoflow-server — the Leoflow control plane (HTTP API, auth, metrics, scheduler).

Configured via environment variables and an optional config file (LEOFLOW_CONFIG);
there are no positional arguments. See docs/configuration.md.

Flags:
  --version   print version and exit
  --help, -h  print this help and exit
`

func main() {
	// Answer `--version`/`--help` before loading any config, so an operator can
	// query a deployed binary without a runnable environment (#593). Without
	// this, `--help` falls through to a boot attempt that errors on missing
	// config.
	args := os.Args[1:]
	switch {
	case version.WantsVersion(args):
		fmt.Println(version.Get().String())
		return
	case version.WantsHelp(args):
		fmt.Print(usage)
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "leoflow-server:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.LoadServer(os.Getenv("LEOFLOW_CONFIG"), nil)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	if verr := cfg.Validate(); verr != nil {
		return verr
	}

	tel, shutdownTel, err := observability.Setup(ctx, observability.Config{
		ServiceName:  "leoflow-server",
		LogLevel:     cfg.Observability.LogLevel,
		LogFormat:    cfg.Observability.LogFormat,
		OTelEnabled:  cfg.Observability.OTel.Enabled,
		OTelEndpoint: cfg.Observability.OTel.Endpoint,
	})
	if err != nil {
		return fmt.Errorf("observability setup: %w", err)
	}
	defer shutdownTel()

	pg, err := storage.NewPostgres(ctx, cfg.Database)
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	defer pg.Close()

	// Datastore for XCom + live-log tailing: Redis when configured (production,
	// ADR 0006), or the embedded Postgres/in-process backends when no Redis is
	// set (Lite — ADR 0026). The signal is the presence of a Redis URL.
	xcomBackend, logTailer, redisHealth, dsCleanup, err := selectDatastore(ctx, cfg, pg, tel.Logger, tel.Metrics)
	if err != nil {
		return err
	}
	defer dsCleanup()

	repo := storage.NewRepository(pg)
	if cerr := configureSecretCipher(repo, cfg.SecretKey, tel.Logger); cerr != nil {
		return cerr
	}
	authn := auth.NewJWTAuthenticator(repo, cfg.Auth.JWT.Secret, time.Duration(cfg.Auth.JWT.TokenTTLSeconds)*time.Second)
	execStore := storage.NewExecutionStore(pg)
	xcomSvc := xcom.NewService(xcomBackend, storage.NewXComIndex(pg), xcom.DefaultTTL)
	xcomReader := storage.NewXComReader(pg, xcomBackend)

	if err := bootstrapAdmin(ctx, repo, tel.Logger); err != nil {
		return err
	}

	logSink := logs.NewDiskSink(cfg.Logs.Dir)
	// Secrets are served over the agent channel only when explicitly allowed
	// insecure (dev) until gRPC TLS lands (issue #58); otherwise the handlers
	// fail closed on a plaintext channel.
	allowInsecureSecrets := os.Getenv("LEOFLOW_AGENT_ALLOW_INSECURE_SECRETS") == "true"
	// Pro alpha blocker (#58): a production-edition deployment MUST NOT ship
	// secrets over a plaintext channel. The chart's deployment template stamps
	// LEOFLOW_UI_EDITION=production; refuse to boot if the insecure escape
	// hatch is set alongside it. Lite (edition=lite) and the unmarked default
	// (edition="") still tolerate the flag for the dev inner loop.
	if err := guardInsecureSecretsForEdition(cfg.UI.Edition, allowInsecureSecrets); err != nil {
		return err
	}
	// A Pro deployment without agent-gRPC TLS boots but can't deliver secrets —
	// refuse it loudly rather than fail every secrets RPC cryptically (#281).
	if err := guardTLSForEdition(cfg.UI.Edition, cfg.Server.GRPCTLSCert, cfg.Server.GRPCTLSKey); err != nil {
		return err
	}
	// Role gating (ADR 0049). "all" (the default, and Lite's only mode) serves both
	// sides, so both predicates are true and the wiring below is identical to the
	// pre-0049 monolith. The "scheduler" role runs the agent gRPC, the scheduler
	// loop, and the janitors; the "api" role runs the HTTP API + UI. Metrics run in
	// every role. The Helm two-Deployment rendering that lets an operator select a
	// role is a later phase; today the flag is process-level and covered by tests.
	servesAPI := cfg.Server.ServesAPI()
	servesScheduler := cfg.Server.ServesScheduler()

	// Start the scheduler-role components (agent gRPC, janitors, scheduler loop +
	// dispatch). In the api role this starts nothing and returns a nil health handle
	// + no-op stop; in "all"/"scheduler" it is the pre-0049 wiring unchanged.
	schedulerHealth, podDispatch, stopScheduler, serr := startSchedulerSide(ctx, cfg, pg, repo, execStore, authn, xcomSvc, logSink, logTailer, allowInsecureSecrets, servesScheduler, tel.Logger, tel.Metrics)
	if serr != nil {
		return serr
	}
	defer stopScheduler()

	agentAddr := cfg.Executor.AgentControlPlaneAddr
	if agentAddr == "" {
		agentAddr = cfg.Server.GRPCAddr
	}
	executorInfo := api.ExecutorInfo{
		PodDispatchEnabled:    executorDispatchEnabled(cfg, servesScheduler, podDispatch),
		TaskNamespace:         cfg.Executor.TaskNamespace,
		AgentControlPlaneAddr: agentAddr,
	}

	// Dependency health probes (Postgres, and Redis when configured). Built once
	// and shared read-only by the API's /readyz and the metrics-port /readyz.
	checks := healthChecks(pg, redisHealth)

	// In the split api role the scheduler runs in another process, so
	// startSchedulerSide returned a nil health handle. Reporting the scheduler as
	// healthy from nil would be a lie (finding F1) — instead read its liveness
	// from shared DB state: a live scheduler leader holds the leadership advisory
	// lock (ADR 0009). The "all" role keeps its in-process handle (real tick
	// health), unchanged.
	if servesAPI && !servesScheduler {
		schedulerHealth = scheduler.NewLeaderHealthReader(pg.Pool)
	}

	// The API side (HTTP + embedded UI) is built only in the api/"all" role. The
	// scheduler role serves no API, so none of the UI/handler machinery is even
	// constructed — apiSrv stays nil and serveHTTP omits it.
	var apiSrv *http.Server
	if servesAPI {
		apiSrv = buildAPIServer(cfg, tel, authn, pg, repo, xcomReader, logSink, logTailer, checks, executorInfo, schedulerHealth)
	}

	// The metrics listener also serves /healthz + /readyz in every role so a
	// scheduler-only pod (ADR 0049), which serves no API, still has a probe target
	// for the kubelet. Additive on the api/"all" role, whose probes still hit the
	// HTTP port.
	metricsSrv := &http.Server{Addr: cfg.Server.MetricsAddr, Handler: api.ObservabilityHandler(tel.Registry, checks), ReadHeaderTimeout: 10 * time.Second}

	tel.Logger.Info("leoflow-server started", "role", cfg.Server.EffectiveRole(), "http_addr", cfg.Server.HTTPAddr, "metrics_addr", cfg.Server.MetricsAddr, "serves_api", servesAPI, "serves_scheduler", servesScheduler)
	return serveHTTP(ctx, tel.Logger, servesAPI, apiSrv, metricsSrv)
}

// awaitShutdown blocks until a server errors or the context is canceled, then
// gracefully shuts the HTTP servers down.
func awaitShutdown(ctx context.Context, errCh <-chan error, logger *slog.Logger, servers ...*http.Server) error {
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
		// Shutdown deliberately uses a fresh context; the inherited ctx is already canceled.
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, srv := range servers {
			if serr := srv.Shutdown(shutCtx); serr != nil { //nolint:contextcheck // fresh shutdown context by design
				logger.Error("server shutdown", "addr", srv.Addr, "error", serr)
			}
		}
		return nil
	}
}

// showLiteBadge reports whether the served UI shows the silver LITE badge: the
// Lite edition, or the legacy dev auth bypass.
func showLiteBadge(cfg *config.ServerConfig) bool {
	return cfg.UI.Edition == "lite" || cfg.Auth.DevNoAuth
}

// showProBadge reports whether the served UI shows the gold PRO badge — the
// Pro edition, set explicitly by the Helm chart (values.yaml: ui.edition=pro).
// Demo and empty editions show no badge.
func showProBadge(cfg *config.ServerConfig) bool {
	return cfg.UI.Edition == "pro"
}

// liteEditorFS builds the workspace filesystem backing the Lite web editor when
// a workspace is configured (Lite only). A misconfigured workspace logs a
// warning and disables the editor rather than failing server boot.
func liteEditorFS(cfg *config.ServerConfig, logger *slog.Logger) api.WorkspaceFS {
	if cfg.UI.Workspace == "" {
		return nil
	}
	fs, err := workspace.New(cfg.UI.Workspace)
	if err != nil {
		logger.Warn("Lite editor disabled: invalid workspace", "workspace", cfg.UI.Workspace, "error", err)
		return nil
	}
	return fs
}

// configureSecretCipher wires the AES-256-GCM cipher for connection secrets
// (ADR 0019). Without a key the connection store stays plaintext-incapable:
// writes are refused, never silently stored in the clear.
func configureSecretCipher(repo *storage.Repository, secretKey string, logger *slog.Logger) error {
	key, kerr := secrets.ParseKey(secretKey)
	if kerr != nil {
		logger.Warn("no LEOFLOW_SECRET_KEY set; connection management disabled (Variables still work)")
		return nil //nolint:nilerr // a missing/unusable key is non-fatal: run without connection encryption
	}
	cipher, cerr := secrets.NewAESGCM(key)
	if cerr != nil {
		return fmt.Errorf("building secret cipher: %w", cerr)
	}
	repo.SetCipher(cipher)
	// A 32-character all-hex key is what `openssl rand -hex 16` produces, which
	// this project's own docs recommended until they were corrected. ParseKey
	// takes those 32 characters as 32 raw bytes, so the cipher is AES-256 over
	// 128 bits of entropy and nothing else would ever mention it. Warn rather
	// than refuse: the shape is indistinguishable from a legitimate 32-character
	// passphrase, and breaking an operator who did nothing wrong is worse.
	if secrets.LooksLikeHalfEntropyHexKey(secretKey) {
		logger.Warn("LEOFLOW_SECRET_KEY looks like `openssl rand -hex 16` output: "+
			"32 hex characters are consumed as 32 raw bytes, giving 128 bits of entropy where AES-256 expects 256. "+
			"Generate a replacement with `openssl rand -hex 32` (64 characters) and re-encrypt existing connections",
			"key_length", len(secretKey))
	}
	logger.Info("connection secret encryption enabled (AES-256-GCM)")
	return nil
}

func bootstrapAdmin(ctx context.Context, repo *storage.Repository, logger *slog.Logger) error {
	email := os.Getenv("LEOFLOW_BOOTSTRAP_EMAIL")
	if email == "" {
		email = "admin@leoflow.local"
	}
	// Prefer a precomputed bcrypt hash (Leoflow Lite never sends the plaintext to
	// the control plane); fall back to a plaintext bootstrap password.
	if hash := os.Getenv("LEOFLOW_BOOTSTRAP_PASSWORD_HASH"); hash != "" {
		created, err := repo.BootstrapAdminHash(ctx, "default", email, hash)
		if err != nil {
			return fmt.Errorf("bootstrap admin (hash): %w", err)
		}
		if created {
			logger.Info("bootstrapped admin user", "email", email)
		}
		return nil
	}
	pw := os.Getenv("LEOFLOW_BOOTSTRAP_PASSWORD")
	if pw == "" {
		return nil
	}
	created, err := repo.BootstrapAdmin(ctx, "default", email, pw)
	if err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	}
	if created {
		logger.Info("bootstrapped admin user", "email", email)
	}
	return nil
}

// agentTokenTTL is how long a dispatched task's agent identity token stays valid.
const agentTokenTTL = 24 * time.Hour

// loginRateLimit returns the per-minute failed-login cap with a safe floor: a
// missing or nonsensical (<=0) config value falls back to the conservative
// default rather than 0, which would lock every user out.
func loginRateLimit(cfg *config.ServerConfig) int {
	if cfg.Auth.LoginRateLimitPerMinute > 0 {
		return cfg.Auth.LoginRateLimitPerMinute
	}
	return 5
}

// startSchedulerSide starts the scheduler-role components (ADR 0049): the agent
// gRPC endpoint, the XCom/log janitors, and — when cfg.Scheduler.Enabled — the
// scheduler loop + dispatch pool. It returns the scheduler's health handle (nil
// when the loop is off; the API reports scheduler health as healthy on nil),
// whether pod dispatch is on, and a stop func to defer. When servesScheduler is
// false (the api role) it starts nothing and returns a nil handle + no-op stop, so
// the caller defers unconditionally. The stop func fires drain-then-gRPC-stop,
// matching the pre-0049 defer order (LIFO: drainDispatch before GracefulStop).
func startSchedulerSide(ctx context.Context, cfg *config.ServerConfig, pg *storage.Postgres, repo *storage.Repository, execStore *storage.ExecutionStore, authn *auth.JWTAuthenticator, xcomSvc *xcom.Service, logSink *logs.DiskSink, logTailer logs.Tailer, allowInsecureSecrets bool, servesScheduler bool, logger *slog.Logger, metrics *observability.Metrics) (health api.Heartbeater, podDispatch bool, stop func(), err error) {
	if !servesScheduler {
		return nil, false, func() {}, nil
	}
	grpcSrv, gerr := startAgentGRPC(ctx, cfg.Server.GRPCAddr, authn, execStore, repo, xcomSvc, logSink, logTailer, allowInsecureSecrets, cfg.Server.GRPCTLSCert, cfg.Server.GRPCTLSKey, logger)
	if gerr != nil {
		return nil, false, nil, gerr
	}
	// XCom-TTL and log-retention janitors are maintenance the scheduler owns; the
	// api role runs no background writers.
	startCleanup(ctx, storage.NewXComIndex(pg), logSink, cfg.Logs.Dir, logger)

	drain := func() {}
	if cfg.Scheduler.Enabled {
		sched, dispatchOn, dispatchCloser, serr := startScheduler(ctx, cfg, pg, repo, execStore, authn, logger, metrics)
		if serr != nil {
			grpcSrv.GracefulStop()
			return nil, false, nil, serr
		}
		// On shutdown, drain the buffered dispatch pool (if any) so in-flight
		// dispatches settle (success or failed via the sink) instead of leaking
		// workers and leaving TIs stuck `queued` (#133). nil in Lite/passthrough.
		drain = func() { drainDispatch(dispatchCloser, logger) }
		health = sched
		podDispatch = dispatchOn
	}
	stop = func() {
		drain()
		grpcSrv.GracefulStop()
	}
	return health, podDispatch, stop, nil
}

// buildAPIServer assembles the HTTP API + embedded UI server for the api/"all"
// role (ADR 0049). It is called only when the role serves the API, so the UI
// shell, editor FS, rate limiter, and full route set are never constructed in a
// scheduler-only process.
func buildAPIServer(cfg *config.ServerConfig, tel *observability.Telemetry, authn *auth.JWTAuthenticator, pg *storage.Postgres, repo *storage.Repository, xcomReader *storage.XComReader, logSink *logs.DiskSink, logTailer logs.Tailer, checks map[string]api.HealthChecker, executorInfo api.ExecutorInfo, schedulerHealth api.Heartbeater) *http.Server {
	if cfg.Auth.DevNoAuth {
		tel.Logger.Warn("AUTHENTICATION DISABLED (auth.dev_no_auth): every request is treated as admin. Dev only — NEVER use in production")
	}
	// Show the LITE badge for the Lite edition (independent of the auth mode), and
	// also when the legacy dev auth bypass is on. The demo/production show neither.
	uiSrv := ui.New()
	uiSrv.SetLiteBanner(showLiteBadge(cfg))
	uiSrv.SetProBanner(showProBadge(cfg))
	uiSrv.SetInstanceName(cfg.UI.InstanceName)

	editorFS := liteEditorFS(cfg, tel.Logger)
	uiSrv.SetEditorButton(editorFS != nil)

	handler := api.NewServer(api.Dependencies{
		Logger:                       tel.Logger,
		Authenticator:                authn,
		RateLimiter:                  auth.NewRateLimiter(loginRateLimit(cfg), time.Minute),
		Registry:                     tel.Registry,
		Metrics:                      tel.Metrics,
		Tracer:                       tel.Tracer,
		HealthChecks:                 checks,
		CORSOrigins:                  cfg.Server.CORS.AllowedOrigins,
		TrustedProxies:               cfg.Server.TrustedProxies,
		TokenTTLSecs:                 cfg.Auth.JWT.TokenTTLSeconds,
		InstanceName:                 cfg.UI.InstanceName,
		UIAutoRefreshIntervalSeconds: cfg.UI.AutoRefreshIntervalSeconds,
		DevNoAuth:                    cfg.Auth.DevNoAuth,

		Dags:            repo,
		DagRuns:         repo,
		Tasks:           repo,
		Versions:        repo,
		Xcoms:           xcomReader,
		Logs:            storage.NewLogReader(pg, logSink, logTailer),
		Specs:           repo,
		LatestRuns:      repo,
		TaskSummary:     repo,
		DagVersions:     repo,
		DashboardStats:  repo,
		AuditLog:        repo,
		Variables:       repo,
		Connections:     repo,
		Favorites:       repo,
		ImportErrors:    repo,
		Audit:           repo,
		ExecutorInfo:    executorInfo,
		SchedulerHealth: schedulerHealth,
		UI:              uiSrv,
		Workspace:       editorFS,
		MonacoDir:       cfg.UI.MonacoDir,
		ExamplesFS:      leoflow.ExampleDAGs(),
	})
	return &http.Server{Addr: cfg.Server.HTTPAddr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
}

// executorDispatchEnabled decides what /api/v2/monitor/executor reports for
// pod_dispatch_enabled. The scheduler owns the executor, so on the scheduler/all
// role the accurate runtime signal (whether a K8s client actually wired up) is
// right. In the split api role (ADR 0049) the scheduler runs in another process,
// so the runtime bool is always false here — reporting that would tell an
// operator dispatch is off when it isn't (F1). Fall back to the configured
// capability (executor.type), the config-derived ExecutorInfo the ADR calls for.
func executorDispatchEnabled(cfg *config.ServerConfig, servesScheduler, runtimePodDispatch bool) bool {
	if servesScheduler {
		return runtimePodDispatch
	}
	return cfg.Executor.Type == "kubernetes"
}

// serveHTTP starts the process's HTTP listeners — the api role's API+UI (when
// servesAPI) plus /metrics in every role, so a scheduler-only pod stays scrapable
// — and blocks until one errors or ctx is canceled, then shuts them down.
func serveHTTP(ctx context.Context, logger *slog.Logger, servesAPI bool, apiSrv, metricsSrv *http.Server) error {
	servers := []*http.Server{metricsSrv}
	if servesAPI {
		servers = append([]*http.Server{apiSrv}, servers...)
	}
	errCh := make(chan error, len(servers))
	for _, srv := range servers {
		go serve(srv, errCh)
	}
	return awaitShutdown(ctx, errCh, logger, servers...)
}

// startAgentGRPC starts the AgentService gRPC server and returns it for graceful
// shutdown. TLS is enabled when tlsCert/tlsKey are set (issue #58); otherwise the
// channel is plaintext (dev). The per-task bearer token in metadata authenticates
// each call regardless.
func startAgentGRPC(ctx context.Context, addr string, authn *auth.JWTAuthenticator, store *storage.ExecutionStore, secretsStore agentrpc.SecretsStore, xcomSvc agentrpc.XComService, logSink agentrpc.LogSink, logTailer agentrpc.LogPublisher, allowInsecureSecrets bool, tlsCert, tlsKey string, logger *slog.Logger) (*grpc.Server, error) {
	var lc net.ListenConfig
	lis, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listening for agent grpc on %s: %w", addr, err)
	}
	agentSrv := agentrpc.NewServer(authn, store, xcomSvc)
	agentSrv.SetLogSink(logSink)
	agentSrv.SetLogPublisher(logTailer)
	agentSrv.SetSecrets(secretsStore, allowInsecureSecrets)

	// Recover panics in any agent RPC handler so a single malformed request from a
	// worker pod cannot crash the control plane (it returns Internal instead).
	opts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(agentrpc.RecoveryUnaryInterceptor(logger)),
		grpc.ChainStreamInterceptor(agentrpc.RecoveryStreamInterceptor(logger)),
	}
	secure := tlsCert != "" && tlsKey != ""
	if secure {
		creds, cerr := credentials.NewServerTLSFromFile(tlsCert, tlsKey)
		if cerr != nil {
			return nil, fmt.Errorf("loading agent grpc TLS cert: %w", cerr)
		}
		opts = append(opts, grpc.Creds(creds))
	}
	srv := grpc.NewServer(opts...)
	agentv1.RegisterAgentServiceServer(srv, agentSrv)
	go func() {
		if serr := srv.Serve(lis); serr != nil && !errors.Is(serr, grpc.ErrServerStopped) {
			logger.Error("agent grpc server", "error", serr)
		}
	}()
	logger.Info("agent grpc server started", "grpc_addr", addr, "tls", secure)
	return srv, nil
}

// buildPodExecutor constructs a Kubernetes executor from the in-cluster config
// or the local kubeconfig. It returns an error when neither is available, in
// which case pod dispatch is disabled and tasks have no executor to run them.
func buildK8sClient() (kubernetes.Interface, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		cfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{}).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("no in-cluster config or kubeconfig: %w", err)
		}
	}
	// Bound every API call. Workers dispatch with a detached context (they
	// already accepted responsibility for the task), so without a client-side
	// deadline an apiserver that accepts the connection and never answers hangs
	// a worker for good — and with it the shutdown drain (#463).
	cfg.Timeout = k8sClientTimeout
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("building kubernetes client: %w", err)
	}
	return cs, nil
}

// k8sClientTimeout caps a single Kubernetes API call. Generous enough for a pod
// create against a loaded apiserver, short enough that a hung call is cut well
// inside the dispatch drain timeout.
const k8sClientTimeout = 10 * time.Second

// reconcileInterval is how often the pod reconciler sweeps for failed pods.
const reconcileInterval = 30 * time.Second

// cleanupInterval is how often expired XCom index rows and old logs are purged.
const cleanupInterval = time.Hour

// logRetention is how long task logs are kept before garbage collection.
const logRetention = 30 * 24 * time.Hour

// lowDiskWarnBytes is the free-space threshold below which the janitor warns: the
// embedded datastore (managed Postgres + logs) lives on this filesystem, and a
// full disk makes Postgres fail writes with a cryptic error.
const lowDiskWarnBytes = 1 << 30 // 1 GiB

// startCleanup runs a periodic janitor that purges expired XCom index rows,
// prunes old log files, and warns on low disk for the datastore dir. The
// operations are idempotent, so it is safe on every replica.
func startCleanup(ctx context.Context, idx *storage.XComIndex, sink *logs.DiskSink, dataDir string, logger *slog.Logger) {
	go func() {
		t := time.NewTicker(cleanupInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				safeCycle("cleanup", logger, func() {
					if err := idx.PurgeExpired(ctx); err != nil {
						logger.Error("purging expired xcom index", "error", err)
					}
					if err := sink.Prune(time.Now(), logRetention); err != nil {
						logger.Error("pruning old logs", "error", err)
					}
					if free, derr := dirFreeBytes(dataDir); derr == nil && lowDisk(free, lowDiskWarnBytes) {
						logger.Warn("low disk space for the datastore", "dir", dataDir,
							"free_mb", free/(1<<20), "threshold_mb", uint64(lowDiskWarnBytes)/(1<<20))
					}
				})
			}
		}
	}()
}

// lowDisk reports whether free is below the threshold (both in bytes).
func lowDisk(free, threshold uint64) bool { return free < threshold }

// dirFreeBytes returns the bytes available on the filesystem holding dir.
func dirFreeBytes(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", dir, err)
	}
	return st.Bavail * uint64(st.Bsize), nil //nolint:gosec // G115: a filesystem block size is never negative
}

// safeCycle runs one iteration of a periodic background loop, recovering any
// panic so a single bad cycle logs and is retried next tick instead of crashing
// the goroutine (and, since an unrecovered panic in any goroutine aborts the
// program, the whole control plane). It changes no behavior on the happy path.
func safeCycle(name string, logger *slog.Logger, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("recovered panic in background cycle", "cycle", name, "panic", r, "stack", string(debug.Stack()))
		}
	}()
	fn()
}

// xcomSweepInterval is how often the embedded Postgres XCom store is swept of
// expired rows. Redis expires keys natively; Postgres needs a sweep (ADR 0026).
const xcomSweepInterval = 10 * time.Minute

// selectDatastore chooses the XCom backend and live-log tailer for the run. With
// a configured Redis URL it uses the production Redis backends (ADR 0006); with
// no Redis configured it uses the embedded backends — XCom on Postgres and an
// in-process tailer — so Lite needs no Redis (ADR 0026). It returns a health
// checker for Redis (nil when embedded) and a cleanup to defer.
func selectDatastore(ctx context.Context, cfg *config.ServerConfig, pg *storage.Postgres, logger *slog.Logger, metrics *observability.Metrics) (xcom.Backend, logs.Tailer, api.HealthChecker, func(), error) {
	if cfg.Redis.URL == "" {
		logger.Info("embedded datastore: XCom on Postgres, in-process log tailer (no Redis)")
		backend := xcom.NewPostgresBackend(pg.Pool)
		startXComSweep(ctx, backend, logger)
		return backend, logs.NewMemoryTailer(), nil, func() {}, nil
	}
	rd, err := storage.NewRedis(ctx, cfg.Redis)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("redis: %w", err)
	}
	// Attach the per-reason error counter, dial-latency histogram, and pool
	// gauges (#312 sibling — same shape as the #311 step-down observability).
	// 5s scrape interval is comfortably under the default 15s Prometheus
	// scrape, so a P99 spike never reaches the dashboard from missing data.
	// Lite already returned above (no Redis), so metrics is always non-nil
	// here on the Pro path.
	stopObs := storage.AttachRedisObservability(ctx, rd, metrics, 5*time.Second)
	cleanup := func() {
		stopObs()
		if cerr := rd.Close(); cerr != nil {
			logger.Error("closing redis", "error", cerr)
		}
	}
	return xcom.NewRedisBackend(rd.Client), logs.NewRedisTailer(rd.Client), rd, cleanup, nil
}

// healthChecks builds the readiness checks, including Redis only when it is the
// active datastore (redisHealth is nil in the embedded edition).
func healthChecks(pg *storage.Postgres, redisHealth api.HealthChecker) map[string]api.HealthChecker {
	checks := map[string]api.HealthChecker{"postgres": pg}
	if redisHealth != nil {
		checks["redis"] = redisHealth
	}
	return checks
}

// startXComSweep periodically deletes expired rows from the embedded Postgres
// XCom store (the durable equivalent of Redis's native key expiry, ADR 0026).
func startXComSweep(ctx context.Context, backend *xcom.PostgresBackend, logger *slog.Logger) {
	go func() {
		t := time.NewTicker(xcomSweepInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				safeCycle("xcom-sweep", logger, func() {
					if n, err := backend.DeleteExpired(ctx); err != nil {
						logger.Error("sweeping expired xcom", "error", err)
					} else if n > 0 {
						logger.Debug("swept expired xcom", "rows", n)
					}
				})
			}
		}
	}()
}

// startReconciler runs a periodic pod reconciler that marks task instances
// failed when their pod failed without the agent reporting (feeding retries).
// startGatedTicker runs fn every interval on a background goroutine, but only
// while leading() reports this instance holds leadership. It stops when ctx is
// done. The pod reconciler and staging GC mutate cluster state, so at
// replicaCount>1 they must run on the leader alone (ADR 0009/0031); a nil gate
// means "always run" (single-replica / no election).
func startGatedTicker(ctx context.Context, name string, interval time.Duration, leading func() bool, logger *slog.Logger, fn func()) {
	t := time.NewTicker(interval)
	go func() {
		defer t.Stop()
		runGatedTicker(ctx, name, t.C, leading, logger, fn)
	}()
}

// runGatedTicker is the loop of startGatedTicker with the tick source injected,
// so the leadership gate is testable without a real timer. Each cycle is
// panic-isolated by safeCycle. A tick that arrives while not leading is dropped,
// not queued.
func runGatedTicker(ctx context.Context, name string, ticks <-chan time.Time, leading func() bool, logger *slog.Logger, fn func()) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			if leading != nil && !leading() {
				continue
			}
			safeCycle(name, logger, fn)
		}
	}
}

func startReconciler(ctx context.Context, cs kubernetes.Interface, namespace string, reporter executor.FailureReporter, leading func() bool, logger *slog.Logger) {
	rec := executor.NewReconciler(cs, namespace, reporter)
	startGatedTicker(ctx, "pod-reconcile", reconcileInterval, leading, logger, func() {
		if err := rec.Reconcile(ctx); err != nil {
			logger.Error("pod reconcile", "error", err)
		}
	})
}

// stagingGCInterval is how often the per-run staging-volume GC sweeps; stagingTTL
// is how long a FAILED run's volume is kept after its terminal time before the
// PVC is deleted (ADR 0022 — long enough for a clear+re-run to re-attach the
// data). A SUCCEEDED run's volume is freed immediately, regardless of the TTL.
const (
	stagingGCInterval = time.Minute // frequent so a succeeded run's volume is freed ~at DAG end
	stagingTTL        = 24 * time.Hour
)

// startStagingGC periodically reclaims per-run staging PVCs from the
// metadatabase-tracked lifecycle: succeeded runs immediately, failed runs after
// the TTL, orphaned volumes (run gone) on sight (ADR 0022).
func startStagingGC(ctx context.Context, cs kubernetes.Interface, namespace string, store executor.StagingStore, leading func() bool, logger *slog.Logger) {
	exec := executor.NewKubernetesExecutor(cs, namespace)
	exec.SetStagingStore(store)
	startGatedTicker(ctx, "staging-gc", stagingGCInterval, leading, logger, func() {
		if err := exec.GCStagingClaims(ctx, stagingTTL); err != nil {
			logger.Error("staging gc", "error", err)
		}
	})
}

func startScheduler(ctx context.Context, cfg *config.ServerConfig, pg *storage.Postgres, repo *storage.Repository, execStore *storage.ExecutionStore, authn *auth.JWTAuthenticator, logger *slog.Logger, metrics *observability.Metrics) (*scheduler.Scheduler, bool, io.Closer, error) {
	leaderPool, err := storage.NewLeaderPool(ctx, cfg.Database)
	if err != nil {
		return nil, false, nil, fmt.Errorf("leader pool: %w", err)
	}
	store := storage.NewSchedulerStore(pg)
	sched := scheduler.NewScheduler(store, logger,
		time.Duration(cfg.Scheduler.LoopIntervalMS)*time.Millisecond)
	sched.SetRecorder(metrics)
	// Native on-failure alerting (#424): the scheduler fires Slack/webhook rules
	// declared in leoflow.yaml when a run finalizes failed, resolving each rule's
	// managed connection to its endpoint URL. Best-effort, off the tick path.
	sched.SetAlerter(failurealert.New(
		alerts.NewNotifier(&http.Client{Timeout: alertHTTPTimeout}),
		connEndpointResolver{repo},
		metrics,
		logger,
	))
	podDispatch, dispatchCloser := setupDispatch(ctx, cfg, sched, execStore, authn, store, logger, metrics)
	leader := scheduler.NewLeader(leaderPool)
	go func() {
		defer leaderPool.Close()
		campaignAndRun(ctx, leader, sched, logger)
	}()
	return sched, podDispatch, dispatchCloser, nil
}

// drainDispatch closes the buffered dispatch pool on shutdown so in-flight
// dispatches drain instead of leaking workers / leaving TIs stuck `queued`
// (#133). Nil closer (Lite/passthrough) is a no-op; a close error is logged.
func drainDispatch(closer io.Closer, logger *slog.Logger) {
	if closer == nil {
		return
	}
	if err := closer.Close(); err != nil {
		logger.Error("draining dispatch pool on shutdown", "error", err)
	}
}

// alertHTTPTimeout bounds each on-failure alert POST so a slow or hung channel
// endpoint cannot pile up detached alert goroutines (#424).
const alertHTTPTimeout = 10 * time.Second

// connEndpointResolver adapts the connection store to failurealert.EndpointResolver:
// an alert channel's endpoint URL is the connection's decrypted secret (#424).
type connEndpointResolver struct{ repo *storage.Repository }

// ResolveAlertEndpoint returns the connection's endpoint — its secret URL and any
// headers from the connection extra — for the tenant, or an error when it is
// missing/undecryptable.
func (c connEndpointResolver) ResolveAlertEndpoint(ctx context.Context, tenantID, connID string) (failurealert.Endpoint, error) {
	url, headers, err := c.repo.AlertEndpoint(ctx, tenantID, connID)
	if err != nil {
		return failurealert.Endpoint{}, err
	}
	return failurealert.Endpoint{URL: url, Headers: headers}, nil
}

// leaderCheckInterval is how often we both poll for leadership (as a follower)
// and revalidate it (as a leader).
const leaderCheckInterval = 5 * time.Second

// maxLeaderCheckFailures is how many consecutive leadership-check errors are
// tolerated before stepping down, so a single transient error (e.g. the leader
// connection being recycled) does not cause needless leadership churn.
const maxLeaderCheckFailures = 3

// leadershipChecker reports whether this instance still holds leadership.
type leadershipChecker interface {
	HoldsLock(ctx context.Context) (bool, error)
}

// campaignAndRun acquires scheduler leadership (polling every leaderCheckInterval)
// and runs the loop only while leader, so a single replica schedules at a time
// (ADR 0009). If leadership is lost mid-run (the advisory lock dropped because the
// connection blipped, was idle-reaped, or recycled), it steps down and
// re-campaigns rather than scheduling alongside the new leader.
func campaignAndRun(ctx context.Context, leader *scheduler.Leader, sched *scheduler.Scheduler, logger *slog.Logger) {
	ticker := time.NewTicker(leaderCheckInterval)
	defer ticker.Stop()
	// stepDownAt is the wall-clock the previous runAsLeader returned at (i.e.
	// when we transitioned out of leadership). Zero means "no prior step-down
	// in this process" — the first acquisition at boot doesn't record a
	// re-acquire latency (no churn happened yet).
	var stepDownAt time.Time
	for {
		acquired, err := leader.TryAcquire(ctx)
		switch {
		case err != nil:
			logger.Error("acquiring leadership", "error", err)
		case acquired:
			// Record the time we spent stepped down (#311 observability). A
			// growing P99 here surfaces leader churn that affects scheduling
			// latency — operators alert on the histogram, not log content.
			sched.RecordReacquireSince(stepDownAt)
			sched.ClearSteppingDown()
			runAsLeader(ctx, leader, sched, logger)
			if ctx.Err() != nil {
				return // shutting down, not a leadership loss
			}
			stepDownAt = time.Now()
			logger.Warn("stepped down as scheduler leader; re-campaigning")
		default:
			logger.Info("scheduler follower; retrying for leadership")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// runAsLeader runs the scheduling loop while leadership is held. A watchdog
// revalidates the advisory lock and cancels the loop the moment leadership is
// lost, so a stale leader (whose lock-holding session died) steps down instead
// of double-scheduling against the new leader. It signals leadership to the
// scheduler so the heartbeat/health reflects whether this instance is the
// active scheduler.
func runAsLeader(ctx context.Context, leader *scheduler.Leader, sched *scheduler.Scheduler, logger *slog.Logger) {
	logger.Info("became scheduler leader")
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sched.SetLeading(true)
	defer sched.SetLeading(false)

	done := make(chan struct{})
	go func() {
		defer close(done)
		// The watch passes the step-down reason to the scheduler BEFORE
		// canceling the run-context, so the reapers/Step that are about to
		// return ctx-canceled log at WARN ("expected during step-down") not
		// ERROR (#311). A nil scheduler is tolerated by tests.
		watchLeadership(runCtx, leader, leaderCheckInterval, cancel, logger,
			func(reason string) { sched.MarkSteppingDown(reason) })
	}()

	if runErr := sched.Run(runCtx); runErr != nil && !errors.Is(runErr, context.Canceled) {
		logger.Error("scheduler stopped", "error", runErr)
	}
	cancel()
	<-done
	releaseLeader(leader, logger) //nolint:contextcheck // release uses a fresh bounded context
}

// watchLeadership cancels the run when leadership is lost: a definitive "not
// held" steps down immediately, while transient check errors are tolerated up to
// maxLeaderCheckFailures (a connection blip should not churn leadership). It
// returns when the run context is canceled. The onStepDown callback fires
// once, BEFORE cancel(), with the step-down reason so the scheduler can
// classify the cancel fanout (#311) and increment the per-reason counter.
func watchLeadership(ctx context.Context, chk leadershipChecker, interval time.Duration, cancel context.CancelFunc, logger *slog.Logger, onStepDown func(reason string)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	failures := 0
	stepDown := func(reason string) {
		if onStepDown != nil {
			onStepDown(reason)
		}
		cancel()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			held, err := chk.HoldsLock(ctx)
			switch {
			case err != nil:
				if ctx.Err() != nil {
					return // canceled mid-check (shutdown/stepdown)
				}
				failures++
				logger.Warn("leadership check failed", "error", err, "consecutive", failures)
				if failures >= maxLeaderCheckFailures {
					logger.Warn("too many failed leadership checks; stepping down",
						"reason", "check_timeout", "consecutive_failures", failures)
					stepDown("check_timeout")
					return
				}
			case !held:
				logger.Warn("lost scheduler advisory lock; stepping down",
					"reason", "lock_released")
				stepDown("lock_released")
				return
			default:
				failures = 0
			}
		}
	}
}

func releaseLeader(leader *scheduler.Leader, logger *slog.Logger) {
	rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := leader.Release(rctx); err != nil { //nolint:contextcheck // fresh context to release after shutdown
		logger.Error("releasing leadership", "error", err)
	}
}

func serve(s *http.Server, errCh chan<- error) {
	if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		errCh <- fmt.Errorf("serving %s: %w", s.Addr, err)
	}
}

// setupDispatch wires the pod-path executor selected by executor.type onto the
// scheduler and returns whether pod dispatch is active. "subprocess" runs the
// agent on the host (dev only); "kubernetes" (default) launches task pods.
func setupDispatch(ctx context.Context, cfg *config.ServerConfig, sched *scheduler.Scheduler, execStore *storage.ExecutionStore, authn *auth.JWTAuthenticator, store *storage.SchedulerStore, logger *slog.Logger, metrics *observability.Metrics) (bool, io.Closer) {
	if cfg.Executor.Type == "subprocess" {
		return setupSubprocessDispatch(cfg, sched, execStore, authn, logger, store, metrics) //nolint:contextcheck // buffered worker deliberately detaches from caller ctx
	}
	return setupK8sDispatch(ctx, cfg, sched, execStore, authn, store, logger, metrics) //nolint:contextcheck // buffered worker deliberately detaches from caller ctx
}

// resolveAgentControlAddr returns the address task agents dial back, defaulting
// to the server's own gRPC address.
func resolveAgentControlAddr(cfg *config.ServerConfig) string {
	if cfg.Executor.AgentControlPlaneAddr != "" {
		return cfg.Executor.AgentControlPlaneAddr
	}
	return cfg.Server.GRPCAddr
}

// setupSubprocessDispatch wires the dev-only subprocess executor (ADR 0023): it
// runs the agent on the host with no isolation, so it is gated to dev use.
func setupSubprocessDispatch(cfg *config.ServerConfig, sched *scheduler.Scheduler, execStore *storage.ExecutionStore, authn *auth.JWTAuthenticator, logger *slog.Logger, sink dispatch.FailureSink, metrics *observability.Metrics) (bool, io.Closer) {
	subExec := executor.NewSubprocessExecutor(cfg.Executor.AgentPath, logger)
	subExec.SetWorkDir(cfg.Executor.SubprocessWorkDir)
	dispatcher := dispatch.NewDispatcher(subExec, execStore, authn, resolveAgentControlAddr(cfg), agentTokenTTL)
	dispatcher.SetPlatformDefaults(platformDefaults(cfg.Executor.Defaults))
	disp, closer := wrapBuffered(dispatcher, sink, logger, metrics, cfg.Scheduler.Dispatch)
	sched.SetDispatcher(disp)
	logger.Warn("subprocess dispatch enabled (dev only; user code runs unsandboxed)")
	return true, closer
}

// setupK8sDispatch wires the production pod-per-task executor; it is a no-op
// (tasks have no executor and are failed as undispatchable) when no Kubernetes
// client is available.
func setupK8sDispatch(ctx context.Context, cfg *config.ServerConfig, sched *scheduler.Scheduler, execStore *storage.ExecutionStore, authn *auth.JWTAuthenticator, store *storage.SchedulerStore, logger *slog.Logger, metrics *observability.Metrics) (bool, io.Closer) {
	cs, perr := buildK8sClient()
	if perr != nil {
		logger.Warn("pod dispatch disabled; tasks have no executor and will fail as undispatchable", "error", perr)
		return false, nil
	}
	controlAddr := resolveAgentControlAddr(cfg)
	podExec := executor.NewKubernetesExecutor(cs, cfg.Executor.TaskNamespace)
	podExec.SetStagingStore(store) // record per-run staging volumes in the metadatabase (ADR 0022)
	dispatcher := dispatch.NewDispatcher(podExec, execStore, authn, controlAddr, agentTokenTTL)
	dispatcher.SetAgentTLSCAConfigMap(cfg.Executor.AgentTLSCAConfigMap)
	dispatcher.SetTaskSecret(cfg.Executor.TaskSecretName, cfg.Executor.TaskSecretMountPath)
	dispatcher.SetPlatformDefaults(platformDefaults(cfg.Executor.Defaults))
	disp, closer := wrapBuffered(dispatcher, store, logger, metrics, cfg.Scheduler.Dispatch) //nolint:contextcheck // buffered worker deliberately detaches from caller ctx
	sched.SetDispatcher(disp)
	// Let the reapers tear down a reaped task's pod and gate the dispatch-lost
	// decision on real pod liveness (#474, #461). Only wired on the pod path;
	// Lite/subprocess leaves it nil and the reapers stay DB-only.
	sched.SetPodManager(podExec)
	startReconciler(ctx, cs, cfg.Executor.TaskNamespace, execStore, sched.IsLeading, logger)
	startStagingGC(ctx, cs, cfg.Executor.TaskNamespace, store, sched.IsLeading, logger)
	logger.Info("pod dispatch enabled", "namespace", cfg.Executor.TaskNamespace, "agent_control_plane_addr", controlAddr)
	return true, closer
}

// wrapBuffered returns the dispatcher to plug into the scheduler. When
// BufferSize > 0 the inner dispatcher is fronted by the worker pool (#127);
// when BufferSize == 0 the inner dispatcher is used directly (Lite). The
// caller passes a FailureSink (typically the SchedulerStore) so worker-side
// dispatch failures fail the TI with a clear reason instead of leaving it
// stuck `queued`.
// The io.Closer is non-nil only in buffered mode; the caller defers Close() on
// shutdown so in-flight dispatches drain (workers finish or fail via the sink)
// instead of leaking goroutines and leaving TIs stuck `queued` (#133).
func wrapBuffered(inner dispatch.Inner, sink dispatch.FailureSink, logger *slog.Logger, metrics *observability.Metrics, cfg config.DispatchSection) (scheduler.Dispatcher, io.Closer) {
	if cfg.BufferSize <= 0 {
		// Passthrough: keep the inner dispatcher exposed verbatim so the
		// scheduler sees the same surface it always did in Lite. No pool to close.
		return inner, nil
	}
	bd := dispatch.NewBuffered(inner, sink, logger, metrics, dispatch.BufferConfig{
		BufferSize: cfg.BufferSize,
		Workers:    cfg.Workers,
	})
	logger.Info("buffered dispatch enabled (async pool)",
		"buffer_size", cfg.BufferSize, "workers", cfg.Workers)
	return bd, bd
}

// platformDefaults maps the executor.defaults config (L0 task defaults, ADR
// 0023) into the dispatcher's PlatformDefaults. Resources are set only when a
// quantity is configured, so an unset section leaves req.Resources untouched.
func platformDefaults(c config.PlatformDefaultsSection) dispatch.PlatformDefaults {
	d := dispatch.PlatformDefaults{
		StagingSize:         c.StagingSize,
		StagingStorageClass: c.StagingStorageClass,
		StagingAccessMode:   c.StagingAccessMode,
		PodSecurity: executor.PodSecurity{
			RunAsNonRoot:           c.RunTasksAsNonRoot,
			ReadOnlyRootFilesystem: c.ReadOnlyTaskRootFilesystem,
		},
	}
	if c.ResourcesCPU != "" || c.ResourcesMemory != "" {
		d.Resources = &domain.Resources{
			Requests: &domain.ResourceQuantity{CPU: c.ResourcesCPU, Memory: c.ResourcesMemory},
		}
	}
	return d
}
