package leoflow

import (
	"io/fs"
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
