package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neochaotic/leoflow/internal/domain"
)

func writeCtx(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func readIgnore(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, ".dockerignore"))
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatal(err)
	}
	return string(b)
}

// TestExcludePathsReachTheBuildContext is the #995 regression. exclude_paths was
// in the schema, defaulted and documented as "skipped both in image build and
// workspace discovery" with zero consumers in build code, so with the mode-1
// default `project: "."` the single `COPY . /home/leoflow/` baked the whole
// context — a `.env`, a BYO `profiles.yml`, `.git`, and the generated Dockerfile
// the build had just written.
func TestExcludePathsReachTheBuildContext(t *testing.T) {
	dir := writeCtx(t, map[string]string{"dag.py": "x", ".git/config": "x"})
	cfg := &domain.LeoflowConfig{DagID: "d"}
	cfg.ExcludePaths = []string{"secrets", "*.pem"}
	cfg.ApplyDefaults()

	cleanup, err := ensureDockerignore(&bytes.Buffer{}, dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := readIgnore(t, dir)
	for _, want := range []string{"secrets", "*.pem", generatedDockerfileName} {
		if !strings.Contains(got, want) {
			t.Errorf("the .dockerignore does not carry %q; exclude_paths never reached the build\n%s", want, got)
		}
	}
	// The generated Dockerfile is written INTO the context by ensureDockerfile
	// and has no business inside the image it builds.
	cleanup()
	if readIgnore(t, dir) != "" {
		t.Error("the workspace was left dirty: a .dockerignore we created outlived the build")
	}
}

// TestUserDockerignoreIsMergedNotReplaced pins the trap that decided the design.
// BuildKit's `<dockerfile>.dockerignore` would need no cleanup, but where it is
// honored it REPLACES the context .dockerignore — measured: with a user file
// excluding big.bin and a per-Dockerfile file excluding .env, big.bin shipped.
// Closing one leak by reopening the author's own exclusions is not a fix.
func TestUserDockerignoreIsMergedNotReplaced(t *testing.T) {
	dir := writeCtx(t, map[string]string{".dockerignore": "big.bin\n!keep.me\n", "dag.py": "x"})
	cfg := &domain.LeoflowConfig{DagID: "d"}
	cfg.ApplyDefaults()

	cleanup, err := ensureDockerignore(&bytes.Buffer{}, dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := readIgnore(t, dir)
	if !strings.Contains(got, "big.bin") || !strings.Contains(got, "!keep.me") {
		t.Errorf("the author's own rules were dropped:\n%s", got)
	}
	if !strings.Contains(got, ".git") {
		t.Errorf("our excludes were not added:\n%s", got)
	}
	// Ours last: later rules win in .dockerignore, and leoflow.yaml is the
	// authoritative statement of what may leave in the image.
	if strings.Index(got, "big.bin") > strings.Index(got, ".git") {
		t.Errorf("our block precedes the author's, so a stray !rule could defeat exclude_paths:\n%s", got)
	}

	cleanup()
	if restored := readIgnore(t, dir); restored != "big.bin\n!keep.me\n" {
		t.Errorf("the author's .dockerignore was not restored byte-for-byte, got:\n%q", restored)
	}
}

// TestDbtArtifactsAreScopedToTheirProject is the #1013 regression. The compile
// runs a host-side `dbt parse`, which writes into the project it parsed, and the
// wholesale COPY then bakes the result: `.user.yml` (dbt's anonymous-usage
// cookie, a stable UUID identifying the BUILD HOST, read by the in-pod dbt so
// every pod reports as that user) and `logs/dbt.log` (absolute host paths — the
// #993 class arriving through a door the entrypoint assertions do not watch).
//
// Scoped to the project directories rather than added to the defaults, because
// `logs` and `target` are ordinary names a non-dbt project may want shipped.
func TestDbtArtifactsAreScopedToTheirProject(t *testing.T) {
	dir := writeCtx(t, map[string]string{"dag.py": "x"})
	cfg := &domain.LeoflowConfig{DagID: "d"}
	cfg.DbtGroups = map[string]*domain.DbtConfig{"a": {Project: "transform"}}
	cfg.ApplyDefaults()

	if _, err := ensureDockerignore(&bytes.Buffer{}, dir, cfg); err != nil {
		t.Fatal(err)
	}
	got := readIgnore(t, dir)
	for _, want := range []string{
		"transform/target", "transform/logs", "transform/.user.yml",
		"transform/dbt_packages", "transform/profiles.yml",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q — a dbt build artifact would be baked\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"\nlogs\n", "\ntarget\n"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("unscoped %q would exclude an unrelated directory in a non-dbt tree\n%s", unwanted, got)
		}
	}
}

func TestNoDbtMeansNoDbtExcludes(t *testing.T) {
	dir := writeCtx(t, map[string]string{"dag.py": "x"})
	cfg := &domain.LeoflowConfig{DagID: "d"}
	cfg.ApplyDefaults()
	if _, err := ensureDockerignore(&bytes.Buffer{}, dir, cfg); err != nil {
		t.Fatal(err)
	}
	if got := readIgnore(t, dir); strings.Contains(got, "dbt_packages") {
		t.Errorf("a project with no dbt got dbt excludes:\n%s", got)
	}
}

// TestSecretishFilesWarnRatherThanVanish. Silently dropping .env would break a
// DAG that calls load_dotenv() with a failure far from its cause; letting a
// password ship unnoticed is also wrong. The author is told in time to act.
func TestSecretishFilesWarnRatherThanVanish(t *testing.T) {
	dir := writeCtx(t, map[string]string{".env": "PASSWORD=hunter2", "dag.py": "x"})
	cfg := &domain.LeoflowConfig{DagID: "d"}
	cfg.ApplyDefaults()

	var out bytes.Buffer
	if _, err := ensureDockerignore(&out, dir, cfg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), ".env") {
		t.Errorf("no warning for a .env in the build context, got %q", out.String())
	}
	if strings.Contains(readIgnore(t, dir), "\n.env\n") {
		t.Error(".env was excluded silently; that breaks load_dotenv() far from its cause")
	}

	// And the warning stops once the author acts on it.
	cfg2 := &domain.LeoflowConfig{DagID: "d"}
	cfg2.ExcludePaths = []string{".env"}
	cfg2.ApplyDefaults()
	var out2 bytes.Buffer
	if _, err := ensureDockerignore(&out2, dir, cfg2); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out2.String(), "warning") {
		t.Errorf("still warning after .env was added to exclude_paths: %q", out2.String())
	}
}

// TestMergeIsIdempotentAcrossAnInterruptedBuild. A build killed before cleanup
// leaves the merged file behind — a superset of the author's, so it excludes
// more and never less, which is the right direction to fail in. The next build
// must not stack duplicate blocks onto it.
func TestMergeIsIdempotentAcrossAnInterruptedBuild(t *testing.T) {
	dir := writeCtx(t, map[string]string{".dockerignore": "big.bin\n", "dag.py": "x"})
	cfg := &domain.LeoflowConfig{DagID: "d"}
	cfg.ApplyDefaults()

	for range 3 {
		if _, err := ensureDockerignore(&bytes.Buffer{}, dir, cfg); err != nil {
			t.Fatal(err)
		}
	}
	got := readIgnore(t, dir)
	if n := strings.Count(got, dockerignoreHeader); n != 1 {
		t.Errorf("header appears %d times after three interrupted builds, want 1\n%s", n, got)
	}
	if n := strings.Count(got, "\n.git\n"); n != 1 {
		t.Errorf(".git appears %d times, want 1\n%s", n, got)
	}
}
