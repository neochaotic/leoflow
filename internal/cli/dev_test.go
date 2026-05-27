package cli

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/neochaotic/leoflow/internal/config"
	"github.com/neochaotic/leoflow/internal/domain"
	"github.com/neochaotic/leoflow/internal/setup"
)

// devTestCmd returns a cobra command whose stdout/stderr are discarded, for
// exercising dev helpers without noisy output.
func devTestCmd() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	return cmd
}

func TestDevBannerMarksEnvironment(t *testing.T) {
	b := liteBanner("http://localhost:8080")
	if !strings.Contains(b, "LITE") {
		t.Errorf("banner must shout LITE, got:\n%s", b)
	}
	if !strings.Contains(b, "http://localhost:8080") {
		t.Errorf("banner must show the UI url, got:\n%s", b)
	}
	if !strings.Contains(b, "\x1b[") {
		t.Errorf("banner must be colored (ANSI), got:\n%s", b)
	}
}

func TestMtimesChangedDetectsEdits(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "dag.py")
	if err := os.WriteFile(f, []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := projectMtimes([]string{f})

	// No change: same snapshot compares equal.
	if mtimesChanged(prev, projectMtimes([]string{f})) {
		t.Error("unchanged file reported as changed")
	}

	// Edit bumps the modtime (force it forward so the test is not flaky).
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(f, future, future); err != nil {
		t.Fatal(err)
	}
	if !mtimesChanged(prev, projectMtimes([]string{f})) {
		t.Error("edited file not detected")
	}
}

func TestMtimesChangedDetectsAddAndRemove(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "leoflow.yaml")
	b := filepath.Join(dir, "dag.py")
	if err := os.WriteFile(a, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := projectMtimes([]string{a, b}) // b absent

	if err := os.WriteFile(b, []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !mtimesChanged(prev, projectMtimes([]string{a, b})) {
		t.Error("newly-created file not detected")
	}
}

func TestResolveBinaryExplicitAndFallback(t *testing.T) {
	// Explicit wins.
	if got, err := resolveBinary("/custom/leoflow-server", "leoflow-server"); err != nil || got != "/custom/leoflow-server" {
		t.Errorf("explicit = (%q,%v), want /custom/leoflow-server", got, err)
	}
	// ./bin fallback: chdir into a temp dir holding bin/<name>.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	name := "leoflow-fake-bin"
	if err := os.WriteFile(filepath.Join(dir, "bin", name), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	// The fallback must be absolute: the subprocess executor runs the agent in a
	// different working directory, so a relative path would not resolve.
	got, err := resolveBinary("", name)
	if err != nil || !filepath.IsAbs(got) || filepath.Base(got) != name {
		t.Errorf("fallback = (%q,%v), want an absolute path ending in %s", got, err, name)
	}
	// Not found anywhere → actionable error.
	if _, err := resolveBinary("", "definitely-not-a-real-binary-xyz"); err == nil {
		t.Error("expected error for a missing binary")
	}
}

func TestDevWatchPaths(t *testing.T) {
	cfg := &domain.LeoflowConfig{DagID: "p", DagSource: "flows/etl.py"}
	got := devWatchPaths("proj", cfg)
	want := map[string]bool{
		filepath.Join("proj", "leoflow.yaml"):    true,
		filepath.Join("proj", "flows", "etl.py"): true,
	}
	if len(got) != 2 {
		t.Fatalf("watch paths = %v, want 2", got)
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("unexpected watch path %q", p)
		}
	}
}

func TestDevReadyOnce(t *testing.T) {
	ctx := context.Background()
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ok.Close()
	if !devReadyOnce(ctx, ok.URL) {
		t.Error("ready server should report ready")
	}

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	down.Close() // closed: connection refused
	if devReadyOnce(ctx, down.URL) {
		t.Error("closed server should not report ready")
	}
}

func TestWaitForReadyReturnsWhenUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if err := waitForReady(context.Background(), srv.URL); err != nil {
		t.Errorf("waitForReady = %v, want nil", err)
	}
}

func TestWaitForReadyHonorsCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Nothing is listening here and the context is already canceled.
	if err := waitForReady(ctx, "http://127.0.0.1:0"); err == nil {
		t.Error("expected error when context is canceled")
	}
}

func TestNewDevCommandFlagDefaults(t *testing.T) {
	cmd := newLiteCommand()
	for flag, want := range map[string]string{
		"compose": "", // empty = a managed compose under ~/.leoflow, materialized on first run
		"host":    "127.0.0.1",
		"image":   "leoflow-dev:local",
	} {
		if got, _ := cmd.Flags().GetString(flag); got != want {
			t.Errorf("--%s default = %q, want %q", flag, got, want)
		}
	}
}

