// Command leoflow-server runs the Leoflow control plane: the HTTP API, auth,
// metrics, and (when enabled) the scheduler.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/neochaotic/leoflow/internal/api"
	"github.com/neochaotic/leoflow/internal/auth"
	"github.com/neochaotic/leoflow/internal/config"
	"github.com/neochaotic/leoflow/internal/observability"
	"github.com/neochaotic/leoflow/internal/scheduler"
	"github.com/neochaotic/leoflow/internal/storage"
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

	if err := bootstrapAdmin(ctx, repo, tel.Logger); err != nil {
		return err
	}
	if cfg.Scheduler.Enabled {
		startScheduler(ctx, cfg, pg, tel.Logger)
	}

	handler := api.NewServer(api.Dependencies{
		Logger:        tel.Logger,
		Authenticator: authn,
		RateLimiter:   auth.NewRateLimiter(5, time.Minute),
		Registry:      tel.Registry,
		HealthChecks:  map[string]api.HealthChecker{"postgres": pg, "redis": rd},
		CORSOrigins:   cfg.Server.CORS.AllowedOrigins,
		TokenTTLSecs:  cfg.Auth.JWT.TokenTTLSeconds,
		Dags:          repo,
		DagRuns:       repo,
		Tasks:         repo,
		Versions:      repo,
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

func startScheduler(ctx context.Context, cfg *config.ServerConfig, pg *storage.Postgres, logger *slog.Logger) {
	sched := scheduler.NewScheduler(storage.NewSchedulerStore(pg), logger,
		time.Duration(cfg.Scheduler.LoopIntervalMS)*time.Millisecond)
	go func() {
		if err := sched.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("scheduler stopped", "error", err)
		}
	}()
}

func serve(s *http.Server, errCh chan<- error) {
	if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		errCh <- fmt.Errorf("serving %s: %w", s.Addr, err)
	}
}
