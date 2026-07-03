package executor

import (
	"os"
	"testing"
)

// TestPrependVenvBin: a per-DAG venv's bin dir is put ahead of PATH so
// venv-installed console scripts (e.g. `dbt`) resolve for bash tasks in Lite
// subprocess — not just `python -m ...` for Python tasks. Without it a dbt model
// task runs bare `dbt` against the system PATH and fails with exit 127.
func TestPrependVenvBin(t *testing.T) {
	sep := string(os.PathListSeparator)
	cases := []struct {
		name, perDagPy, basePATH, want string
	}{
		{"no venv keeps PATH", "", "/usr/bin" + sep + "/bin", "/usr/bin" + sep + "/bin"},
		{"venv bin prepended", "/v/venvs/sales/bin/python", "/usr/bin", "/v/venvs/sales/bin" + sep + "/usr/bin"},
		{"empty base yields bin", "/v/venvs/sales/bin/python", "", "/v/venvs/sales/bin"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := prependVenvBin(c.perDagPy, c.basePATH); got != c.want {
				t.Errorf("prependVenvBin(%q, %q) = %q, want %q", c.perDagPy, c.basePATH, got, c.want)
			}
		})
	}
}
