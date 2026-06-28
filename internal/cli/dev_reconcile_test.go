package cli

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"
)

func set(ids ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		m[id] = struct{}{}
	}
	return m
}

// TestReconcileStartup_DeregistersGhosts: on boot, DAGs the control plane still
// has registered that are NOT in the current workspace (stale from a reused
// metadata DB or a previous workspace, #404) are deregistered, so Lite self-heals
// to match the workspace without manual cleanup.
func TestReconcileStartup_DeregistersGhosts(t *testing.T) {
	var deleted []string
	del := func(id string) error { deleted = append(deleted, id); return nil }

	seen := reconcileStartup(set("hello", "ghost_a", "ghost_b"), nil, set("hello"), del, func(string, ...any) {})

	sort.Strings(deleted)
	if !reflect.DeepEqual(deleted, []string{"ghost_a", "ghost_b"}) {
		t.Errorf("deregistered = %v, want [ghost_a ghost_b]", deleted)
	}
	if !reflect.DeepEqual(seen, set("hello")) {
		t.Errorf("seen = %v, want {hello}", seen)
	}
}

// TestReconcileStartup_FetchErrorIsFailSafe: if the registered-DAG list can't be
// fetched (control plane unreachable), reconcile deregisters NOTHING and keeps the
// workspace IDs — never destructive on an unreliable control plane.
func TestReconcileStartup_FetchErrorIsFailSafe(t *testing.T) {
	called := false
	del := func(string) error { called = true; return nil }

	seen := reconcileStartup(nil, errors.New("connection refused"), set("hello"), del, func(string, ...any) {})

	if called {
		t.Error("deleteDag called despite a fetch error — must be fail-safe")
	}
	if !reflect.DeepEqual(seen, set("hello")) {
		t.Errorf("seen = %v, want the workspace fallback {hello}", seen)
	}
}

// TestReconcileStartup_CleanIsNoop: when the control plane already matches the
// workspace, nothing is deregistered.
func TestReconcileStartup_CleanIsNoop(t *testing.T) {
	called := false
	reconcileStartup(set("hello"), nil, set("hello"), func(string) error { called = true; return nil }, func(string, ...any) {})
	if called {
		t.Error("deleteDag called on a clean workspace")
	}
}

// TestFetchRegisteredDagIDs parses the control plane's GET /api/v2/dags into a
// set of dag_ids, and surfaces a non-200 as an error (so reconcile stays
// fail-safe rather than treating an error page as "no DAGs registered").
func TestFetchRegisteredDagIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/dags" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"dags":[{"dag_id":"hello"},{"dag_id":"weather"}],"total_entries":2}`))
	}))
	defer srv.Close()

	got, err := fetchRegisteredDagIDs(srv.URL, "tok")
	if err != nil {
		t.Fatalf("fetchRegisteredDagIDs() error: %v", err)
	}
	if !reflect.DeepEqual(got, set("hello", "weather")) {
		t.Errorf("got %v, want {hello weather}", got)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()
	if _, err := fetchRegisteredDagIDs(bad.URL, "tok"); err == nil {
		t.Error("fetchRegisteredDagIDs() on 500 = nil error, want error (fail-safe)")
	}
}
