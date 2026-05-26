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
	for i := 0; i < 100; i++ {
		if data, err := os.ReadFile(path); err == nil {
			return data
		}
		time.Sleep(20 * time.Millisecond)
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
	if err := e.Execute(context.Background(), Request{TaskID: "t"}); err != nil {
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
	if err := e.Execute(ctx, Request{TaskID: "t"}); err != nil {
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
	if err := e.Execute(context.Background(), Request{TaskID: "t"}); err != nil {
		t.Errorf("Execute should return nil once the agent starts, got %v", err)
	}
	waitForFile(t, filepath.Join(dir, "marker")) // proves it actually ran async

	// A binary that cannot start is a synchronous error.
	if err := NewSubprocessExecutor("/no/such/agent-binary", discardLogger()).Execute(context.Background(), Request{TaskID: "t"}); err == nil {
		t.Error("an un-startable agent binary should error synchronously")
	}
}
