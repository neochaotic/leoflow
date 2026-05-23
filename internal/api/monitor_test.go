package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

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
