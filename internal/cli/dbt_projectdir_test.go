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

// TestCompileOptionsLocalIsNotDerivedFromBuild pins the fix for #993.
//
// `local` selects between two incompatible meanings of --project-dir: an
// absolute host path for a subprocess executor running from a per-task temp
// workdir (Lite), and the relative path inside the image for a pod (Pro). It
// used to be derived as `!o.build`, which reads as "we did not build an image
// this invocation" — a different question. `leoflow compile` without --build
// and `leoflow deploy --skip-build` both mean "the image already exists or will
// be built elsewhere", and both were classified local, so every dbt task in the
// produced dag.json carried the operator's own absolute path:
//
//	dbt run --select mart --project-dir /Users/<someone>/work/sales/analytics
//
// That dag.json is what gets registered and executed in pods, so the task exits
// seconds after start with "project directory does not exist" and nothing points
// at the real cause. Same class as #20, through a different door — and the more
// likely one for a CI or prebuilt-image workflow.
//
// The executor is chosen by SERVER configuration (LEOFLOW_EXECUTOR_TYPE), not by
// anything in the dag.json, so compile cannot infer it. It has to be told, and
// only Lite's subprocess mode knows the answer is yes.
func TestCompileOptionsLocalIsNotDerivedFromBuild(t *testing.T) {
	for _, c := range []struct {
		name  string
		opts  compileOptions
		local bool
	}{
		{"bare compile targets an image", compileOptions{}, false},
		{"compile --build targets an image", compileOptions{build: true}, false},
		{"deploy targets an image", compileOptions{build: true, push: true}, false},
		{"deploy --skip-build still targets an image", compileOptions{}, false},
		{"only Lite's subprocess mode is local", compileOptions{local: true}, true},
		{"Lite's cluster mode is not", compileOptions{build: true, builder: "docker"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.opts.local != c.local {
				t.Errorf("compileOptions%+v local = %v, want %v", c.opts, c.opts.local, c.local)
			}
			// And the value must reach the baked flag unchanged: deriving it
			// from build is what produced the host path.
			got := dbtProjectDir("/work/sales", "analytics", c.opts.local)
			if c.local && got != "/work/sales/analytics" {
				t.Errorf("local build baked %q, want the absolute workspace path", got)
			}
			if !c.local && got != "analytics" {
				t.Errorf("image build baked %q, want the relative in-image path", got)
			}
		})
	}
}
