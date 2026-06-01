package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMaterializeWorkDir_WritesSourceToTempDir is the load-bearing test for the
// Lima subdir-DAG fix: the SubprocessExecutor must place the dag.json's source
// string into a dag.py file inside a per-TI temp dir, so the spawned agent's
// `python -m leoflow_runtime dag:<task>` can importlib it.
//
// Without this, multi-DAG Lite setups (the user pasted source as a string but
// has no on-disk dag.py at the workspace root) fail with ModuleNotFoundError —
// confirmed on Lima 2026-06-01.
func TestMaterializeWorkDir_WritesSourceToTempDir(t *testing.T) {
	source := "from airflow.sdk import DAG, task\n@task\ndef hello():\n    return 1\n"

	dir, cleanup, err := materializeWorkDir(source, "ti-abc-123")
	if err != nil {
		t.Fatalf("materializeWorkDir: %v", err)
	}
	t.Cleanup(cleanup)

	if dir == "" {
		t.Fatal("expected a non-empty workdir for non-empty source")
	}
	if !strings.Contains(filepath.Base(dir), "ti-abc-123") {
		t.Errorf("temp dir name %q should embed the task_instance_id", dir)
	}
	got, err := os.ReadFile(filepath.Join(dir, "dag.py"))
	if err != nil {
		t.Fatalf("reading materialized dag.py: %v", err)
	}
	if string(got) != source {
		t.Errorf("materialized dag.py content mismatch:\n got: %q\nwant: %q", got, source)
	}
}

// TestMaterializeWorkDir_EmptySourceIsNoop is the Pro-path compatibility test:
// in Kubernetes mode the source lives inside the container image, not in the
// dag.json, so req.Source is empty. materializeWorkDir MUST then return a
// nil-effect cleanup and an empty dir so the executor falls back to its
// configured global workdir (unchanged for Pro).
func TestMaterializeWorkDir_EmptySourceIsNoop(t *testing.T) {
	dir, cleanup, err := materializeWorkDir("", "ti-xyz")
	if err != nil {
		t.Fatalf("empty source must not error: %v", err)
	}
	if dir != "" {
		t.Errorf("empty source must return empty workdir, got %q", dir)
	}
	cleanup()
}

// TestMaterializeWorkDir_CleanupRemovesDir guarantees the per-TI temp dir does
// not leak across tasks. The executor stages a `defer cleanup()` after Wait()
// completes; this test confirms the cleanup actually does what it says.
func TestMaterializeWorkDir_CleanupRemovesDir(t *testing.T) {
	dir, cleanup, err := materializeWorkDir("x = 1\n", "ti-leak")
	if err != nil {
		t.Fatalf("materializeWorkDir: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("dir should exist before cleanup: %v", err)
	}
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("dir should be removed after cleanup, stat err = %v", err)
	}
}
