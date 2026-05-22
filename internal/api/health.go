package api

import (
	"context"
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
				AbortProblem(c, http.StatusServiceUnavailable, "not ready", name+": "+err.Error())
				return
			}
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	}
}
