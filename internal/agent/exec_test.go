package agent

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestExecRunnerCapturesOutputAndExitCode(t *testing.T) {
	var out, errb bytes.Buffer
	code, err := NewExecRunner().Run(context.Background(),
		[]string{"sh", "-c", "echo hello; echo oops 1>&2; exit 3"}, nil, &out, &errb)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
	if !strings.Contains(out.String(), "hello") {
		t.Errorf("stdout = %q, want hello", out.String())
	}
	if !strings.Contains(errb.String(), "oops") {
		t.Errorf("stderr = %q, want oops", errb.String())
	}
}

func TestExecRunnerRejectsEmptyCommand(t *testing.T) {
	if _, err := NewExecRunner().Run(context.Background(), nil, nil, nil, nil); err == nil {
		t.Error("empty command should error")
	}
}

func TestExecRunnerErrorsOnMissingBinary(t *testing.T) {
	if _, err := NewExecRunner().Run(context.Background(),
		[]string{"leoflow-no-such-binary-xyz"}, nil, nil, nil); err == nil {
		t.Error("missing binary should error")
	}
}