func TestDevPrintHelpers(t *testing.T) {
	var b bytes.Buffer
	devPrintf(&b, "x=%d", 7)
	devPrintln(&b, "line")
	if b.String() != "x=7line\n" {
		t.Errorf("print helpers wrote %q", b.String())
	}
}

func TestRunDevValidatesProject(t *testing.T) {
	cmd := devTestCmd()
	// Missing leoflow.yaml.
	if err := runDev(cmd, t.TempDir(), devOptions{}); err == nil {
		t.Error("expected error for a project without leoflow.yaml")
	}
	// Present but invalid (missing dag_id).
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "leoflow.yaml"), []byte("description: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runDev(cmd, dir, devOptions{}); err == nil {
		t.Error("expected validation error for missing dag_id")
	}
}

func TestStartDevServerStartsAndErrors(t *testing.T) {
	cmd := devTestCmd()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A real, harmless binary starts successfully and a *Cmd is returned.
	srv, err := startDevServer(ctx, cmd, "/bin/sleep", subprocessServerEnv("127.0.0.1", 8088, "/bin/true", t.TempDir(), "python3", "", ""))
	if err != nil || srv == nil {
		t.Fatalf("startDevServer(real bin) = (%v,%v), want a running cmd", srv, err)
	}
	cancel()
	_ = srv.Wait()

	// A nonexistent binary fails at Start.
	if _, e := startDevServer(context.Background(), cmd, "/no/such/leoflow-server", sharedServerEnv("127.0.0.1", 8088, "", "")); e == nil {
		t.Error("expected error starting a nonexistent server binary")
	}
}

func TestDevWatchLoopExitsOnCancel(t *testing.T) {
	cmd := devTestCmd()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled: the loop must do its initial pass then return nil

	dir := t.TempDir()
	cfg := &domain.LeoflowConfig{DagID: "p"}
	reloads := 0
	reload := func() error { reloads++; return nil }
	if err := devWatchLoop(ctx, cmd, dir, cfg, reload); err != nil {
		t.Errorf("devWatchLoop on canceled ctx = %v, want nil", err)
	}
	if reloads != 1 {
		t.Errorf("reload ran %d times, want 1 initial pass", reloads)
	}
}

func TestVenvPythonOSAware(t *testing.T) {
	home := filepath.FromSlash("/home/u/.leoflow/dev")
	got := venvPython(home)
	want := filepath.Join(home, "venv", "bin", "python")
	if runtime.GOOS == "windows" {
		want = filepath.Join(home, "venv", "Scripts", "python.exe")
	}
	if got != want {
		t.Errorf("venvPython = %q, want %q", got, want)
	}
}

func TestVenvPipArgs(t *testing.T) {
	args := venvPipArgs("runtime/python", []string{"pandas==2.1.0"})
	joined := strings.Join(args, " ")
	for _, must := range []string{"-m pip install", "runtime/python", taskSDKVersion, "pandas==2.1.0"} {
		if !strings.Contains(joined, must) {
			t.Errorf("pip args %q missing %q", joined, must)
		}
	}
}

func TestDevDagImageRef(t *testing.T) {
	if got := devDagImageRef("my_etl"); got != "leoflow-dev-my_etl:dev" {
		t.Errorf("devDagImageRef = %q, want leoflow-dev-my_etl:dev", got)
	}
}

func TestDevDockerfile(t *testing.T) {
	df := devDockerfile("leoflow-base:py3.11", "dag.py", nil)
	for _, must := range []string{"FROM leoflow-base:py3.11", "COPY dag.py", "PYTHONPATH"} {
		if !strings.Contains(df, must) {
			t.Errorf("generated Dockerfile missing %q:\n%s", must, df)
		}
	}
	if strings.Contains(df, "pip install") {
		t.Errorf("no deps -> no pip install line:\n%s", df)
	}
	// Declared dependencies are pip-installed before COPY (cached layer).
	withDeps := devDockerfile("leoflow-base:py3.11", "dag.py", []string{"duckdb==1.1.3", "pandas"})
	if !strings.Contains(withDeps, "RUN pip install --no-cache-dir duckdb==1.1.3 pandas") {
		t.Errorf("deps not installed in Dockerfile:\n%s", withDeps)
	}
	if strings.Index(withDeps, "pip install") > strings.Index(withDeps, "COPY") {
		t.Errorf("pip install must come before COPY for layer caching:\n%s", withDeps)
	}
}

