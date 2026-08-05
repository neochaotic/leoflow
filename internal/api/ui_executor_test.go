package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func executorEngine(info ExecutorInfo) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/v2/monitor/executor", monitorExecutorHandler(info))
	return r
}

func TestMonitorExecutorReportsDispatch(t *testing.T) {
	cases := []struct {
		name      string
		info      ExecutorInfo
		wantModes int
		wantPod   bool
	}{
		{"pod dispatch on", ExecutorInfo{PodDispatchEnabled: true, TaskNamespace: "leoflow"}, 1, true},
		{"pod dispatch off", ExecutorInfo{PodDispatchEnabled: false}, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v2/monitor/executor", http.NoBody)
			rec := httptest.NewRecorder()
			executorEngine(tc.info).ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			var got struct {
				PodDispatchEnabled bool     `json:"pod_dispatch_enabled"`
				ExecutionModes     []string `json:"execution_modes"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.PodDispatchEnabled != tc.wantPod {
				t.Errorf("pod_dispatch_enabled = %v, want %v", got.PodDispatchEnabled, tc.wantPod)
			}
			if len(got.ExecutionModes) != tc.wantModes {
				t.Errorf("execution_modes = %v, want %d modes", got.ExecutionModes, tc.wantModes)
			}
		})
	}
}
