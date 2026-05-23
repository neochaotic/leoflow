package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type fakeHeartbeater struct {
	healthy bool
	last    time.Time
}

func (f fakeHeartbeater) Heartbeat() (bool, time.Time) { return f.healthy, f.last }

func TestMonitorHealthAllComponentsHealthy(t *testing.T) {
	rec := authGet(stubsServer(), http.MethodGet, "/api/v2/monitor/health", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/v2/monitor/health = %d, want 200", rec.Code)
	}
	var h struct {
		Metadatabase struct {
			Status string `json:"status"`
		} `json:"metadatabase"`
		Scheduler struct {
			Status    string `json:"status"`
			Heartbeat string `json:"latest_scheduler_heartbeat"`
		} `json:"scheduler"`
		Triggerer struct {
			Status string `json:"status"`
		} `json:"triggerer"`
		DagProcessor struct {
			Status string `json:"status"`
		} `json:"dag_processor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &h); err != nil {
		t.Fatal(err)
	}
	if h.Metadatabase.Status != "healthy" || h.Scheduler.Status != "healthy" ||
		h.Triggerer.Status != "healthy" || h.DagProcessor.Status != "healthy" {
		t.Errorf("all components should be healthy, got %s", rec.Body.String())
	}
	if h.Scheduler.Heartbeat == "" {
		t.Errorf("scheduler heartbeat should be set")
	}
}

func TestMonitorHealthReflectsSchedulerHeartbeat(t *testing.T) {
	gin.SetMode(gin.TestMode)
	last := time.Date(2026, 5, 23, 4, 0, 0, 0, time.UTC)

	serve := func(hb Heartbeater) map[string]map[string]string {
		r := gin.New()
		r.GET("/h", monitorHealthHandler(nil, hb))
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/h", http.NoBody)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		var h map[string]map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &h); err != nil {
			t.Fatal(err)
		}
		return h
	}

	// A stalled scheduler turns scheduler/triggerer/dag_processor unhealthy.
	stalled := serve(fakeHeartbeater{healthy: false, last: last})
	for _, comp := range []string{"scheduler", "triggerer", "dag_processor"} {
		if stalled[comp]["status"] != "unhealthy" {
			t.Errorf("%s status = %q, want unhealthy", comp, stalled[comp]["status"])
		}
	}
	if stalled["scheduler"]["latest_scheduler_heartbeat"] != last.Format(time.RFC3339) {
		t.Errorf("heartbeat = %q, want %s", stalled["scheduler"]["latest_scheduler_heartbeat"], last.Format(time.RFC3339))
	}

	// A live scheduler reports healthy.
	live := serve(fakeHeartbeater{healthy: true, last: last})
	if live["scheduler"]["status"] != "healthy" {
		t.Errorf("live scheduler status = %q, want healthy", live["scheduler"]["status"])
	}
}
