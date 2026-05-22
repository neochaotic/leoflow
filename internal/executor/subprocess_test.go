package executor

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
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

func TestSubprocessExecuteSuccessAndFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash agent stub is POSIX-only")
	}
	ok := NewSubprocessExecutor(writeScript(t, "exit 0"), discardLogger())
	if err := ok.Execute(context.Background(), Request{TaskID: "t"}); err != nil {
		t.Errorf("exit 0 should succeed, got %v", err)
	}
	bad := NewSubprocessExecutor(writeScript(t, "exit 7"), discardLogger())
	if err := bad.Execute(context.Background(), Request{TaskID: "t"}); err == nil {
		t.Error("non-zero exit should fail")
	}
}
