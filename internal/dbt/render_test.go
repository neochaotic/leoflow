package dbt

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/neochaotic/leoflow/internal/domain"
)

// Ephemeral models are inlined by dbt (no table), so they must not become tasks;
// a downstream task is re-parented through them onto their executable ancestors
// (chain raw -> stg[ephemeral] -> mart yields tasks {raw, mart} with mart<-raw).
func TestRenderSkipsEphemeralWithReparenting(t *testing.T) {
	tasks, err := Render(loadManifest(t, "manifest_ephemeral.json"), Options{})
	if err != nil {
		t.Fatalf("Render() error: %v", err)
	}
	byID := tasksByID(tasks)
	if _, ok := byID["stg"]; ok {
		t.Error("ephemeral model stg must not become a task")
	}
	if len(tasks) != 2 {
		t.Fatalf("got %d tasks, want 2 (raw, mart): %v", len(tasks), ids(byID))
	}
	mart, ok := byID["mart"]
	if !ok {
		t.Fatal("mart task missing")
	}
	if !reflect.DeepEqual(mart.DependsOn, []string{"raw"}) {
		t.Errorf("mart depends_on = %v, want [raw] (re-parented through ephemeral stg)", mart.DependsOn)
	}
}

// Two dbt nodes that share a name (e.g. the same model name across installed
// packages) would silently produce duplicate task_ids. That is rejected loudly.
func TestRenderRejectsDuplicateName(t *testing.T) {
	_, err := Render(loadManifest(t, "manifest_collision.json"), Options{})
	if err == nil {
		t.Fatal("expected duplicate-name rejection, got nil")
	}
	if !strings.Contains(err.Error(), "stg_orders") {
		t.Errorf("error %q should name the colliding node", err)
	}
}

// A manifest with no executable nodes is a loud error, not a silent empty DAG.
func TestRenderEmptyManifestErrors(t *testing.T) {
	for _, in := range []string{`{}`, `{"nodes":{}}`} {
		if _, err := Render([]byte(in), Options{}); err == nil {
			t.Errorf("Render(%s) = nil error, want an error for no executable nodes", in)
		}
	}
}

// With a managed connection, each task's dbt command is prefixed with the runtime
// step that generates profiles.yml from the connection (ADR 0043 #2).
func TestRenderWrapsManagedConnection(t *testing.T) {
	tasks, err := Render(loadManifest(t, "manifest_chain.json"), Options{
		Granularity: GranularityNode,
		Connection:  "warehouse_pg",
		Profile:     "analytics",
	})
	if err != nil {
		t.Fatalf("Render() error: %v", err)
	}
	got := tasksByID(tasks)["stg"].Entrypoint
	want := "python -m leoflow_runtime --dbt-profile warehouse_pg analytics && dbt run --select stg"
	if got != want {
		t.Errorf("entrypoint = %q, want %q", got, want)
	}
}

// TestRenderNodeGranularity feeds a real (trimmed) dbt manifest.json and asserts
// the renderer emits one flat bash task per executable node, with the scoped dbt
// command and the dependency edges carried over from depends_on.nodes.
func TestRenderNodeGranularity(t *testing.T) {
	manifest, err := os.ReadFile("testdata/manifest_chain.json")
	if err != nil {
		t.Fatalf("reading testdata: %v", err)
	}

	tasks, err := Render(manifest, Options{Granularity: GranularityNode})
	if err != nil {
		t.Fatalf("Render() error: %v", err)
	}
	if len(tasks) != 4 {
		t.Fatalf("Render() returned %d tasks, want 4", len(tasks))
	}

	byID := make(map[string]domain.TaskSpec, len(tasks))
	for _, ts := range tasks {
		byID[ts.TaskID] = ts
	}

	want := map[string]struct {
		entrypoint string
		deps       []string
	}{
		"raw":            {"dbt seed --select raw", nil},
		"stg":            {"dbt run --select stg", []string{"raw"}},
		"mart":           {"dbt run --select mart", []string{"stg"}},
		"unique_mart_id": {"dbt test --select unique_mart_id", []string{"mart"}},
	}
	for id, w := range want {
		got, ok := byID[id]
		if !ok {
			t.Errorf("task %q missing from render output", id)
			continue
		}
		if got.Type != domain.TaskTypeBash {
			t.Errorf("task %q type = %q, want %q", id, got.Type, domain.TaskTypeBash)
		}
		if got.Entrypoint != w.entrypoint {
			t.Errorf("task %q entrypoint = %q, want %q", id, got.Entrypoint, w.entrypoint)
		}
		if !reflect.DeepEqual(got.DependsOn, w.deps) {
			t.Errorf("task %q depends_on = %v, want %v", id, got.DependsOn, w.deps)
		}
	}
}
