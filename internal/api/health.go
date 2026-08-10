package api

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
)

// HealthChecker reports dependency health for readiness checks.
type HealthChecker interface {
	Ping(ctx context.Context) error
}

func livenessHandler(c *gin.Context) {
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
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	}
}
