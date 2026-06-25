package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/neochaotic/leoflow/internal/domain"
)

// TestCompileDbtProject drives `leoflow compile` on a dbt project: a leoflow.yaml
// with a dbt block and a pre-baked manifest.json (no dbt binary needed) must
// produce a valid dag.json with one task per folder group.
func TestCompileDbtProject(t *testing.T) {
	dir := t.TempDir()
	yaml := `schema_version: "1.0"
dag_id: sales
owner: data-team
tags: [analytics, dbt]
dbt:
  project: .
  manifest: manifest.json
  granularity: folder
  schedule: "@daily"
`
	if err := os.WriteFile(filepath.Join(dir, "leoflow.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join("..", "dbt", "testdata", "manifest_wide.json"))
	if err != nil {
		t.Fatalf("reading fixture manifest: %v", err)
	}
	if werr := os.WriteFile(filepath.Join(dir, "manifest.json"), manifest, 0o600); werr != nil {
		t.Fatal(werr)
	}

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	o := compileOptions{output: filepath.Join(dir, "dag.json"), image: "reg/sales:v1", dagVersion: "v1"}
	if rerr := runCompile(cmd, dir, o); rerr != nil {
		t.Fatalf("runCompile (dbt path): %v\noutput:\n%s", rerr, out.String())
	}

	data, err := os.ReadFile(filepath.Join(dir, "dag.json"))
	if err != nil {
		t.Fatalf("dag.json not produced: %v", err)
	}
	var spec domain.DAGSpec
	if uerr := json.Unmarshal(data, &spec); uerr != nil {
		t.Fatalf("dag.json invalid JSON: %v", uerr)
	}
	if spec.DagID != "sales" {
		t.Errorf("dag_id = %q, want sales", spec.DagID)
	}
	if spec.Schedule == nil || *spec.Schedule != "@daily" {
		t.Errorf("schedule = %v, want @daily", spec.Schedule)
	}
	if spec.Owner != "data-team" {
		t.Errorf("owner = %q, want data-team (leoflow.yaml owner must overlay onto the dbt DAG)", spec.Owner)
	}
	if !reflect.DeepEqual(spec.Tags, []string{"analytics", "dbt"}) {
		t.Errorf("tags = %v, want [analytics dbt]", spec.Tags)
	}
	if len(spec.Tasks) != 3 { // folder grouping: seeds, staging, marts
		t.Fatalf("got %d tasks, want 3: %+v", len(spec.Tasks), spec.Tasks)
	}
	for _, ts := range spec.Tasks {
		if ts.Type != domain.TaskTypeBash {
			t.Errorf("task %q type = %q, want bash", ts.TaskID, ts.Type)
		}
	}
}

// With a managed connection, the expanded dbt tasks' commands are wrapped with the
// runtime profile-generation step, using the profile name read from dbt_project.yml.
func TestExpandDbtGroupsWithManagedConnection(t *testing.T) {
	dir := t.TempDir()
	dagJSON := `{"schema_version":"1.0","dag_id":"d","dag_version":"v1","image":"img","tasks":[
		{"task_id":"g","type":"dbt_group"}
	]}`
	out := filepath.Join(dir, "dag.json")
	if err := os.WriteFile(out, []byte(dagJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if mkErr := os.MkdirAll(filepath.Join(dir, "proj"), 0o750); mkErr != nil {
		t.Fatal(mkErr)
	}
	if werr := os.WriteFile(filepath.Join(dir, "proj", "dbt_project.yml"),
		[]byte("name: proj\nprofile: wh_profile\n"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	manifest, err := os.ReadFile(filepath.Join("..", "dbt", "testdata", "manifest_chain.json"))
	if err != nil {
		t.Fatal(err)
	}
	if werr := os.WriteFile(filepath.Join(dir, "proj", "manifest.json"), manifest, 0o600); werr != nil {
		t.Fatal(werr)
	}
	cfg := &domain.LeoflowConfig{
		DagID: "d",
		DbtGroups: map[string]*domain.DbtConfig{
			"g": {Project: "proj", Manifest: "manifest.json", Granularity: "node", Connection: "warehouse_pg"},
		},
	}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	if eerr := expandDbtGroupsInFile(cmd, dir, out, cfg); eerr != nil {
		t.Fatalf("expandDbtGroupsInFile: %v", eerr)
	}
	data, _ := os.ReadFile(out)
	var spec domain.DAGSpec
	if uerr := json.Unmarshal(data, &spec); uerr != nil {
		t.Fatal(uerr)
	}
	want := "python -m leoflow_runtime --dbt-profile warehouse_pg wh_profile && "
	for _, ts := range spec.Tasks {
		if !strings.HasPrefix(ts.Entrypoint, want) {
			t.Errorf("task %q entrypoint = %q, want prefix %q", ts.TaskID, ts.Entrypoint, want)
		}
	}
}

// TestExpandDbtGroupsInFile drives the merge step on the dag.json the parser would
// produce for a mixed DAG (operator + dbt_group placeholder): the placeholder is
// replaced by the namespaced dbt tasks and the downstream is rewired to the leaves.
func TestExpandDbtGroupsInFile(t *testing.T) {
	dir := t.TempDir()
	dagJSON := `{"schema_version":"1.0","dag_id":"sales","dag_version":"v1","image":"img","tasks":[
		{"task_id":"extract","type":"python","entrypoint":"d:extract"},
		{"task_id":"analytics","type":"dbt_group","depends_on":["extract"]},
		{"task_id":"notify","type":"python","entrypoint":"d:notify","depends_on":["analytics"]}
	]}`
	out := filepath.Join(dir, "dag.json")
	if err := os.WriteFile(out, []byte(dagJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join("..", "dbt", "testdata", "manifest_wide.json"))
	if err != nil {
		t.Fatal(err)
	}
	// the manifest lives inside the project dir (manifest path is project-relative)
	if mkErr := os.MkdirAll(filepath.Join(dir, "analytics"), 0o750); mkErr != nil {
		t.Fatal(mkErr)
	}
	if werr := os.WriteFile(filepath.Join(dir, "analytics", "manifest.json"), manifest, 0o600); werr != nil {
		t.Fatal(werr)
	}
	cfg := &domain.LeoflowConfig{
		DagID: "sales",
		DbtGroups: map[string]*domain.DbtConfig{
			"analytics": {Project: "analytics", Manifest: "manifest.json", Granularity: "node"},
		},
	}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	if eerr := expandDbtGroupsInFile(cmd, dir, out, cfg); eerr != nil {
		t.Fatalf("expandDbtGroupsInFile: %v", eerr)
	}
	data, rerr := os.ReadFile(out)
	if rerr != nil {
		t.Fatal(rerr)
	}
	var spec domain.DAGSpec
	if uerr := json.Unmarshal(data, &spec); uerr != nil {
		t.Fatal(uerr)
	}
	byID := map[string]domain.TaskSpec{}
	for _, ts := range spec.Tasks {
		byID[ts.TaskID] = ts
	}
	if _, ok := byID["analytics"]; ok {
		t.Error("the dbt_group placeholder should be gone after expansion")
	}
	if got, ok := byID["analytics__raw"]; !ok {
		t.Error("missing namespaced task analytics__raw")
	} else if !reflect.DeepEqual(got.DependsOn, []string{"extract"}) {
		t.Errorf("group root deps = %v, want [extract]", got.DependsOn)
	}
	if !reflect.DeepEqual(byID["notify"].DependsOn, []string{"analytics__mart_a", "analytics__mart_b"}) {
		t.Errorf("notify deps = %v, want the two group leaves", byID["notify"].DependsOn)
	}
}
