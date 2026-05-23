package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// Airflow health status strings the UI renders (green / red).
const (
	healthStatusHealthy   = "healthy"
	healthStatusUnhealthy = "unhealthy"
)

// monitorHealthHandler implements GET /api/v2/monitor/health (Airflow
// HealthInfoResponse). The Airflow UI's home dashboard polls it to color the
// component health widgets (metadatabase, scheduler, triggerer, dag processor).
//
// The metadatabase status is a real probe — it pings the "postgres" health
// checker. Leoflow's single Go control plane subsumes the scheduler, triggerer,
// and DAG processor roles (there are no separate Python daemons); since this
// process serves the request, those report healthy with a current heartbeat.
// See docs/ui-compatibility.md.
func monitorHealthHandler(checks map[string]HealthChecker) gin.HandlerFunc {
	return func(c *gin.Context) {
		now := time.Now().UTC().Format(time.RFC3339)
		dbStatus := healthStatusHealthy
		if hc, ok := checks["postgres"]; ok {
			if err := hc.Ping(c.Request.Context()); err != nil {
				dbStatus = healthStatusUnhealthy
			}
		}
		c.JSON(http.StatusOK, gin.H{
			"metadatabase": gin.H{"status": dbStatus},
			"scheduler": gin.H{
				"status":                     healthStatusHealthy,
				"latest_scheduler_heartbeat": now,
			},
			"triggerer": gin.H{
				"status":                     healthStatusHealthy,
				"latest_triggerer_heartbeat": now,
			},
			"dag_processor": gin.H{
				"status":                         healthStatusHealthy,
				"latest_dag_processor_heartbeat": now,
			},
		})
	}
}
