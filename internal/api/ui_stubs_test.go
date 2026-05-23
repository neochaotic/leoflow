package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/neochaotic/leoflow/internal/auth"
)

func stubsServer() *gin.Engine {
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
	})
}

func TestUIStubsReturnSchemaValidEmpties(t *testing.T) {
	srv := stubsServer()

	// Collections: {total_entries:0, <field>:[]}.
	collections := map[string]string{
		"/ui/calendar/etl": "dag_runs",
		"/ui/backfills":    "backfills",
		"/ui/teams":        "teams",
	}
	for path, field := range collections {
		rec := authGet(srv, http.MethodGet, path, "")
		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200", path, rec.Code)
			continue
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if string(body["total_entries"]) != "0" {
			t.Errorf("%s total_entries = %s", path, body["total_entries"])
		}
		if string(body[field]) != "[]" {
			t.Errorf("%s %s = %s, want []", path, field, body[field])
		}
	}

	// hook_meta is a bare array.
	if rec := authGet(srv, http.MethodGet, "/ui/connections/hook_meta", ""); rec.Body.String() != "[]" {
		t.Errorf("hook_meta = %q, want []", rec.Body.String())
	}

	// dag_stats carries its four required counts.
	rec := authGet(srv, http.MethodGet, "/ui/dashboard/dag_stats", "")
	var stats map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &stats); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"active_dag_count", "failed_dag_count", "running_dag_count", "queued_dag_count"} {
		if _, ok := stats[f]; !ok {
			t.Errorf("dag_stats missing %q", f)
		}
	}

	// historical metrics: state-count objects present, not the catch-all {}.
	rec = authGet(srv, http.MethodGet, "/ui/dashboard/historical_metrics_data", "")
	var hist struct {
		TaskInstanceStates map[string]int `json:"task_instance_states"`
		StateCountLimit    *int           `json:"state_count_limit"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &hist); err != nil {
		t.Fatal(err)
	}
	if _, ok := hist.TaskInstanceStates["success"]; !ok || hist.StateCountLimit == nil {
		t.Errorf("historical_metrics_data not schema-valid: %s", rec.Body.String())
	}

	// dependencies graph: edges + nodes arrays.
	rec = authGet(srv, http.MethodGet, "/ui/dependencies", "")
	var graph struct {
		Edges []any `json:"edges"`
		Nodes []any `json:"nodes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &graph); err != nil {
		t.Fatalf("dependencies: %v (%s)", err, rec.Body.String())
	}
}

func TestUIStubWritesStill501(t *testing.T) {
	// A write to an unimplemented /ui path degrades to 501 (via NoRoute), even
	// where a GET stub exists.
	rec := authGet(stubsServer(), http.MethodPost, "/ui/backfills", "{}")
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("POST /ui/backfills = %d, want 501", rec.Code)
	}
}
