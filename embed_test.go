package leoflow

import (
	"io/fs"
	"slices"
	"testing"
)

// TestEmbeddedAssetsPresent asserts the three embed accessors return populated
// trees / payloads. A regression here means a binary-only install (no source
// checkout) cannot bootstrap the parser, the Lite Docker compose, or the
// example DAGs — silent failures that surface only at first-run.
func TestEmbeddedAssetsPresent(t *testing.T) {
	t.Run("PythonSources contains the parser package", func(t *testing.T) {
		entries, err := fs.ReadDir(PythonSources(), "parser/leoflow_parser")
		if err != nil {
			t.Fatalf("read parser dir: %v", err)
		}
		if len(entries) == 0 {
			t.Fatal("parser/leoflow_parser is empty — go:embed pattern stale")
		}
	})

	t.Run("PythonSources contains the runtime package", func(t *testing.T) {
		entries, err := fs.ReadDir(PythonSources(), "runtime/python/leoflow_runtime")
		if err != nil {
			t.Fatalf("read runtime dir: %v", err)
		}
		if len(entries) == 0 {
			t.Fatal("runtime/python/leoflow_runtime is empty — go:embed pattern stale")
		}
	})

	// The authoring package a user's dag.py imports at its top (#17). It is a
	// SEPARATE go:embed pattern from leoflow_runtime — `all:runtime/python/
	// leoflow_runtime` cannot match `runtime/python/leoflow` — and missing it is
	// silent twice over: hatchling ships a wheel without a `packages` entry whose
	// directory is absent, with no error, so a binary-only install builds its Lite
	// venv from ~/.leoflow/pysrc and gets a runtime with no `leoflow` in it. Every
	// python task in a hybrid DAG then dies on the DAG's first line, and the venv
	// freshness gate (which probes both packages) can never be satisfied, so every
	// `leoflow dev` boot reinstalls every DAG's venv.
	t.Run("PythonSources contains the leoflow authoring package", func(t *testing.T) {
		entries, err := fs.ReadDir(PythonSources(), "runtime/python/leoflow")
		if err != nil {
			t.Fatalf("read authoring dir: %v", err)
		}
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		if !slices.Contains(names, "__init__.py") {
			t.Fatalf("runtime/python/leoflow has no __init__.py (got %v) — go:embed pattern stale; the `all:` prefix is required because the file starts with _", names)
		}
	})

	t.Run("DevCompose is a non-empty YAML", func(t *testing.T) {
		data := DevCompose()
		if len(data) == 0 {
			t.Fatal("docker-compose.dev.yaml is not embedded")
		}
	})

	t.Run("ExampleDAGs ships at least one DAG project", func(t *testing.T) {
		entries, err := fs.ReadDir(ExampleDAGs(), "examples")
		if err != nil {
			t.Fatalf("read examples dir: %v", err)
		}
		if len(entries) == 0 {
			t.Fatal("examples/ is empty — DAG sample bundle missing")
		}
	})
}
