package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// TestResolveWorkspace_EmptyWorkspaceReturnsNoProjectsButValidRoot asserts that
// pointing lite at an empty workspace doesn't fail — the caller (runDev) is
// expected to scaffold a starter subdir, so ResolveWorkspace must return a
// usable WorkspaceSpec with an empty Projects slice and a defaulted RootCfg.
func TestResolveWorkspace_EmptyWorkspaceReturnsNoProjectsButValidRoot(t *testing.T) {
	ws := t.TempDir()
	got, err := ResolveWorkspace(ws)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	if got.Path == "" {
		t.Error("Path should be set")
	}
	if len(got.Projects) != 0 {
		t.Errorf("Projects: got %d, want 0", len(got.Projects))
	}
	if got.RootCfg == nil {
		t.Fatal("RootCfg: nil, want defaulted config")
	}
	if got.RootCfg.PythonVersion != "3.11" {
		t.Errorf("RootCfg.PythonVersion: got %q, want 3.11", got.RootCfg.PythonVersion)
	}
}

// TestResolveWorkspace_SingleSubdirProject confirms the multi-DAG happy path:
// one subdir with leoflow.yaml + dag.py shows up as the only project, with the
// expected dag_id and resolved config path.
func TestResolveWorkspace_SingleSubdirProject(t *testing.T) {
	ws := t.TempDir()
	dag := filepath.Join(ws, "sales")
	if err := os.MkdirAll(dag, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dag, "leoflow.yaml"), []byte("dag_id: sales\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dag, "dag.py"), []byte("x=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveWorkspace(ws)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	if len(got.Projects) != 1 {
		t.Fatalf("Projects: got %d, want 1", len(got.Projects))
	}
	if got.Projects[0].DagID != "sales" {
		t.Errorf("DagID: got %q, want sales", got.Projects[0].DagID)
	}
	if !got.Projects[0].HasYAML {
		t.Error("HasYAML: false, want true")
	}
}

// TestResolveWorkspace_RootCfgUnionsDependencies guarantees the venv contract:
// when multiple projects declare different pip deps, the synthesized root cfg
// carries the de-duplicated union so the shared dev venv satisfies every DAG.
// This is the load-bearing invariant for subprocess-mode multi-DAG.
func TestResolveWorkspace_RootCfgUnionsDependencies(t *testing.T) {
	ws := t.TempDir()
	for _, p := range []struct {
		dir  string
		deps string
	}{
		{"etl", "  - pandas==2.1.0\n  - requests\n"},
		{"ml", "  - pandas==2.1.0\n  - numpy\n"}, // overlap on pandas
		{"web", "  - requests\n  - fastapi\n"},   // overlap on requests
	} {
		full := filepath.Join(ws, p.dir)
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatal(err)
		}
		yaml := "dag_id: " + p.dir + "\ndependencies:\n" + p.deps
		if err := os.WriteFile(filepath.Join(full, "leoflow.yaml"), []byte(yaml), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(full, "dag.py"), []byte("x=1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	got, err := ResolveWorkspace(ws)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	wantDeps := []string{"fastapi", "numpy", "pandas==2.1.0", "requests"}
	gotDeps := append([]string(nil), got.RootCfg.Dependencies...)
	sort.Strings(gotDeps)
	if !reflect.DeepEqual(gotDeps, wantDeps) {
		t.Errorf("RootCfg.Dependencies (sorted): got %v, want %v", gotDeps, wantDeps)
	}
}

// TestResolveWorkspace_RootCfgPicksHighestPythonVersion asserts the pragmatic
// venv-shared rule: when projects disagree on python_version, the workspace
// venv targets the highest declared version (newest features available; older
// projects still work — Python is backwards-compatible inside a major). This
// matches docs/configuration.md and avoids the "your workspace shipped a 3.10
// venv but my new DAG needs 3.12" trap.
func TestResolveWorkspace_RootCfgPicksHighestPythonVersion(t *testing.T) {
	ws := t.TempDir()
	for _, p := range []struct {
		dir, py string
	}{
		{"a", "3.10"},
		{"b", "3.12"}, // highest
		{"c", "3.11"},
	} {
		full := filepath.Join(ws, p.dir)
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatal(err)
		}
		yaml := "dag_id: " + p.dir + "\npython_version: \"" + p.py + "\"\n"
		if err := os.WriteFile(filepath.Join(full, "leoflow.yaml"), []byte(yaml), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(full, "dag.py"), []byte("x=1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	got, err := ResolveWorkspace(ws)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	if got.RootCfg.PythonVersion != "3.12" {
		t.Errorf("RootCfg.PythonVersion: got %q, want 3.12 (highest)", got.RootCfg.PythonVersion)
	}
}

// TestResolveWorkspace_PropagatesDiscoveryErrors confirms that a discovery
// error (e.g. duplicate dag_id) surfaces unchanged. ResolveWorkspace should not
// swallow it — the caller needs the path list in the message to fix the
// collision.
func TestResolveWorkspace_PropagatesDiscoveryErrors(t *testing.T) {
	ws := t.TempDir()
	for _, name := range []string{"foo", "bar"} {
		full := filepath.Join(ws, name)
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(full, "leoflow.yaml"), []byte("dag_id: shared\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(full, "dag.py"), []byte("x=1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, err := ResolveWorkspace(ws)
	if err == nil {
		t.Fatal("expected duplicate-dag_id error, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error %q should contain 'duplicate'", err.Error())
	}
}

// TestResolveWorkspace_WatchedPathsCoverEveryProject is the contract the lite
// watcher relies on: every project's leoflow.yaml AND dag.py must appear in
// WatchedPaths so a save in any of them triggers a reload. Without a yaml the
// project still contributes its dag.py.
func TestResolveWorkspace_WatchedPathsCoverEveryProject(t *testing.T) {
	ws := t.TempDir()
	// one project with yaml
	withYaml := filepath.Join(ws, "with_yaml")
	_ = os.MkdirAll(withYaml, 0o755)
	_ = os.WriteFile(filepath.Join(withYaml, "leoflow.yaml"), []byte("dag_id: with_yaml\n"), 0o600)
	_ = os.WriteFile(filepath.Join(withYaml, "dag.py"), []byte("x=1\n"), 0o600)
	// one project without yaml
	withoutYaml := filepath.Join(ws, "without_yaml")
	_ = os.MkdirAll(withoutYaml, 0o755)
	_ = os.WriteFile(filepath.Join(withoutYaml, "dag.py"), []byte("x=1\n"), 0o600)

	got, err := ResolveWorkspace(ws)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	want := map[string]bool{
		filepath.Join(withYaml, "leoflow.yaml"): true,
		filepath.Join(withYaml, "dag.py"):       true,
		filepath.Join(withoutYaml, "dag.py"):    true,
	}
	paths := got.WatchedPaths()
	for _, p := range paths {
		if !want[p] {
			t.Errorf("unexpected watched path %q", p)
		}
		delete(want, p)
	}
	for missing := range want {
		t.Errorf("missing watched path %q", missing)
	}
}
