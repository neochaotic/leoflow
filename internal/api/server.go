package api

import (
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
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
	// TrustedProxies is the set of proxy IPs/CIDRs whose X-Forwarded-For header
	// gin will honor when resolving c.ClientIP(). Empty/nil trusts NO proxy, so
	// ClientIP is the direct peer and a spoofed XFF cannot forge the client IP
	// (audit H1). A Pro deployment behind an ingress sets this to the ingress
	// CIDR so per-client rate-limiting and audit see the real client.
	TrustedProxies []string
	TokenTTLSecs   int
	// InstanceName is shown in the UI navbar (Airflow's instance_name). Empty
	// falls back to "Leoflow"; `leoflow dev` sets it to mark the DEV environment.
	InstanceName string
	// UIAutoRefreshIntervalSeconds controls the SPA's polling cadence for DAG /
	// DagRun / task-instance state refresh (Airflow's auto_refresh_interval).
	// Non-positive (the zero default) falls back to DefaultUIAutoRefreshIntervalSeconds
	// (30s, production-safe). `leoflow lite` sets it to ~5s for a snappy inner loop.
	UIAutoRefreshIntervalSeconds int
	// DevNoAuth replaces JWT auth with a dev-only bypass that authenticates every
	// request as an admin (no login). It is for `leoflow dev` only and must never
	// be set in production. See DevBypassAuth.
	DevNoAuth bool
	// Edition marks the running edition ("pro", "lite", or empty). It gates
	// Pro-only surfaces: named-pool CRUD is registered as real endpoints only when
	// Edition == "pro" (ADR 0053), otherwise the Pools screen gets the graceful
	// empty-collection stub, matching how the scheduler's pool gate is Pro-gated.
	Edition string

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
	Users          UserStore
	UserAudit      UserAuditWriter
	Connections    ConnectionStore
	ConnectionTest ConnectionTester
	Pools          PoolStore
	Favorites      FavoriteStore
	ImportErrors   ImportErrorStore
	Audit          AuditWriter
	ExecutorInfo   ExecutorInfo

	// Workspace backs the Lite web editor (ADR 0025). When nil the editor's
	// filesystem API is not registered (Production, or Lite without a workspace).
	Workspace WorkspaceFS

	// MonacoDir is the directory holding the pinned Monaco bundle that
	// `leoflow setup` fetched; the editor page is served Monaco from it. Empty or
	// missing makes the page show a setup hint instead of a broken editor.
	MonacoDir string

	// ExamplesFS backs the IDE's "Download examples" button — typically the
	// `embed.FS` shipped from the leoflow root package. Nil disables the button.
	ExamplesFS fs.FS

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
	// Trust only the explicitly-configured proxies for X-Forwarded-For; the empty
	// default trusts none, so c.ClientIP() is the direct peer and a spoofed XFF
	// cannot forge it (audit H1). An invalid CIDR fails SECURE — trust none — not
	// open.
	if err := r.SetTrustedProxies(deps.TrustedProxies); err != nil {
		deps.Logger.Error("invalid trusted_proxies; trusting no proxy", "error", err)
		if resetErr := r.SetTrustedProxies(nil); resetErr != nil {
			deps.Logger.Error("resetting trusted proxies to none failed", "error", resetErr)
		}
	}
	// Disable auto-redirect on trailing slash (#291): the 301 it writes
	// bypasses NoStoreOnVolatileRoutes, so the browser can cache the bare
	// 301 and short-circuit the next request. Bare paths register explicit
	// routes alongside their *action wildcards.
	r.RedirectTrailingSlash = false
	r.Use(gin.Recovery())
	r.Use(RequestID())
	r.Use(Observe(deps.Metrics, deps.Tracer))
	r.Use(StructuredLogger(deps.Logger))
	r.Use(CORS(deps.CORSOrigins))
	r.Use(NoStoreOnVolatileRoutes())
	if deps.DevNoAuth {
		r.Use(DevBypassAuth())
	} else {
		r.Use(JWTAuth(deps.Authenticator))
	}

	r.GET("/healthz", livenessHandler)
	r.GET("/readyz", readinessHandler(deps.HealthChecks))
	// /metrics is intentionally NOT served here (audit H2): scraping lives on the
	// dedicated observability listener (ObservabilityHandler on the metrics port),
	// which every role runs, so metrics can be firewalled separately from the
	// public API/UI surface. deps.Registry is retained for that listener's wiring.
	registerDocs(r)

	r.POST("/auth/token", authTokenHandler(deps.Authenticator, deps.RateLimiter, deps.TokenTTLSecs))
	// The Airflow UI redirects unauthenticated users to GET /api/v2/auth/login.
	r.GET("/api/v2/auth/login", loginPageHandler())
	r.GET("/api/v2/auth/logout", logoutHandler())
	r.GET("/api/v2/monitor/health", monitorHealthHandler(deps.HealthChecks, deps.SchedulerHealth))
	r.GET("/api/v2/monitor/executor", monitorExecutorHandler(deps.ExecutorInfo))

	registerResources(r, deps)
	registerUI(r, deps.TokenTTLSecs, deps.InstanceName, deps.UIAutoRefreshIntervalSeconds)
	registerUIViews(r, deps)
	registerUIStructure(r, deps.Specs)
	registerUISummaries(r, deps.TaskSummary)
	registerUITasks(r, deps.Specs)
	registerUIDashboard(r, deps.DashboardStats)
	registerUIAudit(r, deps.AuditLog)
	registerUIVariables(r, deps.Variables)
	registerUsers(r, deps.Users, deps.UserAudit)
	registerUIConnections(r, deps.Connections, deps.ConnectionTest)
	registerUIPools(r, deps.Pools, deps.Edition == "pro")
	registerUIFavorites(r, deps.Favorites)
	registerImportErrors(r, deps.ImportErrors)
	registerIDE(r, deps.Workspace, deps.MonacoDir, deps.ExamplesFS)
	registerUIStubs(r)
	registerAPIStubs(r)
	if deps.UI != nil {
		static := gin.WrapH(http.StripPrefix("/static", deps.UI.StaticHandler()))
		r.GET("/static/*filepath", static)
		r.HEAD("/static/*filepath", static)
	}
	r.NoRoute(uiNoRoute(deps.UI, deps.Authenticator, deps.DevNoAuth))

	return r
}
