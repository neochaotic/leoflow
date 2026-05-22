package api

import (
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/trace"

	"github.com/neochaotic/leoflow/internal/auth"
)

// Dependencies bundles everything the HTTP server needs.
type Dependencies struct {
	Logger        *slog.Logger
	Authenticator auth.Authenticator
	RateLimiter   *auth.RateLimiter
	Registry      *prometheus.Registry
	Metrics       Metrics
	Tracer        trace.Tracer
	HealthChecks  map[string]HealthChecker
	CORSOrigins   []string
	TokenTTLSecs  int

	// Resource repositories. Routes for nil repositories are not registered.
	Dags     DagRepository
	DagRuns  DagRunRepository
	Tasks    TaskInstanceRepository
	Versions DagVersionRepository
}

// NewServer builds the gin engine with the full middleware chain, health and
// metrics endpoints, embedded Scalar docs, and the auth token endpoint.
func NewServer(deps Dependencies) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(RequestID())
	r.Use(Observe(deps.Metrics, deps.Tracer))
	r.Use(StructuredLogger(deps.Logger))
	r.Use(CORS(deps.CORSOrigins))
	r.Use(JWTAuth(deps.Authenticator))

	r.GET("/healthz", livenessHandler)
	r.GET("/readyz", readinessHandler(deps.HealthChecks))
	if deps.Registry != nil {
		r.GET("/metrics", gin.WrapH(promhttp.HandlerFor(deps.Registry, promhttp.HandlerOpts{})))
	}
	registerDocs(r)

	r.POST("/auth/token", authTokenHandler(deps.Authenticator, deps.RateLimiter, deps.TokenTTLSecs))

	registerResources(r, deps)

	return r
}
