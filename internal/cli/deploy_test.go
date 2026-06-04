package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neochaotic/leoflow/internal/domain"
)

func TestRegisterDeployedDAGRepinsAndRegisters(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	// A compiled dag.json carrying the tag ref; register must re-pin it to the
	// digest before posting, and the file on disk must end up re-pinned too.
	out := filepath.Join(t.TempDir(), "dag.json")
	if err := os.WriteFile(out, []byte(pushSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	digest := "ghcr.io/org/etl@sha256:cafef00d"

	var sb strings.Builder
	err := registerDeployedDAG(context.Background(), &sb, srv.URL, "tok", out, digest, "v1")
	if err != nil {
		t.Fatalf("registerDeployedDAG: %v", err)
	}
	if gotPath != "/api/v2/dags/etl/versions" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("auth = %q, want Bearer tok", gotAuth)
	}
	if !strings.Contains(gotBody, digest) {
		t.Errorf("posted spec was not re-pinned to the digest: %s", gotBody)
	}
	if !strings.Contains(sb.String(), "Deployed etl") {
		t.Errorf("summary = %q, want a Deployed line", sb.String())
	}
	// The on-disk dag.json must also be re-pinned (the immutable artifact).
	repinned, _ := os.ReadFile(out)
	if !strings.Contains(string(repinned), digest) {
		t.Errorf("dag.json on disk was not re-pinned: %s", repinned)
	}
}

func TestRegisterDeployedDAGSurfacesServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "forbidden")
	}))
	defer srv.Close()

	out := filepath.Join(t.TempDir(), "dag.json")
	if err := os.WriteFile(out, []byte(pushSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	err := registerDeployedDAG(context.Background(), io.Discard, srv.URL, "tok", out, "r@sha256:x", "v1")
	if err == nil {
		t.Error("expected an error when the control plane rejects the register")
	}
}

func TestDeployRunsPastGuardThenFailsAtCompile(t *testing.T) {
	// A project WITH a registry passes the guard, so deploy proceeds to resolve
	// the image ref and invoke compile. With the parser stubbed to a failing
	// command, the run fails after the guard — exercising the orchestration
	// middle without a real parser or builder.
	dir := filepath.Join(t.TempDir(), "proj")
	if _, _, err := run(t, "init", dir); err != nil {
		t.Fatalf("init: %v", err)
	}
	f := filepath.Join(dir, "leoflow.yaml")
	data, _ := os.ReadFile(f)
	withRegistry := string(data) + "\nregistry:\n  url: ghcr.io/org\n  image_name: etl\n"
	if err := os.WriteFile(f, []byte(withRegistry), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("LEOFLOW_PARSER_CMD", "false") // deterministic compile failure, no python
	if _, _, err := run(t, "deploy", dir, "--server", "http://127.0.0.1:0"); err == nil {
		t.Error("expected deploy to fail at compile when the parser fails")
	}
}

func TestResolveServerTokenPrecedence(t *testing.T) {
	cmd := newDeployCommand()
	// Flags win outright — config is not even consulted.
	srv, tok, err := resolveServerToken(cmd, "https://pro", "flag-tok")
	if err != nil {
		t.Fatalf("resolveServerToken: %v", err)
	}
	if srv != "https://pro" || tok != "flag-tok" {
		t.Errorf("flags should win: got server=%q token=%q", srv, tok)
	}
}

func TestResolveServerTokenFallsBackToEnv(t *testing.T) {
	t.Setenv("LEOFLOW_SERVER_URL", "https://env-pro")
	t.Setenv("LEOFLOW_TOKEN", "env-tok")
	cmd := newDeployCommand()
	srv, tok, err := resolveServerToken(cmd, "", "")
	if err != nil {
		t.Fatalf("resolveServerToken: %v", err)
	}
	if srv != "https://env-pro" || tok != "env-tok" {
		t.Errorf("config/env fallback: got server=%q token=%q", srv, tok)
	}
}

func TestDeployImageRef(t *testing.T) {
	cfg := &domain.LeoflowConfig{Registry: &domain.RegistryConfig{
		URL: "ghcr.io/org", ImageName: "etl", TagStrategy: "version",
	}}
	if got := deployImageRef(cfg, "v3"); got != "ghcr.io/org/etl:v3" {
		t.Errorf("ref = %q, want ghcr.io/org/etl:v3", got)
	}
}

func TestRepinImageInSpec(t *testing.T) {
	in := []byte(`{"schema_version":"1.0","dag_id":"etl","dag_version":"v1","image":"ghcr.io/org/etl:v1","tasks":[{"task_id":"a","type":"python","entrypoint":"dag:a"}]}`)
	out, err := repinImageInSpec(in, "ghcr.io/org/etl@sha256:deadbeef")
	if err != nil {
		t.Fatalf("repinImageInSpec: %v", err)
	}
	var m map[string]any
	if uerr := json.Unmarshal(out, &m); uerr != nil {
		t.Fatalf("output is not valid JSON: %v", uerr)
	}
	if m["image"] != "ghcr.io/org/etl@sha256:deadbeef" {
		t.Errorf("image = %v, want the digest-pinned ref", m["image"])
	}
	// Every other field must survive the re-pin untouched.
	if m["dag_id"] != "etl" || m["dag_version"] != "v1" {
		t.Errorf("re-pin dropped fields: %v", m)
	}
	if tasks, ok := m["tasks"].([]any); !ok || len(tasks) != 1 {
		t.Errorf("tasks not preserved: %v", m["tasks"])
	}
}

func TestDeployCommandRejectsMissingRegistry(t *testing.T) {
	// A scaffolded project has no registry: deploy must fail at the guard,
	// before touching any builder or server.
	dir := filepath.Join(t.TempDir(), "proj")
	if _, _, err := run(t, "init", dir); err != nil {
		t.Fatalf("init: %v", err)
	}
	_, stderr, err := run(t, "deploy", dir)
	if err == nil {
		t.Fatal("expected deploy to fail on a project with no registry")
	}
	if !strings.Contains(err.Error()+stderr, "registry") {
		t.Errorf("error should name the missing registry; got err=%v stderr=%q", err, stderr)
	}
}

func TestRequireRegistryRejectsUnconfigured(t *testing.T) {
	// ApplyDefaults instantiates Registry but leaves URL empty — deploy must
	// still reject it, loudly and actionably (ADR 0041).
	cfg := &domain.LeoflowConfig{}
	cfg.ApplyDefaults()

	err := requireRegistry(cfg)
	if err == nil {
		t.Fatal("expected requireRegistry to reject a config with no registry URL")
	}
	msg := err.Error()
	for _, want := range []string{"registry", "leoflow.yaml", "docker login"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q; got:\n%s", want, msg)
		}
	}
}

func TestRequireRegistryRejectsMissingImageName(t *testing.T) {
	cfg := &domain.LeoflowConfig{Registry: &domain.RegistryConfig{URL: "ghcr.io/org"}}
	if err := requireRegistry(cfg); err == nil {
		t.Error("expected requireRegistry to reject a registry URL with no image_name")
	}
}

func TestRequireRegistryAcceptsConfigured(t *testing.T) {
	cfg := &domain.LeoflowConfig{Registry: &domain.RegistryConfig{URL: "ghcr.io/org", ImageName: "etl"}}
	if err := requireRegistry(cfg); err != nil {
		t.Errorf("requireRegistry on a configured registry: %v", err)
	}
}
