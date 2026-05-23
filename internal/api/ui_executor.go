package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// ExecutorInfo describes the control plane's execution capacity. It surfaces
// whether pod dispatch is available — the cluster-level answer to "why is a task
// stuck queued" (#46/#47). The stock Airflow UI has no widget for it, but
// operators (curl/monitoring) and a future custom Cluster Activity view consume
// it. Cluster Activity in Airflow 3.2 is otherwise the Home dashboard, already
// backed by /api/v2/monitor/health (#33) and /ui/dashboard/* (#39).
type ExecutorInfo struct {
	PodDispatchEnabled    bool
	TaskNamespace         string
	AgentControlPlaneAddr string
	InlineConcurrency     int
}

// monitorExecutorHandler implements GET /api/v2/monitor/executor.
func monitorExecutorHandler(info ExecutorInfo) gin.HandlerFunc {
	return func(c *gin.Context) {
		modes := []string{"inline_http_api"}
		if info.PodDispatchEnabled {
			modes = append(modes, "kubernetes_pod")
		}
		c.JSON(http.StatusOK, gin.H{
			"pod_dispatch_enabled":     info.PodDispatchEnabled,
			"task_namespace":           info.TaskNamespace,
			"agent_control_plane_addr": info.AgentControlPlaneAddr,
			"inline_http_api": gin.H{
				"enabled":           true,
				"concurrency_limit": info.InlineConcurrency,
			},
			"execution_modes": modes,
		})
	}
}
