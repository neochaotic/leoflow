package executor

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestResolveLitePythonForDagPrefersPerDagVenv pins the Lite per-DAG Python
// resolution contract: the subprocess executor consults
// LEOFLOW_LITE_VENVS_ROOT/<dag_id>/bin/python for each Request and, when that
// file exists, exports it as LEOFLOW_PYTHON so the agent runs the DAG's own
// venv. Without an override, it returns "" so the boot-fallback
// (server-inherited LEOFLOW_PYTHON) keeps applying.
//
// The bug class this protects against: a one-character mismatch between the
// CLI's venv layout and the executor's lookup would have every task run
// against the boot fallback (the WRONG venv), and the only symptom would be
// ModuleNotFoundError on a dep that was successfully installed — exactly the
// class of failure issue #346 traces to.
func TestResolveLitePythonForDagPrefersPerDagVenv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Lite per-DAG venv path differs on Windows; tested via TestDagVenvPathLayout in internal/cli")
	}
	root := t.TempDir()
	dagID := "sales_etl"
	pyDir := filepath.Join(root, dagID, "bin")
	if err := os.MkdirAll(pyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	py := filepath.Join(pyDir, "python")
	if err := os.WriteFile(py, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if got := resolveLitePythonForDag(root, dagID); got != py {
		t.Errorf("override for %q = %q, want %q", dagID, got, py)
	}
	if got := resolveLitePythonForDag(root, "unknown_dag"); got != "" {
		t.Errorf("override for unknown dag = %q, want \"\" (fallback to inherited LEOFLOW_PYTHON)", got)
	}
	if got := resolveLitePythonForDag("", dagID); got != "" {
		t.Errorf("empty venvs root must skip resolution, got %q", got)
	}
	// Empty DagID happens for old agents / tests where the executor is built
	// without a routing target; it must NOT collapse to <root>/bin/python.
	if got := resolveLitePythonForDag(root, ""); got != "" {
		t.Errorf("empty dag_id must skip resolution, got %q", got)
	}
}
