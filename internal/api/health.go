package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// HealthChecker reports dependency health for readiness checks.
type HealthChecker interface {
	Ping(ctx context.Context) error
}

// SchemaChecker is an optional capability a HealthChecker may also implement:
// asserting that the dependency's SCHEMA — not just its connection — is the one
// this binary requires. *storage.Postgres implements it; Redis and other
// schema-less dependencies do not, and keep the Ping-only contract.
//
// It exists because a Ping is not evidence of a usable database (#1023). The
// invariant is enforced at boot, where the server refuses to start against a
// database whose schema_migrations is absent or behind — but a boot check says
// nothing about a database that changes UNDER a running pod, which is exactly
// what an ephemeral-volume recycle, a restore from an older backup, a failover
// to a lagging replica, or a repointed database.url does.
type SchemaChecker interface {
	SchemaReady(ctx context.Context) error
}

// schemaProbeTimeout bounds the schema query so a wedged or slow database cannot
// hold the handler past the kubelet's own probe timeout (the chart's
// probes.readiness.timeoutSeconds default is 3s). Overrunning it turns a probe
// that should report "not ready" into a probe that reports nothing at all.
const schemaProbeTimeout = 2 * time.Second

func livenessHandler(c *gin.Context) {
	// Deliberately trivial, and deliberately NOT schema-aware: liveness decides
	// whether the kubelet RESTARTS the pod, and restarting creates no schema. A
	// crash loop on a database problem is strictly worse than a pod that is
	// honestly not-ready and stays available for logs and diagnosis.
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func readinessHandler(checks map[string]HealthChecker) gin.HandlerFunc {
	return func(c *gin.Context) {
		for name, hc := range checks {
			if err := hc.Ping(c.Request.Context()); err != nil {
				// /readyz is unauthenticated (probes carry no token), so the raw
				// dependency error — which can carry a DSN, credentials, or internal
				// hostnames — must not go in the response (audit H2). Log the real
				// cause server-side; tell the caller only which dependency is unready.
				slog.WarnContext(c.Request.Context(), "readiness check failed", "dependency", name, "error", err)
				AbortProblem(c, http.StatusServiceUnavailable, "not ready", name+" unavailable")
				return
			}
			sc, ok := hc.(SchemaChecker)
			if !ok {
				continue
			}
			// Ping passed, so the connection is up; ask whether what is behind it is
			// still the schema this binary requires.
			ctx, cancel := context.WithTimeout(c.Request.Context(), schemaProbeTimeout)
			err := sc.SchemaReady(ctx)
			cancel()
			if err != nil {
				// Same audit H2 reasoning as above: the version gap and any connection
				// detail stay in the log, and the response only names the dependency.
				slog.WarnContext(c.Request.Context(), "readiness schema check failed", "dependency", name, "error", err)
				AbortProblem(c, http.StatusServiceUnavailable, "not ready", name+" schema not current")
				return
			}
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	}
}
