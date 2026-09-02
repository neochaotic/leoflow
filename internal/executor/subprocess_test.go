package executor

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestAgentEnv(t *testing.T) {
	env := agentEnv(Request{
		ControlPlaneAddr: "localhost:9000",
		AgentToken:       "tok",
		TaskInstanceID:   "ti-1",
		Env:              map[string]string{"FOO": "bar"},
	})
	want := map[string]bool{
		"LEOFLOW_CONTROL_PLANE_ADDR=localhost:9000": true,
		"LEOFLOW_AGENT_TOKEN=tok":                   true,
		"LEOFLOW_TASK_INSTANCE_ID=ti-1":             true,
		"FOO=bar":                                   true,
	}
	for _, e := range env {
		delete(want, e)
	}
	if len(want) != 0 {
		t.Errorf("missing env entries: %v (got %v)", want, env)
	}
}

// TestDbtScratchEnv: a Lite task gets DBT_PROFILES_DIR/TARGET/LOG pointed at the
// private scratch, never the CWD (the dbt project), so profiles.yml — which
// carries the connection secret — can't clobber the repo's versioned file (#882).
func TestDbtScratchEnv(t *testing.T) {
	scratch := t.TempDir()
	got := map[string]string{}
	for _, kv := range dbtScratchEnv(scratch) {
		i := strings.IndexByte(kv, '=')
		got[kv[:i]] = kv[i+1:]
	}
	if got["DBT_PROFILES_DIR"] != scratch {
		t.Errorf("DBT_PROFILES_DIR = %q, want %q", got["DBT_PROFILES_DIR"], scratch)
	}
	if got["DBT_TARGET_PATH"] != filepath.Join(scratch, "target") {
		t.Errorf("DBT_TARGET_PATH = %q", got["DBT_TARGET_PATH"])
	}
	if got["DBT_LOG_PATH"] != filepath.Join(scratch, "logs") {
		t.Errorf("DBT_LOG_PATH = %q", got["DBT_LOG_PATH"])
	}
	if cwd, _ := os.Getwd(); got["DBT_PROFILES_DIR"] == cwd {
		t.Error("DBT_PROFILES_DIR must never be the task CWD (the dbt project) — #882")
	}
}

func writeScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "agent.sh")
	if err := os.WriteFile(p, []byte("#!/usr/bin/env bash\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// waitForFile polls for a file to appear (the subprocess executor launches the
// agent asynchronously, so its side effects land shortly after Execute returns).
func waitForFile(t *testing.T, path string) []byte {
	t.Helper()
	// Generous deadline: the executor spawns the agent asynchronously, and under
	// parallel `go test ./...` with coverage instrumentation that side effect can
	// land well after Execute returns. Poll up to 10s (returns as soon as the file
	// appears) so the test is not flaky under load — a fixed 2s wait was.
	for i := 0; i < 200; i++ {
		if data, err := os.ReadFile(path); err == nil {
			return data
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("file %s never appeared", path)
	return nil
}

func TestSubprocessExecuteRunsInWorkDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash agent stub is POSIX-only")
	}
	dir := t.TempDir()
	// The script records its working directory; with SetWorkDir the agent must
	// run there (so a dev host can import the project's dag.py).
	e := NewSubprocessExecutor(writeScript(t, "pwd > cwd.txt"), discardLogger())
	e.SetWorkDir(dir)
	if _, err := e.Execute(context.Background(), Request{TaskID: "t"}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := waitForFile(t, filepath.Join(dir, "cwd.txt"))
	// macOS resolves TempDir under /private; compare the basename to stay portable.
	if filepath.Base(strings.TrimSpace(string(got))) != filepath.Base(dir) {
		t.Errorf("agent ran in %q, want workdir %q", strings.TrimSpace(string(got)), dir)
	}
}

func TestSubprocessExecuteSurvivesContextCancel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash agent stub is POSIX-only")
	}
	// The agent must outlive the dispatch context, exactly like a Kubernetes pod
	// outlives the request that created it. Binding the process to the dispatch
	// ctx (exec.CommandContext(ctx, ...)) SIGKILLs the agent the moment that ctx
	// is canceled — surfacing as "signal: killed" and a falsely failed task even
	// for a trivial task that already did its work.
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	e := NewSubprocessExecutor(writeScript(t, "sleep 0.3; echo ran > "+marker), discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := e.Execute(ctx, Request{TaskID: "t"}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	cancel()               // the dispatch context ends immediately; the agent must keep running
	waitForFile(t, marker) // never appears if cancellation killed the agent mid-run
}

func TestSubprocessExecuteLaunchesAsync(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash agent stub is POSIX-only")
	}
	// Execute launches the agent and returns immediately (like the K8s executor
	// creating a pod); the agent reports its own terminal state over gRPC, so a
	// non-zero exit is NOT surfaced synchronously. Only a failure to START errors.
	dir := t.TempDir()
	e := NewSubprocessExecutor(writeScript(t, "echo ran > "+filepath.Join(dir, "marker")+"; exit 7"), discardLogger())
	if _, err := e.Execute(context.Background(), Request{TaskID: "t"}); err != nil {
		t.Errorf("Execute should return nil once the agent starts, got %v", err)
	}
	waitForFile(t, filepath.Join(dir, "marker")) // proves it actually ran async

	// A binary that cannot start is a synchronous error.
	if _, err := NewSubprocessExecutor("/no/such/agent-binary", discardLogger()).Execute(context.Background(), Request{TaskID: "t"}); err == nil {
		t.Error("an un-startable agent binary should error synchronously")
	}
}
