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
	"github.com/neochaotic/leoflow/internal/storage"
	"github.com/neochaotic/leoflow/internal/ui"
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
	grpcSrv, gerr := startAgentGRPC(ctx, cfg.Server.GRPCAddr, authn, execStore, xcomSvc, logSink, logTailer, tel.Logger)
	if gerr != nil {
		return gerr
	}
	defer grpcSrv.GracefulStop()

	startCleanup(ctx, storage.NewXComIndex(pg), logSink, tel.Logger)

	if cfg.Scheduler.Enabled {
		if serr := startScheduler(ctx, cfg, pg, execStore, authn, xcomSvc, logSink, tel.Logger, tel.Metrics); serr != nil {
			return serr
		}
	}

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
		UI:                           ui.New(),
	})

	apiSrv := &http.Server{Addr: cfg.Server.HTTPAddr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	metricsSrv := &http.Server{Addr: cfg.Server.MetricsAddr, Handler: promhttp.HandlerFor(tel.Registry, promhttp.HandlerOpts{}), ReadHeaderTimeout: 10 * time.Second}

	errCh := make(chan error, 2)
	go serve(apiSrv, errCh)
	go serve(metricsSrv, errCh)
	tel.Logger.Info("leoflow-server started", "http_addr", cfg.Server.HTTPAddr, "metrics_addr", cfg.Server.MetricsAddr)

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		tel.Logger.Info("shutting down")
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if serr := apiSrv.Shutdown(shutCtx); serr != nil {
			tel.Logger.Error("api shutdown", "error", serr)
		}
		if serr := metricsSrv.Shutdown(shutCtx); serr != nil {
			tel.Logger.Error("metrics shutdown", "error", serr)
		}
		return nil
	}
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

// startAgentGRPC starts the AgentService gRPC server (insecure transport; the
// per-task bearer token in metadata authenticates each call) and returns it for
// graceful shutdown.
func startAgentGRPC(ctx context.Context, addr string, authn *auth.JWTAuthenticator, store *storage.ExecutionStore, xcomSvc agentrpc.XComService, logSink agentrpc.LogSink, logTailer agentrpc.LogPublisher, logger *slog.Logger) (*grpc.Server, error) {
	var lc net.ListenConfig
	lis, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listening for agent grpc on %s: %w", addr, err)
	}
	agentSrv := agentrpc.NewServer(authn, store, xcomSvc)
	agentSrv.SetLogSink(logSink)
	agentSrv.SetLogPublisher(logTailer)
	srv := grpc.NewServer()
	agentv1.RegisterAgentServiceServer(srv, agentSrv)
	go func() {
		if serr := srv.Serve(lis); serr != nil && !errors.Is(serr, grpc.ErrServerStopped) {
			logger.Error("agent grpc server", "error", serr)
		}
	}()
	logger.Info("agent grpc server started", "grpc_addr", addr)
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

func startScheduler(ctx context.Context, cfg *config.ServerConfig, pg *storage.Postgres, execStore *storage.ExecutionStore, authn *auth.JWTAuthenticator, xcomSvc executor.XComPusher, logSink logs.Sink, logger *slog.Logger, metrics *observability.Metrics) error {
	leaderPool, err := storage.NewLeaderPool(ctx, cfg.Database)
	if err != nil {
		return fmt.Errorf("leader pool: %w", err)
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
	if cs, perr := buildK8sClient(); perr == nil {
		controlAddr := cfg.Executor.AgentControlPlaneAddr
		if controlAddr == "" {
			controlAddr = cfg.Server.GRPCAddr
		}
		podExec := executor.NewKubernetesExecutor(cs, podNamespace)
		sched.SetDispatcher(dispatch.NewDispatcher(podExec, execStore, authn, controlAddr, agentTokenTTL))
		startReconciler(ctx, cs, execStore, logger)
		logger.Info("pod dispatch enabled", "namespace", podNamespace, "agent_control_plane_addr", controlAddr)
	} else {
		logger.Warn("pod dispatch disabled; only inline http_api tasks will run", "error", perr)
	}
	leader := scheduler.NewLeader(leaderPool)
	go func() {
		defer leaderPool.Close()
		campaignAndRun(ctx, leader, sched, logger)
	}()
	return nil
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
