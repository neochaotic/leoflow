package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/neochaotic/leoflow/internal/auth"
	"github.com/neochaotic/leoflow/internal/domain"
)

func viewsServer(runs []domain.DagRun, user *auth.User) *gin.Engine {
	if user == nil {
		user = &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}
	}
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: user},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		DagRuns:       &fakeRunRepo{runs: runs},
	})
}

func TestVersionEndpoint(t *testing.T) {
	rec := authGet(viewsServer(nil, nil), http.MethodGet, "/api/v2/version", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/v2/version = %d, want 200", rec.Code)
	}
	var v map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if _, ok := v["version"]; !ok {
		t.Errorf("missing version field: %v", v)
	}
	if _, ok := v["git_version"]; !ok {
		t.Errorf("missing git_version field: %v", v)
	}
}

func TestGridRunsShape(t *testing.T) {
	start := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	end := start.Add(90 * time.Second)
	runs := []domain.DagRun{{
		DagID: "etl", RunID: "r1", State: domain.DagRunStateSuccess, RunType: "scheduled",
		LogicalDate: start, QueuedAt: start, StartedAt: &start, EndedAt: &end,
	}}
	rec := authGet(viewsServer(runs, nil), http.MethodGet, "/ui/grid/runs/etl", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("/ui/grid/runs = %d (%s)", rec.Code, rec.Body.String())
	}
	var cols []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &cols); err != nil {
		t.Fatal(err)
	}
	if len(cols) != 1 {
		t.Fatalf("want 1 grid column, got %d", len(cols))
	}
	col := cols[0]
	for _, f := range []string{"dag_id", "run_id", "state", "run_type", "run_after", "has_missed_deadline", "duration"} {
		if _, ok := col[f]; !ok {
			t.Errorf("grid run missing required field %q", f)
		}
	}
	if col["duration"].(float64) != 90 {
		t.Errorf("duration = %v, want 90", col["duration"])
	}
	if col["run_id"] != "r1" {
		t.Errorf("run_id = %v", col["run_id"])
	}
}

func TestGridRunsEmpty(t *testing.T) {
	rec := authGet(viewsServer(nil, nil), http.MethodGet, "/ui/grid/runs/etl", "")
	if rec.Code != http.StatusOK || rec.Body.String() != "[]" {
		t.Errorf("empty grid runs = %d %q, want 200 []", rec.Code, rec.Body.String())
	}
}

func TestLatestRunShape(t *testing.T) {
	logical := time.Date(2026, 5, 22, 0, 0, 0, 0, time.UTC)
	runs := []domain.DagRun{{DagID: "etl", RunID: "r9", State: domain.DagRunStateRunning, LogicalDate: logical}}
	rec := authGet(viewsServer(runs, nil), http.MethodGet, "/ui/dags/etl/latest_run", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("latest_run = %d", rec.Code)
	}
	var lr map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &lr); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"id", "dag_id", "run_id", "logical_date", "run_after", "state", "duration"} {
		if _, ok := lr[f]; !ok {
			t.Errorf("latest_run missing required field %q", f)
		}
	}
	if lr["run_id"] != "r9" {
		t.Errorf("run_id = %v", lr["run_id"])
	}
}

func TestLatestRunNoneReturnsNull(t *testing.T) {
	// Spec: DAGRunLightResponse|null — 200 null when there is no run, not 404.
	rec := authGet(viewsServer(nil, nil), http.MethodGet, "/ui/dags/etl/latest_run", "")
	if rec.Code != http.StatusOK || rec.Body.String() != "null" {
		t.Errorf("no-run latest_run = %d %q, want 200 null", rec.Code, rec.Body.String())
	}
}

func TestUIViewsRequireReadPermission(t *testing.T) {
	viewer := &auth.User{ID: "u2", TenantID: "default",
		Permissions: []auth.Permission{{Action: "read", Resource: "dag"}}} // no dag_run
	rec := authGet(viewsServer(nil, viewer), http.MethodGet, "/ui/grid/runs/etl", "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("read:dag_run without permission = %d, want 403", rec.Code)
	}
}
