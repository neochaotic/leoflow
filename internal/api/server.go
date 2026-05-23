package api

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/trace"

	"github.com/neochaotic/leoflow/internal/auth"
)

// UIServer serves the embedded single-page app: static assets and an
// index.html shell that the SPA's client-side router falls back to. It is
// satisfied by internal/ui.Server. When nil, the server runs API-only and
// unknown paths return 404 instead of the SPA shell.
type UIServer interface {
	StaticHandler() http.Handler
	Index(w http.ResponseWriter, basePath string)
}

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

	// InlineHTTPMaxDurationSeconds caps inline http_api task timeouts at push
	// time. Zero falls back to domain.DefaultInlineMaxDurationSeconds.
	InlineHTTPMaxDurationSeconds int

	// Resource repositories. Routes for nil repositories are not registered.
	Dags           DagRepository
	DagRuns        DagRunRepository
	Tasks          TaskInstanceRepository
	Versions       DagVersionRepository
	Xcoms          XComReader
	Logs           LogReader
	Specs          DagSpecReader
	LatestRuns     DagLatestRunsReader
	TaskSummary    TaskSummaryReader
	DagVersions    DagVersionLister
	DashboardStats DashboardStatsReader
	AuditLog       AuditLogReader
	Variables      VariableStore
	Connections    ConnectionStore

	// SchedulerHealth reports the scheduler's heartbeat for /monitor/health.
	// When nil the component reports healthy (single-process role assumption).
	SchedulerHealth Heartbeater

	// UI serves the embedded SPA. When nil the server is API-only.
	UI UIServer
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
	// The Airflow UI redirects unauthenticated users to GET /api/v2/auth/login.
	r.GET("/api/v2/auth/login", loginPageHandler())
	r.GET("/api/v2/auth/logout", logoutHandler())
	r.GET("/api/v2/monitor/health", monitorHealthHandler(deps.HealthChecks, deps.SchedulerHealth))

	registerResources(r, deps)
	registerUI(r, deps.TokenTTLSecs)
	registerUIViews(r, deps)
	registerUIStructure(r, deps.Specs)
	registerUISummaries(r, deps.TaskSummary)
	registerUITasks(r, deps.Specs)
	registerUIDashboard(r, deps.DashboardStats)
	registerUIAudit(r, deps.AuditLog)
	registerUIVariables(r, deps.Variables)
	registerUIConnections(r, deps.Connections)
	registerUIStubs(r)
	registerAPIStubs(r)
	if deps.UI != nil {
		static := gin.WrapH(http.StripPrefix("/static", deps.UI.StaticHandler()))
		r.GET("/static/*filepath", static)
		r.HEAD("/static/*filepath", static)
	}
	r.NoRoute(uiNoRoute(deps.UI))

	return r
}
