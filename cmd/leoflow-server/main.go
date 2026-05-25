// Command leoflow-server runs the Leoflow control plane: the HTTP API, auth,
// metrics, and (when enabled) the scheduler.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/neochaotic/leoflow/internal/agentrpc"
	"github.com/neochaotic/leoflow/internal/api"
	"github.com/neochaotic/leoflow/internal/auth"
	"github.com/neochaotic/leoflow/internal/config"
	"github.com/neochaotic/leoflow/internal/dispatch"
	"github.com/neochaotic/leoflow/internal/domain"
	"github.com/neochaotic/leoflow/internal/executor"
	"github.com/neochaotic/leoflow/internal/logs"
	"github.com/neochaotic/leoflow/internal/observability"
	"github.com/neochaotic/leoflow/internal/scheduler"
	"github.com/neochaotic/leoflow/internal/secrets"
	"github.com/neochaotic/leoflow/internal/storage"
	"github.com/neochaotic/leoflow/internal/ui"
	"github.com/neochaotic/leoflow/internal/version"
	"github.com/neochaotic/leoflow/internal/xcom"
	agentv1 "github.com/neochaotic/leoflow/proto/agent/v1"
)

func main() {
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
	if xerr := checkServerExpiry(time.Now(), os.Getenv); xerr != nil {
		return xerr
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

	rd, err := storage.NewRedis(ctx, cfg.Redis)
	if err != nil {
		return fmt.Errorf("redis: %w", err)
	}
	defer func() {
		if cerr := rd.Close(); cerr != nil {
			tel.Logger.Error("closing redis", "error", cerr)
		}
	}()

	repo := storage.NewRepository(pg)
	if cerr := configureSecretCipher(repo, cfg.SecretKey, tel.Logger); cerr != nil {
		return cerr
	}
	authn := auth.NewJWTAuthenticator(repo, cfg.Auth.JWT.Secret, time.Duration(cfg.Auth.JWT.TokenTTLSeconds)*time.Second)
	execStore := storage.NewExecutionStore(pg)
	xcomBackend := xcom.NewRedisBackend(rd.Client)
	xcomSvc := xcom.NewService(xcomBackend, storage.NewXComIndex(pg), xcom.DefaultTTL)
	xcomReader := storage.NewXComReader(pg, xcomBackend)

	if err := bootstrapAdmin(ctx, repo, tel.Logger); err != nil {
		return err
	}

	logSink := logs.NewDiskSink(cfg.Logs.Dir)
	logTailer := logs.NewRedisTailer(rd.Client)
	// Secrets are served over the agent channel only when explicitly allowed
	// insecure (dev) until gRPC TLS lands (issue #58); otherwise the handlers
	// fail closed on a plaintext channel.
	allowInsecureSecrets := os.Getenv("LEOFLOW_AGENT_ALLOW_INSECURE_SECRETS") == "true"
	grpcSrv, gerr := startAgentGRPC(ctx, cfg.Server.GRPCAddr, authn, execStore, repo, xcomSvc, logSink, logTailer, allowInsecureSecrets, cfg.Server.GRPCTLSCert, cfg.Server.GRPCTLSKey, tel.Logger)
	if gerr != nil {
		return gerr
	}
	defer grpcSrv.GracefulStop()

	startCleanup(ctx, storage.NewXComIndex(pg), logSink, tel.Logger)

	var schedulerHealth api.Heartbeater
	podDispatch := false
	if cfg.Scheduler.Enabled {
		sched, dispatchOn, serr := startScheduler(ctx, cfg, pg, execStore, authn, xcomSvc, logSink, tel.Logger, tel.Metrics)
		if serr != nil {
			return serr
		}
		schedulerHealth = sched
		podDispatch = dispatchOn
	}
	agentAddr := cfg.Executor.AgentControlPlaneAddr
	if agentAddr == "" {
		agentAddr = cfg.Server.GRPCAddr
	}
	executorInfo := api.ExecutorInfo{
		PodDispatchEnabled:    podDispatch,
		TaskNamespace:         podNamespace,
		AgentControlPlaneAddr: agentAddr,
		InlineConcurrency:     cfg.Executor.HTTP.InlineConcurrencyLimit,
	}

	if cfg.Auth.DevNoAuth {
		tel.Logger.Warn("AUTHENTICATION DISABLED (auth.dev_no_auth): every request is treated as admin. Dev only — NEVER use in production")
	}
	// The DEV overlay in the served UI shell tracks the same dev signal as the
	// auth bypass, so the demo and production never show it.
	uiSrv := ui.New()
	uiSrv.SetLiteBanner(cfg.Auth.DevNoAuth)

	handler := api.NewServer(api.Dependencies{
		Logger:        tel.Logger,
		Authenticator: authn,
		RateLimiter:   auth.NewRateLimiter(5, time.Minute),
		Registry:      tel.Registry,
		Metrics:       tel.Metrics,
		Tracer:        tel.Tracer,
		HealthChecks:  map[string]api.HealthChecker{"postgres": pg, "redis": rd},
		CORSOrigins:   cfg.Server.CORS.AllowedOrigins,
		TokenTTLSecs:  cfg.Auth.JWT.TokenTTLSeconds,
		InstanceName:  cfg.UI.InstanceName,
		DevNoAuth:     cfg.Auth.DevNoAuth,

		InlineHTTPMaxDurationSeconds: cfg.Executor.HTTP.InlineMaxDurationSeconds,
		Dags:                         repo,
		DagRuns:                      repo,
		Tasks:                        repo,
		Versions:                     repo,
		Xcoms:                        xcomReader,
		Logs:                         storage.NewLogReader(pg, logSink, logTailer),
		Specs:                        repo,
		LatestRuns:                   repo,
		TaskSummary:                  repo,
		DagVersions:                  repo,
		DashboardStats:               repo,
		AuditLog:                     repo,
		Variables:                    repo,
		Connections:                  repo,
		Favorites:                    repo,
		ImportErrors:                 repo,
		Audit:                        repo,
		ExecutorInfo:                 executorInfo,
		SchedulerHealth:              schedulerHealth,
		UI:                           uiSrv,
	})

	apiSrv := &http.Server{Addr: cfg.Server.HTTPAddr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	metricsSrv := &http.Server{Addr: cfg.Server.MetricsAddr, Handler: promhttp.HandlerFor(tel.Registry, promhttp.HandlerOpts{}), ReadHeaderTimeout: 10 * time.Second}

	errCh := make(chan error, 2)
	go serve(apiSrv, errCh)
	go serve(metricsSrv, errCh)
	tel.Logger.Info("leoflow-server started", "http_addr", cfg.Server.HTTPAddr, "metrics_addr", cfg.Server.MetricsAddr)

	return awaitShutdown(ctx, errCh, tel.Logger, apiSrv, metricsSrv)
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

// serverExpiryStatus resolves the build's expiry; a var so tests can inject one.
var serverExpiryStatus = version.ExpiryStatus

// checkServerExpiry refuses to start an expired pre-alpha build, mirroring the
// CLI gate. Dev builds (no baked expiry) always pass, as does any build when
// LEOFLOW_IGNORE_EXPIRY is set. getenv is injected for testability.
func checkServerExpiry(now time.Time, getenv func(string) string) error {
	set, at, expired := serverExpiryStatus(now)
	if !set || !expired || getenv("LEOFLOW_IGNORE_EXPIRY") != "" {
		return nil
	}
	return fmt.Errorf("this alpha build expired on %s; download a newer release at %s (or set LEOFLOW_IGNORE_EXPIRY=1 to override)",
		at.Format("2006-01-02"), "https://github.com/neochaotic/leoflow/releases")
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
	logger.Info("connection secret encryption enabled (AES-256-GCM)")
	return nil
}

func bootstrapAdmin(ctx context.Context, repo *storage.Repository, logger *slog.Logger) error {
	pw := os.Getenv("LEOFLOW_BOOTSTRAP_PASSWORD")
	if pw == "" {
		return nil
	}
	created, err := repo.BootstrapAdmin(ctx, "default", "admin@leoflow.local", pw)
	if err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	}
	if created {
		logger.Info("bootstrapped admin user", "email", "admin@leoflow.local")
	}
	return nil
}

// podNamespace is the Kubernetes namespace task pods are created in.
const podNamespace = "leoflow"

// agentTokenTTL is how long a dispatched task's agent identity token stays valid.
const agentTokenTTL = 24 * time.Hour

// inlineStateSink adapts the scheduler store to the inline runner's StateSink,
// recording inline http_api task transitions.
type inlineStateSink struct{ store *storage.SchedulerStore }

func (s inlineStateSink) Transition(ctx context.Context, runID, taskID string, state domain.TaskState) error {
	return s.store.ApplyTransition(ctx, runID, taskID, state)
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

	var opts []grpc.ServerOption
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
// which case pod dispatch is disabled and only inline http_api tasks run.
func buildK8sClient() (kubernetes.Interface, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		cfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{}).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("no in-cluster config or kubeconfig: %w", err)
		}
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("building kubernetes client: %w", err)
	}
	return cs, nil
}

