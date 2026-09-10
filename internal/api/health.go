package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/neochaotic/leoflow/internal/domain"
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

// probeBudget bounds the WHOLE readiness handler, not one step of it, so a
// wedged or slow dependency cannot hold it past the kubelet's own probe timeout
// (the chart's probes.readiness.timeoutSeconds default is 3s; the chart refuses
// to render a value that does not leave room for this one — see
// leoflow.readinessTimeoutSeconds). Overrunning it turns a probe that should
// report "not ready" into a probe that reports nothing at all, and a probe that
// reports nothing writes no log line naming the dependency, which is the
// diagnostic the #1023 fix exists to provide.
//
// It is one deadline shared by every step because a per-step allowance is not a
// budget: bounding the schema query at 2s while leaving Ping unbounded, once per
// dependency, made the handler's worst case Ping(pg) + 2s + Ping(redis) with the
// Pings limited only by whatever the kubelet allowed (#1040). Sharing it means
// the last dependency checked gets what the earlier ones left, which is the
// honest accounting: the caller's timeout is a property of the request, not of
// each thing the request happens to touch.
const probeBudget = 2 * time.Second

func livenessHandler(c *gin.Context) {
	// Deliberately trivial, and deliberately NOT schema-aware: liveness decides
	// whether the kubelet RESTARTS the pod, and restarting creates no schema. A
	// crash loop on a database problem is strictly worse than a pod that is
	// honestly not-ready and stays available for logs and diagnosis.
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// checkDependency runs the full health contract for one dependency: Ping first,
// then — only for a dependency that carries a schema — the schema assertion.
//
// Ping first and short-circuiting, because with the connection down there is
// nothing to learn from a schema query and it would only burn the probe's
// budget. Shared by /readyz and /api/v2/monitor/health so the two endpoints,
// which read the same checks map, cannot come to disagree about whether a
// dependency works — they did, and that was half of #1023's blast radius.
func checkDependency(ctx context.Context, hc HealthChecker) error {
	if err := hc.Ping(ctx); err != nil {
		return err
	}
	sc, ok := hc.(SchemaChecker)
	if !ok {
		return nil
	}
	// No timeout of its own: ctx already carries the handler's whole-probe
	// deadline (probeBudget), and adding a second one here would restore the
	// per-step allowance this deliberately replaced.
	return sc.SchemaReady(ctx)
}

// withProbeBudget derives the single deadline every dependency check shares.
// Both /readyz and /api/v2/monitor/health go through it: they read the same
// checks map and must not come to disagree about how long a dependency is
// allowed to take, for the same reason they must not disagree about whether it
// works.
func withProbeBudget(c *gin.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.Request.Context(), probeBudget)
}

// unreadyDetail turns a dependency failure into the one phrase the caller is
// allowed to see, and picks WHICH phrase by the kind of failure.
//
// The distinction is operational, not cosmetic. The schema check reports through
// a single error, but it answers two questions: "the schema is wrong" and "I
// could not read the schema". A deadline exceeded against the handler's own 2s
// bound, a reset connection, an exhausted pool — all arrive here as a non-nil
// error from the schema call, and calling those "schema not current" points
// whoever is paged at the migration Job for a connectivity problem. The runbook
// makes that exact string a PASS criterion, so conflating them would also make
// the release check pass for the wrong reason.
//
// Both remain 503, and both remain vague on purpose: /readyz is unauthenticated
// (probes carry no token) and the raw error can carry a DSN, credentials or an
// internal hostname (audit H2), so the real cause is logged server-side and the
// response names only the dependency.
func unreadyDetail(name string, err error) string {
	if errors.Is(err, domain.ErrSchemaNotCurrent) {
		return name + " schema not current"
	}
	return name + " unavailable"
}

func readinessHandler(checks map[string]HealthChecker) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := withProbeBudget(c)
		defer cancel()
		for name, hc := range checks {
			if err := checkDependency(ctx, hc); err != nil {
				slog.WarnContext(ctx, "readiness check failed", "dependency", name, "error", err)
				AbortProblem(c, http.StatusServiceUnavailable, "not ready", unreadyDetail(name, err))
				return
			}
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	}
}
