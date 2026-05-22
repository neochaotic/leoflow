package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neochaotic/leoflow/internal/domain"
	yaml "go.yaml.in/yaml/v3"
)

func run(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCommand()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errb.String(), err
}

func TestVersionCommandPrintsInfo(t *testing.T) {
	out, _, err := run(t, "version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.Contains(out, "leoflow") || !strings.Contains(out, "dev") {
		t.Errorf("version output = %q, want to contain leoflow and dev", out)
	}
}

func TestVersionCommandJSON(t *testing.T) {
	out, _, err := run(t, "version", "--json")
	if err != nil {
		t.Fatalf("version --json: %v", err)
	}
	var info map[string]any
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		t.Fatalf("version --json is not valid JSON: %v (%q)", err, out)
	}
	if info["version"] != "dev" {
		t.Errorf("json version = %v, want dev", info["version"])
	}
}

func TestInitCreatesValidProject(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my-dag")
	if _, _, err := run(t, "init", dir); err != nil {
		t.Fatalf("init: %v", err)
	}
	for _, f := range []string{"leoflow.yaml", "dag.py"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("expected scaffolded %s: %v", f, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "leoflow.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg domain.LeoflowConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("scaffolded leoflow.yaml is invalid: %v", err)
	}
	if cfg.DagID != "my-dag" {
		t.Errorf("dag_id = %q, want my-dag", cfg.DagID)
	}
}

func TestValidateAcceptsScaffold(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proj")
	if _, _, err := run(t, "init", dir); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, "validate", dir); err != nil {
		t.Errorf("validate scaffold: %v", err)
	}
}

func TestValidateRejectsBadConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "leoflow.yaml"), []byte("dag_id: \"has spaces\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, "validate", dir); err == nil {
		t.Error("validate should reject a bad dag_id")
	}
}

func TestCompileProducesValidDAGJSON(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proj")
	if _, _, err := run(t, "init", dir); err != nil {
		t.Fatal(err)
	}
	parser := filepath.Join(t.TempDir(), "fake-parser.sh")
	script := "#!/usr/bin/env bash\n" +
		"out=\"\"\n" +
		"while [ $# -gt 0 ]; do case \"$1\" in --output) out=\"$2\"; shift 2;; *) shift;; esac; done\n" +
		"cat > \"$out\" <<'JSON'\n" +
		"{\"schema_version\":\"1.0\",\"dag_id\":\"proj\",\"dag_version\":\"dev\",\"image\":\"test:v1\",\"tasks\":[{\"task_id\":\"hello\",\"type\":\"python\",\"entrypoint\":\"dag:hello\"}]}\n" +
		"JSON\n"
	if err := os.WriteFile(parser, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "dag.json")
	if _, _, err := run(t, "compile", dir, "--output", out, "--image", "test:v1", "--parser-cmd", parser); err != nil {
		t.Fatalf("compile: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("reading dag.json: %v", err)
	}
	var spec domain.DAGSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	if err := spec.Validate(); err != nil {
		t.Errorf("compiled dag.json is invalid: %v", err)
	}
}

func TestStubCommandsAnnounceNotImplemented(t *testing.T) {
	for _, args := range [][]string{{"server"}} {
		out, _, err := run(t, args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if !strings.Contains(strings.ToLower(out), "not yet implemented") {
			t.Errorf("%v output = %q, want 'not yet implemented'", args, out)
		}
	}
}