// reconcileInterval is how often the pod reconciler sweeps for failed pods.
const reconcileInterval = 30 * time.Second

// cleanupInterval is how often expired XCom index rows and old logs are purged.
const cleanupInterval = time.Hour

// logRetention is how long task logs are kept before garbage collection.
const logRetention = 30 * 24 * time.Hour

// startCleanup runs a periodic janitor that purges expired XCom index rows and
// prunes old log files. The operations are idempotent, so it is safe on every
// replica.
func startCleanup(ctx context.Context, idx *storage.XComIndex, sink *logs.DiskSink, logger *slog.Logger) {
	go func() {
		t := time.NewTicker(cleanupInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := idx.PurgeExpired(ctx); err != nil {
					logger.Error("purging expired xcom index", "error", err)
				}
				if err := sink.Prune(time.Now(), logRetention); err != nil {
					logger.Error("pruning old logs", "error", err)
				}
			}
		}
	}()
}

// startReconciler runs a periodic pod reconciler that marks task instances
// failed when their pod failed without the agent reporting (feeding retries).
func startReconciler(ctx context.Context, cs kubernetes.Interface, reporter executor.FailureReporter, logger *slog.Logger) {
	rec := executor.NewReconciler(cs, podNamespace, reporter)
	go func() {
		t := time.NewTicker(reconcileInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := rec.Reconcile(ctx); err != nil {
					logger.Error("pod reconcile", "error", err)
				}
			}
		}
	}()
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
func startStagingGC(ctx context.Context, cs kubernetes.Interface, store executor.StagingStore, logger *slog.Logger) {
	exec := executor.NewKubernetesExecutor(cs, podNamespace)
	exec.SetStagingStore(store)
	go func() {
		t := time.NewTicker(stagingGCInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := exec.GCStagingClaims(ctx, stagingTTL); err != nil {
					logger.Error("staging gc", "error", err)
				}
			}
		}
	}()
}

