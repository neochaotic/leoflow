package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// healthStatusHealthy is the Airflow status string the UI renders green.
const healthStatusHealthy = "healthy"

// monitorHealthHandler implements GET /api/v2/monitor/health (Airflow
// HealthInfoResponse). The Airflow UI's home dashboard polls it to color the
// component health widgets (metadatabase, scheduler, triggerer, dag processor).
//
// Leoflow's single Go control plane subsumes the scheduler, triggerer, and DAG
// processor roles (there are no separate Python daemons), and reaching this
// handler means the metadata database is connected — so all components report
// healthy with a current heartbeat. See docs/ui-compatibility.md.
func monitorHealthHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		now := time.Now().UTC().Format(time.RFC3339)
		c.JSON(http.StatusOK, gin.H{
			"metadatabase": gin.H{"status": healthStatusHealthy},
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
