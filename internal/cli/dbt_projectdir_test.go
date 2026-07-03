package cli

import (
	"path/filepath"
	"testing"
)

// TestDbtProjectDir: for an image build (Pro) the dbt --project-dir stays
// relative — the image bakes the project at that path. For a local/subprocess
// build (Lite) the task runs on the host from a temp workdir, so it must be the
// ABSOLUTE workspace project path or `dbt --project-dir ./transform` fails with
// "Path './transform' does not exist".
func TestDbtProjectDir(t *testing.T) {
	dag := filepath.FromSlash("/ws/sales")
	cases := []struct {
		name, project string
		local         bool
		want          string
	}{
		{"image build keeps relative", "./transform", false, "./transform"},
		{"local build makes absolute", "./transform", true, filepath.Join(dag, "transform")},
		{"empty project stays empty", "", true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := dbtProjectDir(dag, c.project, c.local); got != c.want {
				t.Errorf("dbtProjectDir(%q, %q, %v) = %q, want %q", dag, c.project, c.local, got, c.want)
			}
		})
	}
}