func startScheduler(ctx context.Context, cfg *config.ServerConfig, pg *storage.Postgres, execStore *storage.ExecutionStore, authn *auth.JWTAuthenticator, xcomSvc executor.XComPusher, logSink logs.Sink, logger *slog.Logger, metrics *observability.Metrics) (*scheduler.Scheduler, bool, error) {
	leaderPool, err := storage.NewLeaderPool(ctx, cfg.Database)
	if err != nil {
		return nil, false, fmt.Errorf("leader pool: %w", err)
	}
	store := storage.NewSchedulerStore(pg)
	sched := scheduler.NewScheduler(store, logger,
		time.Duration(cfg.Scheduler.LoopIntervalMS)*time.Millisecond)
	sched.SetRecorder(metrics)
	sched.SetInlineRunner(executor.NewInlineRunner(executor.InlineConfig{
		Sink:        inlineStateSink{store},
		Metrics:     metrics,
		XCom:        xcomSvc,
		Logs:        logSink,
		Concurrency: cfg.Executor.HTTP.InlineConcurrencyLimit,
		MaxSeconds:  cfg.Executor.HTTP.InlineMaxDurationSeconds,
		UserAgent:   cfg.Executor.HTTP.UserAgent,
	}))
	podDispatch := setupDispatch(ctx, cfg, sched, execStore, authn, store, logger)
	leader := scheduler.NewLeader(leaderPool)
	go func() {
		defer leaderPool.Close()
		campaignAndRun(ctx, leader, sched, logger)
	}()
	return sched, podDispatch, nil
}

