package setup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func TestExtractFS(t *testing.T) {
	src := fstest.MapFS{
		"parser/pyproject.toml":             {Data: []byte("[project]\nname='leoflow-parser'\n")},
		"parser/leoflow_parser/__init__.py": {Data: []byte("")},
		"parser/leoflow_parser/cli.py":      {Data: []byte("print('hi')\n")},
		"runtime/python/pyproject.toml":     {Data: []byte("[project]\nname='leoflow-runtime'\n")},
	}
	dest := t.TempDir()
	if err := ExtractFS(src, dest); err != nil {
		t.Fatalf("ExtractFS err = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "parser", "leoflow_parser", "cli.py"))
	if err != nil {
		t.Fatalf("reading extracted file: %v", err)
	}
	if string(got) != "print('hi')\n" {
		t.Errorf("cli.py = %q, want the source", got)
	}
	if _, err := os.Stat(filepath.Join(dest, "runtime", "python", "pyproject.toml")); err != nil {
		t.Errorf("runtime pyproject not extracted: %v", err)
	}
}

func TestProvisionVenv(t *testing.T) {
	t.Run("creates venv then pip installs the packages", func(t *testing.T) {
		var calls [][]string
		run := func(_ context.Context, name string, args ...string) error {
			calls = append(calls, append([]string{name}, args...))
			return nil
		}
		pythonPath := filepath.Join(string(filepath.Separator)+"managed", "python")
		venvDir := t.TempDir()
		parserSrc := filepath.Join(venvDir, "..", "pysrc", "parser")
		venvPy, err := ProvisionVenv(context.Background(), run,
			pythonPath, venvDir,
			[]string{parserSrc, "apache-airflow-task-sdk==1.2.1"})
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if venvPy != filepath.Join(venvDir, "bin", "python") {
			t.Errorf("venvPy = %q, unexpected", venvPy)
		}
		if len(calls) != 2 {
			t.Fatalf("got %d calls, want 2 (venv create + pip install)", len(calls))
		}
		if calls[0][0] != "/managed/python" || calls[0][1] != "-m" || calls[0][2] != "venv" {
			t.Errorf("first call = %v, want python -m venv ...", calls[0])
		}
		// pip install must use the venv's python and include both packages.
		joined := calls[1]
		if joined[0] != venvPy {
			t.Errorf("pip call used %q, want venv python %q", joined[0], venvPy)
		}
		var sawParser, sawSDK bool
		for _, a := range joined {
			if a == parserSrc {
				sawParser = true
			}
			if a == "apache-airflow-task-sdk==1.2.1" {
				sawSDK = true
			}
		}
		if !sawParser || !sawSDK {
			t.Errorf("pip call %v missing parser src or task-sdk", joined)
		}
	})

	t.Run("propagates a venv-creation failure", func(t *testing.T) {
		run := func(_ context.Context, _ string, _ ...string) error { return errors.New("boom") }
		_, err := ProvisionVenv(context.Background(), run, "/p", "/v", []string{"pkg"})
		if err == nil {
			t.Fatal("err = nil, want venv-creation failure")
		}
	})
}
