package agent

import (
	"context"
	"os"
	"strings"
	"testing"

	agentv1 "github.com/neochaotic/leoflow/proto/agent/v1"
)

// TestNewReturnValuePathUnique: each task gets its own return-value path under a
// fresh temp dir (no shared /tmp/leoflow_return_value.json), and cleanup removes it.
func TestNewReturnValuePathUnique(t *testing.T) {
	p1, c1, err := NewReturnValuePath()
	if err != nil {
		t.Fatalf("NewReturnValuePath: %v", err)
	}
	defer func() { _ = c1() }()
	p2, c2, err := NewReturnValuePath()
	if err != nil {
		t.Fatalf("NewReturnValuePath: %v", err)
	}
	if p1 == p2 {
		t.Errorf("two tasks must get distinct paths, both %q", p1)
	}
	if strings.HasPrefix(p1, "/tmp/leoflow_return_value.json") {
		t.Errorf("must not use the shared global path, got %q", p1)
	}
	if err := c2(); err != nil {
		t.Errorf("cleanup: %v", err)
	}
	if _, serr := os.Stat(p2); !os.IsNotExist(serr) {
		t.Errorf("cleanup must remove the return-value dir, stat err = %v", serr)
	}
}

// TestBuildEnvInjectsReturnValuePath: the runner tells the runtime where to write
// the return value (the agent's per-task path), and injects nothing when there is
// no return path.
func TestBuildEnvInjectsReturnValuePath(t *testing.T) {
	r := newRunner(&fakeClient{}, &fakeCmd{}, &recordingSink{})
	r.ReturnPath = "/run/task-42/return_value.json"
	env, err := r.buildEnv(context.Background(), &agentv1.TaskSpec{})
	if err != nil {
		t.Fatalf("buildEnv: %v", err)
	}
	if !contains(env, "LEOFLOW_RETURN_VALUE_PATH=/run/task-42/return_value.json") {
		t.Errorf("buildEnv must inject the per-task return path, got %v", env)
	}

	r.ReturnPath = ""
	env2, err := r.buildEnv(context.Background(), &agentv1.TaskSpec{})
	if err != nil {
		t.Fatalf("buildEnv: %v", err)
	}
	for _, kv := range env2 {
		if strings.HasPrefix(kv, "LEOFLOW_RETURN_VALUE_PATH=") {
			t.Errorf("must not inject a return path when none is set, got %q", kv)
		}
	}
}

func contains(env []string, want string) bool {
	for _, kv := range env {
		if kv == want {
			return true
		}
	}
	return false
}

// TestBuildEnvInjectsOperatorArgs: an airflow_operator task's constructor kwargs
// (ADR 0040) flow to the runtime as LEOFLOW_OPERATOR_ARGS, and nothing is injected
// when the operator has no args.
func TestBuildEnvInjectsOperatorArgs(t *testing.T) {
	r := newRunner(&fakeClient{}, &fakeCmd{}, &recordingSink{})
	env, err := r.buildEnv(context.Background(), &agentv1.TaskSpec{OperatorArgsJson: `{"bash_command":"echo hi"}`})
	if err != nil {
		t.Fatalf("buildEnv: %v", err)
	}
	if !contains(env, `LEOFLOW_OPERATOR_ARGS={"bash_command":"echo hi"}`) {
		t.Errorf("buildEnv must inject LEOFLOW_OPERATOR_ARGS, got %v", env)
	}

	env2, err := r.buildEnv(context.Background(), &agentv1.TaskSpec{})
	if err != nil {
		t.Fatalf("buildEnv: %v", err)
	}
	for _, kv := range env2 {
		if strings.HasPrefix(kv, "LEOFLOW_OPERATOR_ARGS=") {
			t.Errorf("must not inject operator args when none set, got %q", kv)
		}
	}
}
