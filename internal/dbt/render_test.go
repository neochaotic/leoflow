package dbt

import (
	"os"
	"reflect"
	"testing"

	"github.com/neochaotic/leoflow/internal/domain"
)

// TestRenderNodeGranularity feeds a real (trimmed) dbt manifest.json and asserts
// the renderer emits one flat bash task per executable node, with the scoped dbt
// command and the dependency edges carried over from depends_on.nodes.
func TestRenderNodeGranularity(t *testing.T) {
	manifest, err := os.ReadFile("testdata/manifest_chain.json")
	if err != nil {
		t.Fatalf("reading testdata: %v", err)
	}

	tasks, err := Render(manifest)
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
