package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/neochaotic/leoflow/internal/domain"
)

func TestGetDagRunHandler(t *testing.T) {
	srv := authedServer() // has run "r1"
	if r := authGet(srv, http.MethodGet, "/api/v2/dags/etl/dagRuns/r1", ""); r.Code != http.StatusOK {
		t.Errorf("existing run = %d, want 200", r.Code)
	}
	if r := authGet(srv, http.MethodGet, "/api/v2/dags/etl/dagRuns/missing", ""); r.Code != http.StatusNotFound {
		t.Errorf("missing run = %d, want 404", r.Code)
	}
}

func TestToDagRunDTODuration(t *testing.T) {
	start := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	end := start.Add(90 * time.Second)

	// Not started yet: no duration.
	if dto := toDagRunDTO(domain.DagRun{DagID: "etl", RunID: "r1"}); dto.Duration != nil {
		t.Errorf("an unstarted run should have nil duration, got %v", *dto.Duration)
	}
	// Started and ended: exact elapsed seconds.
	if dto := toDagRunDTO(domain.DagRun{DagID: "etl", RunID: "r1", StartedAt: &start, EndedAt: &end}); dto.Duration == nil || *dto.Duration != 90 {
		t.Errorf("finished run duration = %v, want 90", dto.Duration)
	}
	// Started but not ended: a positive (now-based) duration, not nil.
	if dto := toDagRunDTO(domain.DagRun{DagID: "etl", RunID: "r1", StartedAt: &start}); dto.Duration == nil || *dto.Duration <= 0 {
		t.Errorf("running run should have a positive duration, got %v", dto.Duration)
	}
	// The data interval is never null (a zero-width window at the logical date).
	dto := toDagRunDTO(domain.DagRun{DagID: "etl", RunID: "r1"})
	if dto.DataIntervalStart == nil || dto.DataIntervalEnd == nil {
		t.Error("data interval must never be null")
	}
	if string(dto.Conf) != "{}" {
		t.Errorf("conf should default to {}, got %s", dto.Conf)
	}
}