func TestMergeLiteDefaults(t *testing.T) {
	cfg := &config.Config{LiteExecutor: "subprocess", LitePort: 9091}

	t.Run("applies config when flags unset", func(t *testing.T) {
		o := devOptions{executor: "k8s", port: 8088}
		mergeLiteDefaults(&o, cfg, false, false)
		if o.executor != "subprocess" || o.port != 9091 {
			t.Errorf("got executor=%q port=%d, want subprocess/9091", o.executor, o.port)
		}
	})

	t.Run("command-line flags win", func(t *testing.T) {
		o := devOptions{executor: "k8s", port: 8088}
		mergeLiteDefaults(&o, cfg, true, true)
		if o.executor != "k8s" || o.port != 8088 {
			t.Errorf("got executor=%q port=%d, want the flag values kept", o.executor, o.port)
		}
	})

	t.Run("nil/empty config is a no-op", func(t *testing.T) {
		o := devOptions{executor: "k8s", port: 8088}
		mergeLiteDefaults(&o, nil, false, false)
		mergeLiteDefaults(&o, &config.Config{}, false, false)
		if o.executor != "k8s" || o.port != 8088 {
			t.Errorf("got executor=%q port=%d, want unchanged", o.executor, o.port)
		}
	})
}

func TestServerEnvBuilders(t *testing.T) {
	sub := strings.Join(subprocessServerEnv("127.0.0.1", 8088, "/bin/agent", "/proj", "/venv/py", "", ""), "\n")
	// Lite binds its own HTTP/gRPC/metrics ports (distinct from the demo's 8080/9090/9091).
	for _, must := range []string{"LEOFLOW_EXECUTOR_TYPE=subprocess", "LEOFLOW_EXECUTOR_AGENT_PATH=/bin/agent", "LEOFLOW_EXECUTOR_SUBPROCESS_WORKDIR=/proj", "LEOFLOW_PYTHON=/venv/py", "127.0.0.1:9099", "LEOFLOW_SERVER_HTTP_ADDR=127.0.0.1:8088", "LEOFLOW_SERVER_GRPC_ADDR=:9099", "LEOFLOW_SERVER_METRICS_ADDR=:9098"} {
		if !strings.Contains(sub, must) {
			t.Errorf("subprocessServerEnv missing %q", must)
		}
	}
	clu := strings.Join(clusterServerEnv("127.0.0.1", 8088, "/home/u/.leoflow/dev/kubeconfig", "", ""), "\n")
	for _, must := range []string{"LEOFLOW_EXECUTOR_TYPE=kubernetes", "KUBECONFIG=/home/u/.leoflow/dev/kubeconfig", "LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR=" + devHostGRPCAddr(8088)} {
		if !strings.Contains(clu, must) {
			t.Errorf("clusterServerEnv missing %q", must)
		}
	}
	// gRPC/metrics derive from --port so two Lite instances coexist: a different
	// --port must yield different gRPC/metrics ports (and OTel is off in Lite).
	if !strings.Contains(sub, "LEOFLOW_OBSERVABILITY_OTEL_ENABLED=false") {
		t.Error("Lite env should disable the OTLP exporter (no local collector)")
	}
	alt := strings.Join(subprocessServerEnv("127.0.0.1", 8090, "/bin/agent", "/proj", "/venv/py", "", ""), "\n")
	for _, must := range []string{"LEOFLOW_SERVER_GRPC_ADDR=:9101", "LEOFLOW_SERVER_METRICS_ADDR=:9100", "127.0.0.1:9101"} {
		if !strings.Contains(alt, must) {
			t.Errorf("--port 8090 should offset gRPC/metrics, missing %q", must)
		}
	}
	// Both carry the shared settings (isolated DB + LITE badge).
	if !strings.Contains(clu, "LEOFLOW_DATABASE_URL="+devDatabaseURL) || !strings.Contains(clu, "LEOFLOW_UI_EDITION=lite") {
		t.Error("clusterServerEnv missing shared settings")
	}
}

