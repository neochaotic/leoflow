package observability

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

// Config configures observability setup.
type Config struct {
	ServiceName  string
	LogLevel     string
	LogFormat    string
	OTelEnabled  bool
	OTelEndpoint string
}

// Telemetry bundles the configured observability primitives.
type Telemetry struct {
	Logger   *slog.Logger
	Metrics  *Metrics
	Registry *prometheus.Registry
	Tracer   trace.Tracer
}

// Setup builds logging, metrics, and tracing from cfg and returns the telemetry
// bundle plus a shutdown function the caller must defer.
func Setup(ctx context.Context, cfg Config) (*Telemetry, func(), error) {
	logger := NewLogger(cfg.LogLevel, cfg.LogFormat)
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(reg)

	shutdown := func() {}
	if cfg.OTelEnabled {
		tp, err := newTracerProvider(ctx, cfg.OTelEndpoint, cfg.ServiceName)
		if err != nil {
			return nil, nil, err
		}
		otel.SetTracerProvider(tp)
		// shutdown runs after Setup's ctx may already be done, so it uses a
		// fresh bounded context rather than deriving from the startup ctx.
		shutdown = func() { //nolint:contextcheck // intentional fresh context for graceful shutdown
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := tp.Shutdown(sctx); err != nil {
				logger.Error("otel tracer shutdown", "error", err)
			}
		}
	}

	return &Telemetry{
		Logger:   logger,
		Metrics:  metrics,
		Registry: reg,
		Tracer:   otel.Tracer(cfg.ServiceName),
	}, shutdown, nil
}
