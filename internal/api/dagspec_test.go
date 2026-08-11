package api

import (
	"net/http"
	"strings"
	"testing"
)

// TestDagSpecHandler: GET /api/v2/dags/{dag_id}/spec returns the compiled
// dag.json (the structured graph), including the task list — distinct from
// dagSources, which returns the dag.py text.
func TestDagSpecHandler(t *testing.T) {
	srv := structureServer(&fakeSpecReader{spec: diamondSpec()})
	rec := authGet(srv, http.MethodGet, "/api/v2/dags/etl/spec", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"dag_id":"etl"`) {
		t.Errorf("spec missing dag_id: %s", body)
	}
	if !strings.Contains(body, `"load"`) || !strings.Contains(body, `"transform_a"`) {
		t.Errorf("spec missing the task graph: %s", body)
	}
}

// TestDagSpecHandlerNotFound: an unknown DAG surfaces the repo's not-found.
func TestDagSpecHandlerNotFound(t *testing.T) {
	srv := structureServer(&fakeSpecReader{err: ErrNotFound})
	rec := authGet(srv, http.MethodGet, "/api/v2/dags/ghost/spec", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