func TestSharedServerEnvAuthModes(t *testing.T) {
	// No admin configured -> dev no-auth fallback (loopback only).
	noAdmin := strings.Join(sharedServerEnv("127.0.0.1", 8088, "", ""), "\n")
	if !strings.Contains(noAdmin, "LEOFLOW_AUTH_DEV_NO_AUTH=true") {
		t.Error("with no admin, expected the dev no-auth fallback")
	}
	if strings.Contains(noAdmin, "LEOFLOW_BOOTSTRAP_PASSWORD_HASH") {
		t.Error("no admin should not set a bootstrap hash")
	}
	// Admin hash configured -> real auth: bootstrap the admin, NO bypass.
	withAdmin := strings.Join(sharedServerEnv("127.0.0.1", 8088, "$2a$12$hash", "admin@leoflow.local"), "\n")
	if strings.Contains(withAdmin, "LEOFLOW_AUTH_DEV_NO_AUTH") {
		t.Error("with an admin hash, the dev no-auth bypass must be OFF")
	}
	for _, must := range []string{"LEOFLOW_BOOTSTRAP_PASSWORD_HASH=$2a$12$hash", "LEOFLOW_BOOTSTRAP_EMAIL=admin@leoflow.local", "LEOFLOW_UI_EDITION=lite"} {
		if !strings.Contains(withAdmin, must) {
			t.Errorf("real-auth env missing %q", must)
		}
	}
	// Lite mints 30-day sessions (not the server's 1h default) so the user is not
	// silently logged out mid-session.
	for _, env := range []string{noAdmin, withAdmin} {
		if !strings.Contains(env, "LEOFLOW_AUTH_JWT_TOKEN_TTL_SECONDS=2592000") {
			t.Errorf("lite env missing 30-day token TTL; got:\n%s", env)
		}
		// A local single-user tool gets a generous login rate limit so fat-fingering
		// the password does not lock the user out.
		if !strings.Contains(env, "LEOFLOW_AUTH_LOGIN_RATE_LIMIT_PER_MINUTE=30") {
			t.Errorf("lite env missing generous login rate limit; got:\n%s", env)
		}
		// Logs go under the per-user ~/.leoflow, NOT a shared /tmp path that
		// collides across users (root vs non-root permission-denied trap).
		if !strings.Contains(env, filepath.Join(".leoflow", "dev", "logs")) {
			t.Errorf("logs dir must be per-user under ~/.leoflow/dev/logs; got:\n%s", env)
		}
		if strings.Contains(env, "leoflow-dev-logs") {
			t.Errorf("logs dir must not use the shared /tmp path; got:\n%s", env)
		}
	}
}

func TestLiteEditorEnv(t *testing.T) {
	env := strings.Join(liteEditorEnv("/home/u/proj", "/home/u/.leoflow"), "\n")
	if !strings.Contains(env, "LEOFLOW_UI_WORKSPACE=/home/u/proj") {
		t.Errorf("editor env missing workspace; got %q", env)
	}
	wantMonaco := "LEOFLOW_UI_MONACO_DIR=" + setup.MonacoDir("/home/u/.leoflow")
	if !strings.Contains(env, wantMonaco) {
		t.Errorf("editor env missing %q; got %q", wantMonaco, env)
	}
}

func TestK3dArgBuilders(t *testing.T) {
	if got := strings.Join(k3dCreateArgs("leoflow-dev"), " "); got != "cluster create leoflow-dev --wait" {
		t.Errorf("k3dCreateArgs = %q", got)
	}
	if got := strings.Join(k3dImportArgs("leoflow-dev", "base:1", "dag:dev"), " "); got != "image import base:1 dag:dev --cluster leoflow-dev" {
		t.Errorf("k3dImportArgs = %q", got)
	}
}

func TestDevKubeconfigPath(t *testing.T) {
	home := filepath.FromSlash("/home/u/.leoflow/dev")
	if got := devKubeconfigPath(home); got != filepath.Join(home, "kubeconfig") {
		t.Errorf("devKubeconfigPath = %q", got)
	}
}

