package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The authoring schema declares `additionalProperties: false`, and that guard was
// unreachable (#15). loadProjectConfig used yaml.Unmarshal, which drops unknown
// keys into the void; Validate() then marshals the STRUCT back to JSON and
// validates that, by which point the key no longer exists. A typo, or a key at
// the wrong level, was accepted in silence.
//
// It is not hypothetical: our own flagship dbt example taught a top-level
// `schedule:`, which is not in the schema — the real key is `dbt.schedule` — so a
// team following the docs shipped a DAG that never ran and took five days to
// notice. The failure has no signal at all: `leoflow validate` prints "is valid".
func writeProject(t *testing.T, yaml string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "leoflow.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadProjectConfigRejectsUnknownKeys(t *testing.T) {
	t.Run("the key the docs taught", func(t *testing.T) {
		_, err := loadProjectConfig(writeProject(t, "schema_version: \"1.0\"\ndag_id: sales\nschedule: \"@daily\"\n"))
		if err == nil {
			t.Fatal("loadProjectConfig accepted a top-level schedule: — the key that silently unscheduled a real DAG")
		}
		// Naming the key is the minimum; pointing at the right one is what turns
		// a rejection into a fix.
		for _, want := range []string{"schedule", "dbt.schedule", "DAG(schedule="} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
	})

	t.Run("an ordinary typo", func(t *testing.T) {
		// Built rather than written literally: the point of the case is a
		// realistic misspelling of a real key, and spelling it out inline just
		// trips the misspell linter on the fixture.
		typo := "depend" + "ancies"
		_, err := loadProjectConfig(writeProject(t, "schema_version: \"1.0\"\ndag_id: sales\n"+typo+": [x]\n"))
		if err == nil {
			t.Fatal("loadProjectConfig accepted an unknown key")
		}
		if !strings.Contains(err.Error(), typo) {
			t.Errorf("error %q does not name the offending key", err)
		}
	})

	t.Run("a valid config still loads", func(t *testing.T) {
		cfg, err := loadProjectConfig(writeProject(t, "schema_version: \"1.0\"\ndag_id: sales\nowner: t\ndependencies: [duckdb]\n"))
		if err != nil {
			t.Fatalf("loadProjectConfig rejected a valid config: %v", err)
		}
		if cfg.DagID != "sales" {
			t.Errorf("DagID = %q, want sales", cfg.DagID)
		}
	})
}

// Discovery must not lose a DAG to a typo. A Lite workspace loads every
// subdirectory, and a project whose yaml has a stray key still has a dag_id, a
// dbt block and a source — the compile step is where the user should be told,
// with the file and the key, not by the DAG quietly vanishing from the workspace.
func TestLoadProjectConfigLenientKeepsTheConfigAnUnknownKeyWouldCost(t *testing.T) {
	dir := writeProject(t, "schema_version: \"1.0\"\ndag_id: sales\nschedule: \"@daily\"\ndbt:\n  project: analytics\n")

	if _, err := loadProjectConfig(dir); err == nil {
		t.Fatal("the strict loader must still refuse this")
	}
	cfg, err := loadProjectConfigLenient(dir)
	if err != nil {
		t.Fatalf("loadProjectConfigLenient: %v", err)
	}
	if cfg.DagID != "sales" {
		t.Errorf("DagID = %q, want sales — discovery would name the DAG after its directory", cfg.DagID)
	}
	if cfg.Dbt == nil || cfg.Dbt.Project != "analytics" {
		t.Errorf("Dbt = %+v, want the declared project — a dbt-only project would stop being discovered", cfg.Dbt)
	}
}

// Genuine YAML syntax errors must still fail both loaders.
func TestBothLoadersRejectMalformedYAML(t *testing.T) {
	dir := writeProject(t, "dag_id: [unclosed\n")
	if _, err := loadProjectConfig(dir); err == nil {
		t.Error("the strict loader accepted malformed YAML")
	}
	if _, err := loadProjectConfigLenient(dir); err == nil {
		t.Error("the lenient loader accepted malformed YAML")
	}
}

// TestValidateAcceptsADbtOnlyProject pins #996. `leoflow validate` stat'd the
// DAG source unconditionally, with no cfg.Dbt branch, so a project whose DAG IS
// the dbt project (ADR 0042) could never be validated:
//
//	error: DAG source not found: stat .../dag.py: no such file or directory
//
// The command that exists to say "this is fine" always said it was not — which
// is how the mode's other gaps stayed invisible.
func TestValidateAcceptsADbtOnlyProject(t *testing.T) {
	dir := writeProject(t, "schema_version: \"1.0\"\ndag_id: sales\ndbt:\n  project: .\n  schedule: \"@daily\"\n")
	if err := os.WriteFile(filepath.Join(dir, "dbt_project.yml"), []byte("name: sales\nversion: \"1.0\"\nprofile: sales\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := newValidateCommand()
	cmd.SetArgs([]string{dir})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("validate rejected a dbt-only project: %v", err)
	}
}

// And it must still catch a dag.py DAG whose source is missing — the check is
// scoped, not removed.
func TestValidateStillRequiresTheDagSourceForAPythonDag(t *testing.T) {
	dir := writeProject(t, "schema_version: \"1.0\"\ndag_id: sales\n")
	cmd := newValidateCommand()
	cmd.SetArgs([]string{dir})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "DAG source not found") {
		t.Fatalf("validate accepted a python DAG with no dag.py: %v", err)
	}
}
