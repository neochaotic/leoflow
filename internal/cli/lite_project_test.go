package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

func TestScaffoldProject(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "etl")
	dagID, err := scaffoldProject(dir)
	if err != nil {
		t.Fatalf("scaffoldProject: %v", err)
	}
	if dagID != "etl" {
		t.Errorf("dagID = %q, want etl (the dir base)", dagID)
	}
	for _, f := range []string{"leoflow.yaml", "dag.py"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("scaffold missing %s: %v", f, err)
		}
	}
}

func bareCmd() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetOut(&nopWriter{})
	cmd.PersistentFlags().String("config", "", "")
	return cmd
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestResolveLiteProjectExplicitArg(t *testing.T) {
	got, err := resolveLiteProject(bareCmd(), []string{"/some/dag"})
	if err != nil || got != "/some/dag" {
		t.Errorf("explicit arg = (%q,%v), want /some/dag", got, err)
	}
}

func TestResolveLiteProjectNoArgScaffoldsWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home) // no config -> defaultWorkspace falls back to $HOME/leoflow
	got, err := resolveLiteProject(bareCmd(), nil)
	if err != nil {
		t.Fatalf("no-arg resolve: %v", err)
	}
	want := filepath.Join(home, "leoflow")
	if got != want {
		t.Errorf("no-arg dir = %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(want, "leoflow.yaml")); err != nil {
		t.Errorf("no-arg run should scaffold a starter project: %v", err)
	}
}

func TestResolveLiteProjectNoArgUsesExistingProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ws := filepath.Join(home, "leoflow")
	if err := os.MkdirAll(ws, 0o750); err != nil {
		t.Fatal(err)
	}
	// An existing project must be used as-is, not overwritten.
	marker := "dag_id: existing\n"
	if err := os.WriteFile(filepath.Join(ws, "leoflow.yaml"), []byte(marker), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := resolveLiteProject(bareCmd(), nil)
	if err != nil || got != ws {
		t.Fatalf("resolve = (%q,%v), want %q", got, err, ws)
	}
	data, _ := os.ReadFile(filepath.Join(ws, "leoflow.yaml"))
	if string(data) != marker {
		t.Errorf("existing project must not be overwritten, got %q", data)
	}
}

func TestDevBasePythonPrefersManaged(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "dev") // home = ~/.leoflow/dev ; managed = ~/.leoflow/python/...
	managed := filepath.Join(root, "python", "bin", "python3.11")
	if err := os.MkdirAll(filepath.Dir(managed), 0o750); err != nil {
		t.Fatal(err)
	}
	// Before the managed CPython exists, it falls back to a PATH python.
	if got := devBasePython(home); got == managed {
		t.Errorf("should not return the managed path before it exists")
	}
	// Once present, the managed interpreter (which bundles venv) is preferred.
	if err := os.WriteFile(managed, []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
	if got := devBasePython(home); got != managed {
		t.Errorf("devBasePython = %q, want the managed %q", got, managed)
	}
}

func TestResolveRuntimeSrc(t *testing.T) {
	// Explicit override wins.
	if got := resolveRuntimeSrc("/my/runtime", "/h/.leoflow/dev"); got != "/my/runtime" {
		t.Errorf("explicit = %q", got)
	}
	// No repo in cwd, default flag -> the extracted pysrc path under the leoflow home.
	t.Chdir(t.TempDir()) // a dir with no runtime/python
	home := "/h/.leoflow/dev"
	want := "/h/.leoflow/pysrc/runtime/python"
	if got := resolveRuntimeSrc("runtime/python", home); got != want {
		t.Errorf("fallback = %q, want %q", got, want)
	}
	if got := resolveRuntimeSrc("", home); got != want {
		t.Errorf("empty flag fallback = %q, want %q", got, want)
	}
}