func TestEnsureProjectDockerfile(t *testing.T) {
	cmd := devTestCmd()
	cfg := &domain.LeoflowConfig{DagID: "p", DagSource: "dag.py"}

	// Generates one when absent.
	dir := t.TempDir()
	if err := ensureProjectDockerfile(cmd, dir, cfg); err != nil {
		t.Fatalf("ensureProjectDockerfile: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	if err != nil {
		t.Fatalf("Dockerfile not generated: %v", err)
	}
	if !strings.Contains(string(data), "FROM "+devBaseImage) {
		t.Errorf("generated Dockerfile = %q", data)
	}

	// Leaves an existing Dockerfile untouched.
	dir2 := t.TempDir()
	custom := []byte("FROM my/custom:image\n")
	if werr := os.WriteFile(filepath.Join(dir2, "Dockerfile"), custom, 0o600); werr != nil {
		t.Fatal(werr)
	}
	if err := ensureProjectDockerfile(cmd, dir2, cfg); err != nil {
		t.Fatalf("ensureProjectDockerfile (existing): %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir2, "Dockerfile"))
	if !bytes.Equal(got, custom) {
		t.Errorf("existing Dockerfile was overwritten: %q", got)
	}
}

func TestDevSubprocessSetupMissingAgent(t *testing.T) {
	t.Chdir(t.TempDir()) // no agent on PATH or ./bin
	cmd := devTestCmd()
	cfg := &domain.LeoflowConfig{DagID: "p"}
	if _, _, err := devSubprocessSetup(context.Background(), cmd, ".", devOptions{}, t.TempDir(), cfg); err == nil {
		t.Error("expected error when the agent binary is missing")
	}
}

func TestBaseImageBuildArgs(t *testing.T) {
	got := strings.Join(baseImageBuildArgs(), " ")
	for _, must := range []string{"build", "runtime/Dockerfile", "PYTHON_VERSION=" + devPyVersion, devBaseImage} {
		if !strings.Contains(got, must) {
			t.Errorf("baseImageBuildArgs %q missing %q", got, must)
		}
	}
}

func TestKubectlNamespaceArgs(t *testing.T) {
	got := strings.Join(kubectlNamespaceArgs("/kc"), " ")
	if got != "--kubeconfig /kc create namespace "+devNamespace {
		t.Errorf("kubectlNamespaceArgs = %q", got)
	}
}

func TestDevClusterSetupStubbed(t *testing.T) {
	// Cluster mode builds the base image from runtime/Dockerfile (CWD context);
	// run from a tree that has it so ensureBaseImage's source-tree precheck passes.
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "runtime"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "runtime", "Dockerfile"), []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(src)

	origRun, origOut := devRun, devOutput
	defer func() { devRun, devOutput = origRun, origOut }()
	var runs []string
	devRun = func(_ context.Context, _ *cobra.Command, name string, args ...string) error {
		runs = append(runs, name+" "+strings.Join(args, " "))
		return nil
	}
	devOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "docker": // image inspect → "absent" so the base image is built
			return nil, errors.New("no such image")
		case name == "k3d" && len(args) > 1 && args[1] == "list": // cluster absent → create
			return []byte(""), nil
		case name == "k3d": // kubeconfig get
			return []byte("apiVersion: v1\nkind: Config\n"), nil
		case name == "kubectl":
			return []byte("namespace/leoflow created"), nil
		}
		return nil, nil
	}

	home, dir := t.TempDir(), t.TempDir()
	cfg := &domain.LeoflowConfig{DagID: "etl", DagSource: "dag.py"}
	env, makeReload, err := devClusterSetup(context.Background(), devTestCmd(), dir, devOptions{}, home, cfg)
	if err != nil {
		t.Fatalf("devClusterSetup: %v", err)
	}
	if makeReload == nil || makeReload("tok") == nil {
		t.Fatal("expected a reload factory")
	}
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "LEOFLOW_EXECUTOR_TYPE=kubernetes") || !strings.Contains(joined, "KUBECONFIG="+devKubeconfigPath(home)) {
		t.Errorf("cluster env did not target the isolated cluster: %v", env)
	}
	if _, e := os.Stat(devKubeconfigPath(home)); e != nil {
		t.Error("isolated kubeconfig was not written")
	}
	if _, e := os.Stat(filepath.Join(dir, "Dockerfile")); e != nil {
		t.Error("default Dockerfile was not generated")
	}
	// The base image build and cluster create were invoked.
	if !strings.Contains(strings.Join(runs, "\n"), "docker build") || !strings.Contains(strings.Join(runs, "\n"), "cluster create") {
		t.Errorf("expected docker build + k3d create, got: %v", runs)
	}
}

func TestK3dImportStubbed(t *testing.T) {
	origRun := devRun
	defer func() { devRun = origRun }()
	var got string
	devRun = func(_ context.Context, _ *cobra.Command, name string, args ...string) error {
		got = name + " " + strings.Join(args, " ")
		return nil
	}
	if err := k3dImport(context.Background(), devTestCmd(), "leoflow-dev-etl:dev"); err != nil {
		t.Fatalf("k3dImport: %v", err)
	}
	if !strings.Contains(got, "image import leoflow-dev-etl:dev") || !strings.Contains(got, "--cluster "+devClusterName) {
		t.Errorf("k3dImport invoked %q", got)
	}
}