// campaignAndRun acquires scheduler leadership (polling every 5s) and runs the
// loop only while leader, so a single replica schedules at a time (ADR 0009).
func campaignAndRun(ctx context.Context, leader *scheduler.Leader, sched *scheduler.Scheduler, logger *slog.Logger) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		acquired, err := leader.TryAcquire(ctx)
		switch {
		case err != nil:
			logger.Error("acquiring leadership", "error", err)
		case acquired:
			logger.Info("became scheduler leader")
			if runErr := sched.Run(ctx); runErr != nil && !errors.Is(runErr, context.Canceled) {
				logger.Error("scheduler stopped", "error", runErr)
			}
			releaseLeader(leader, logger) //nolint:contextcheck // release uses a fresh bounded context after shutdown
			return
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
func setupDispatch(ctx context.Context, cfg *config.ServerConfig, sched *scheduler.Scheduler, execStore *storage.ExecutionStore, authn *auth.JWTAuthenticator, store *storage.SchedulerStore, logger *slog.Logger) bool {
	if cfg.Executor.Type == "subprocess" {
		return setupSubprocessDispatch(cfg, sched, execStore, authn, logger)
	}
	return setupK8sDispatch(ctx, cfg, sched, execStore, authn, store, logger)
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
func setupSubprocessDispatch(cfg *config.ServerConfig, sched *scheduler.Scheduler, execStore *storage.ExecutionStore, authn *auth.JWTAuthenticator, logger *slog.Logger) bool {
	subExec := executor.NewSubprocessExecutor(cfg.Executor.AgentPath, logger)
	subExec.SetWorkDir(cfg.Executor.SubprocessWorkDir)
	dispatcher := dispatch.NewDispatcher(subExec, execStore, authn, resolveAgentControlAddr(cfg), agentTokenTTL)
	dispatcher.SetPlatformDefaults(platformDefaults(cfg.Executor.Defaults))
	sched.SetDispatcher(dispatcher)
	logger.Warn("subprocess dispatch enabled (dev only; user code runs unsandboxed)")
	return true
}

// setupK8sDispatch wires the production pod-per-task executor; it is a no-op
// (only inline http_api tasks run) when no Kubernetes client is available.
func setupK8sDispatch(ctx context.Context, cfg *config.ServerConfig, sched *scheduler.Scheduler, execStore *storage.ExecutionStore, authn *auth.JWTAuthenticator, store *storage.SchedulerStore, logger *slog.Logger) bool {
	cs, perr := buildK8sClient()
	if perr != nil {
		logger.Warn("pod dispatch disabled; only inline http_api tasks will run", "error", perr)
		return false
	}
	controlAddr := resolveAgentControlAddr(cfg)
	podExec := executor.NewKubernetesExecutor(cs, podNamespace)
	podExec.SetStagingStore(store) // record per-run staging volumes in the metadatabase (ADR 0022)
	dispatcher := dispatch.NewDispatcher(podExec, execStore, authn, controlAddr, agentTokenTTL)
	dispatcher.SetAgentTLSCAConfigMap(cfg.Executor.AgentTLSCAConfigMap)
	dispatcher.SetPlatformDefaults(platformDefaults(cfg.Executor.Defaults))
	sched.SetDispatcher(dispatcher)
	startReconciler(ctx, cs, execStore, logger)
	startStagingGC(ctx, cs, store, logger)
	logger.Info("pod dispatch enabled", "namespace", podNamespace, "agent_control_plane_addr", controlAddr)
	return true
}

// platformDefaults maps the executor.defaults config (L0 task defaults, ADR
// 0023) into the dispatcher's PlatformDefaults. Resources are set only when a
// quantity is configured, so an unset section leaves req.Resources untouched.
func platformDefaults(c config.PlatformDefaultsSection) dispatch.PlatformDefaults {
	d := dispatch.PlatformDefaults{
		StagingSize:         c.StagingSize,
		StagingStorageClass: c.StagingStorageClass,
		StagingAccessMode:   c.StagingAccessMode,
	}
	if c.ResourcesCPU != "" || c.ResourcesMemory != "" {
		d.Resources = &domain.Resources{
			Requests: &domain.ResourceQuantity{CPU: c.ResourcesCPU, Memory: c.ResourcesMemory},
		}
	}
	return d
}
