package dbt

import (
	"encoding/json"
	"strings"
	"testing"
)

// Compile wraps the rendered tasks into a complete, valid dag.json DAGSpec,
// carrying the metadata (dag_id, image, version, schedule) the renderer does not
// know and honoring the requested granularity.
func TestCompileProducesDAGSpec(t *testing.T) {
	spec, err := Compile(loadManifest(t, "manifest_wide.json"), Meta{
		DagID:       "sales",
		DagVersion:  "v1",
		Image:       "reg.example.com/sales@sha256:abc",
		Schedule:    "@daily",
		Granularity: GranularityFolder,
	})
	if err != nil {
		t.Fatalf("Compile() error: %v", err)
	}
	if spec.SchemaVersion != "1.0" {
		t.Errorf("schema_version = %q, want 1.0", spec.SchemaVersion)
	}
	if spec.DagID != "sales" {
		t.Errorf("dag_id = %q, want sales", spec.DagID)
	}
	if spec.DagVersion != "v1" {
		t.Errorf("dag_version = %q, want v1", spec.DagVersion)
	}
	if spec.Image != "reg.example.com/sales@sha256:abc" {
		t.Errorf("image = %q", spec.Image)
	}
	if spec.Schedule == nil || *spec.Schedule != "@daily" {
		t.Errorf("schedule = %v, want @daily", spec.Schedule)
	}
	if len(spec.Tasks) != 3 { // folder grouping: seeds, staging, marts
		t.Fatalf("got %d tasks, want 3", len(spec.Tasks))
	}
	// the spec must marshal to a dag.json carrying the required keys
	b, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"schema_version"`, `"dag_id"`, `"image"`, `"tasks"`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("dag.json missing %s", key)
		}
	}
}

// An empty schedule means an unscheduled DAG (omitted, not "").
func TestCompileNoScheduleIsNil(t *testing.T) {
	spec, err := Compile(loadManifest(t, "manifest_chain.json"), Meta{DagID: "x", Image: "img"})
	if err != nil {
		t.Fatalf("Compile() error: %v", err)
	}
	if spec.Schedule != nil {
		t.Errorf("schedule = %v, want nil", spec.Schedule)
	}
}

// TestCompileLocalEmitsDuckdbPrefix locks the Lite wiring: Meta.Local threads into
// the render options, so a top-level dbt project built for Lite (no managed
// connection) gets the zero-config duckdb profile step — the same behavior the
// dbt_group path already has. Without it, a Lite build emits a bare `dbt run` with
// no profiles.yml and silently assumes the user supplied one (#575).
func TestCompileLocalEmitsDuckdbPrefix(t *testing.T) {
	spec, err := Compile(loadManifest(t, "manifest_chain.json"), Meta{
		DagID: "shop", Image: "img", Profile: "transform", Local: true,
	})
	if err != nil {
		t.Fatalf("Compile() error: %v", err)
	}
	for _, task := range spec.Tasks {
		if !strings.Contains(task.Entrypoint, "--dbt-default-duckdb transform") {
			t.Errorf("Lite task must inject the duckdb profile step; got %q", task.Entrypoint)
		}
	}
}

// TestCompileNonLocalNoDuckdbPrefix: the Pro/image path (Local=false) must NOT
// inject the duckdb step — the baked profiles.yml is used instead.
func TestCompileNonLocalNoDuckdbPrefix(t *testing.T) {
	spec, err := Compile(loadManifest(t, "manifest_chain.json"), Meta{
		DagID: "shop", Image: "img", Profile: "transform", Local: false,
	})
	if err != nil {
		t.Fatalf("Compile() error: %v", err)
	}
	for _, task := range spec.Tasks {
		if strings.Contains(task.Entrypoint, "--dbt-default-duckdb") {
			t.Errorf("Pro task must not inject the duckdb step; got %q", task.Entrypoint)
		}
	}
}

// dag_id is required — a dbt DAG with no id is a loud error, not a silent default.
func TestCompileRequiresDagID(t *testing.T) {
	if _, err := Compile(loadManifest(t, "manifest_chain.json"), Meta{Image: "img"}); err == nil {
		t.Fatal("expected an error for an empty dag_id, got nil")
	}
}
