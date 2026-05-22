package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestCompileBuildsImageWhenRequested(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proj")
	if _, _, err := run(t, "init", dir); err != nil {
		t.Fatal(err)
	}
	parser := filepath.Join(t.TempDir(), "fake-parser.sh")
	pscript := "#!/usr/bin/env bash\n" +
		"out=\"\"\n" +
		"while [ $# -gt 0 ]; do case \"$1\" in --output) out=\"$2\"; shift 2;; *) shift;; esac; done\n" +
		"cat > \"$out\" <<'JSON'\n" +
		"{\"schema_version\":\"1.0\",\"dag_id\":\"proj\",\"dag_version\":\"dev\",\"image\":\"test:v1\",\"tasks\":[{\"task_id\":\"hello\",\"type\":\"python\",\"entrypoint\":\"dag:hello\"}]}\n" +
		"JSON\n"
	if err := os.WriteFile(parser, []byte(pscript), 0o755); err != nil {
		t.Fatal(err)
	}
	// Fake builder records its argv so we can assert the docker build invocation.
	marker := filepath.Join(t.TempDir(), "built.txt")
	builder := filepath.Join(t.TempDir(), "fake-docker.sh")
	if err := os.WriteFile(builder, []byte("#!/usr/bin/env bash\necho \"$@\" > "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "dag.json")
	if _, _, err := run(t, "compile", dir, "--output", out, "--image", "test:v1",
		"--parser-cmd", parser, "--build", "--builder", builder); err != nil {
		t.Fatalf("compile --build: %v", err)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("builder was not invoked: %v", err)
	}
	if !strings.Contains(string(got), "build") || !strings.Contains(string(got), "-t test:v1") {
		t.Errorf("builder argv = %q, want a 'build -t test:v1' invocation", got)
	}
}

func TestCompileBuildRequiresImage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proj")
	if _, _, err := run(t, "init", dir); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, "compile", dir, "--build", "--parser-cmd", "true"); err == nil {
		t.Error("--build without --image should error")
	}
}

func TestRunsTrigger(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"dag_run_id":"run-123","state":"queued"}`))
	}))
	defer srv.Close()

	out, _, err := run(t, "runs", "trigger", "etl", "--server", srv.URL, "--token", "t")
	if err != nil {
		t.Fatalf("runs trigger: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v2/dags/etl/dagRuns" {
		t.Errorf("hit %s %s, want POST /api/v2/dags/etl/dagRuns", gotMethod, gotPath)
	}
	if !strings.Contains(out, "run-123") || !strings.Contains(out, "queued") {
		t.Errorf("output = %q, want run id and state", out)
	}
}

func TestRunsStatusLatest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"dag_runs":[{"dag_run_id":"run-9","state":"success"}],"total_entries":1}`))
	}))
	defer srv.Close()

	out, _, err := run(t, "runs", "status", "etl", "--server", srv.URL, "--token", "t")
	if err != nil {
		t.Fatalf("runs status: %v", err)
	}
	if !strings.Contains(out, "run-9") || !strings.Contains(out, "success") {
		t.Errorf("output = %q, want latest run id and state", out)
	}
}

func TestRunsStatusByRunID(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"dag_run_id":"run-7","state":"running"}`))
	}))
	defer srv.Close()

	out, _, err := run(t, "runs", "status", "etl", "--run", "run-7", "--server", srv.URL, "--token", "t")
	if err != nil {
		t.Fatalf("runs status --run: %v", err)
	}
	if gotPath != "/api/v2/dags/etl/dagRuns/run-7" {
		t.Errorf("hit %s, want the specific run endpoint", gotPath)
	}
	if !strings.Contains(out, "run-7") || !strings.Contains(out, "running") {
		t.Errorf("output = %q, want the run id and state", out)
	}
}

func TestRunsStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	if _, _, err := run(t, "runs", "status", "etl", "--server", srv.URL); err == nil {
		t.Error("a non-2xx status should error")
	}
}

func TestServerCommandPointsToBinary(t *testing.T) {
	out, _, err := run(t, "server")
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	if !strings.Contains(out, "leoflow-server") {
		t.Errorf("server output = %q, want mention of leoflow-server", out)
	}
}
