package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
dbt:
  project: .
  manifest: manifest.json
  granularity: folder
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
	if len(spec.Tasks) != 3 { // folder grouping: seeds, staging, marts
		t.Fatalf("got %d tasks, want 3: %+v", len(spec.Tasks), spec.Tasks)
	}
	for _, ts := range spec.Tasks {
		if ts.Type != domain.TaskTypeBash {
			t.Errorf("task %q type = %q, want bash", ts.TaskID, ts.Type)
		}
	}
}
