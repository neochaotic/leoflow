package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/neochaotic/leoflow/internal/domain"
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
	b := devBanner("http://localhost:8080")
	if !strings.Contains(b, "DEV") {
		t.Errorf("banner must shout DEV, got:\n%s", b)
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
	if got, err := resolveBinary("", name); err != nil || got != filepath.Join("bin", name) {
		t.Errorf("fallback = (%q,%v), want bin/%s", got, err, name)
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
	cmd := newDevCommand()
	for flag, want := range map[string]string{
		"compose":    "docker-compose.dev.yaml",
		"migrations": "migrations",
		"image":      "leoflow-dev:local",
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

func TestResolveDevBinaries(t *testing.T) {
	server, agent, err := resolveDevBinaries(devOptions{serverBin: "/s", agentBin: "/a"})
	if err != nil || server != "/s" || agent != "/a" {
		t.Errorf("resolveDevBinaries = (%q,%q,%v), want /s /a nil", server, agent, err)
	}
	// A missing server binary surfaces an actionable error.
	t.Chdir(t.TempDir())
	if _, _, e := resolveDevBinaries(devOptions{agentBin: "/a"}); e == nil {
		t.Error("expected error when the server binary is missing")
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
	srv, err := startDevServer(ctx, cmd, "/bin/sleep", "/bin/true")
	if err != nil || srv == nil {
		t.Fatalf("startDevServer(real bin) = (%v,%v), want a running cmd", srv, err)
	}
	cancel()
	_ = srv.Wait()

	// A nonexistent binary fails at Start.
	if _, e := startDevServer(context.Background(), cmd, "/no/such/leoflow-server", "/bin/true"); e == nil {
		t.Error("expected error starting a nonexistent server binary")
	}
}

func TestDevWatchLoopExitsOnCancel(t *testing.T) {
	cmd := devTestCmd()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled: the loop must do its initial pass then return nil

	dir := t.TempDir() // no leoflow.yaml → the initial compile fails fast (no parser run)
	cfg := &domain.LeoflowConfig{DagID: "p"}
	if err := devWatchLoop(ctx, cmd, dir, cfg, devOptions{image: "x"}, "tok"); err != nil {
		t.Errorf("devWatchLoop on canceled ctx = %v, want nil", err)
	}
}
