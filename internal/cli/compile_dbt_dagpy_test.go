package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// dbtPlusDagPyProject writes a project carrying BOTH a top-level dbt: block and
// a dag.py. Compiling it used to route on cfg.Dbt before the parser was ever
// consulted, so the Python was discarded with no diagnostic (#1001).
func dbtPlusDagPyProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	yaml := `schema_version: "1.0"
dag_id: sales
owner: data-team
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
	dbtProj := "name: sales\nversion: \"1.0\"\nprofile: sales\n"
	if werr := os.WriteFile(filepath.Join(dir, "dbt_project.yml"), []byte(dbtProj), 0o600); werr != nil {
		t.Fatal(werr)
	}
	dagPy := "from airflow import DAG\n" +
		"from airflow.providers.standard.operators.python import PythonOperator\n"
	if werr := os.WriteFile(filepath.Join(dir, "dag.py"), []byte(dagPy), 0o600); werr != nil {
		t.Fatal(werr)
	}
	return dir
}

// TestCompileRefusesDbtBlockWithDagPy pins the refusal. The failure this locks
// is not a crash: without it `compile` SUCCEEDS and writes a dag.json holding
// only the dbt nodes, so the author ships a DAG missing every Python task with
// a green exit code.
func TestCompileRefusesDbtBlockWithDagPy(t *testing.T) {
	dir := dbtPlusDagPyProject(t)
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	out2 := filepath.Join(dir, "dag.json")
	err := runCompile(cmd, dir, compileOptions{output: out2, image: "reg/sales:v1", dagVersion: "v1"})
	if err == nil {
		t.Fatalf("compile accepted a dbt: block alongside dag.py and silently dropped the Python (#1001)\noutput:\n%s", out.String())
	}
	for _, want := range []string{"dbt:", "dag.py", "dbt_groups"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q, so it does not tell the author what to do: %v", want, err)
		}
	}
	// A refusal that still emitted the truncated artifact would be worse than
	// none: a later step could pick it up.
	if _, serr := os.Stat(out2); serr == nil {
		t.Errorf("refused but still wrote %s", out2)
	}
}

// TestValidateRefusesDbtBlockWithDagPy pins the same refusal in `validate`,
// which is the command whose whole job is to answer "is this project sane".
// It reported "is valid" for this shape.
func TestValidateRefusesDbtBlockWithDagPy(t *testing.T) {
	dir := dbtPlusDagPyProject(t)
	cmd := newValidateCommand()
	cmd.SetContext(context.Background())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{dir})

	err := cmd.Execute()
	if err == nil {
		t.Fatalf("validate called a dbt: block alongside dag.py valid (#1001)\noutput:\n%s", out.String())
	}
	// Assert on the message. An earlier draft of this test passed with no
	// implementation at all, because the fixture was missing dbt_project.yml and
	// validate errored on THAT — a green that observed nothing.
	for _, want := range []string{"dbt:", "dag.py"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("validate errored for some other reason, not the #1001 conflict (missing %q): %v", want, err)
		}
	}
}

// TestCompileDbtOnlyStillWorks is the guard on the guard: the refusal keys on
// the dag.py EXISTING, so a genuine dbt-only project must stay compilable.
// Without this a stricter-than-intended check would take the whole mode down,
// which is how #996 happened.
func TestCompileDbtOnlyStillWorks(t *testing.T) {
	dir := dbtPlusDagPyProject(t)
	if err := os.Remove(filepath.Join(dir, "dag.py")); err != nil {
		t.Fatal(err)
	}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := runCompile(cmd, dir, compileOptions{
		output: filepath.Join(dir, "dag.json"), image: "reg/sales:v1", dagVersion: "v1",
	}); err != nil {
		t.Fatalf("dbt-only project no longer compiles: %v\noutput:\n%s", err, out.String())
	}
}
