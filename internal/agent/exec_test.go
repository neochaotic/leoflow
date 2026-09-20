package agent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
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

// TestRunReapsSurvivingGrandchildren is the regression test for the warm-pool
// half of #1216.
//
// cmd.Cancel, which kills the process group, is invoked by os/exec only when the
// CONTEXT ends. A task that simply exits left its group untouched, so a
// grandchild that outlived its parent kept running. Under pod-per-task that is
// invisible, because the agent exits and the container takes everything with it.
// Under warm pools the container is reused attempt after attempt, so the
// survivor crosses into the next task and holds the memory that task was sized
// for.
//
// The shape here is the one the codebase already worries about elsewhere: a
// shell that backgrounds something and exits, leaving a grandchild.
func TestRunReapsSurvivingGrandchildren(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the agent is built for linux and darwin only")
	}
	marker := filepath.Join(t.TempDir(), "survivor.pid")

	// sh exits immediately; the backgrounded sleep is the grandchild, and it
	// writes its pid so the test can ask the kernel about it by name rather than
	// inferring from timing.
	script := "sh -c 'echo $$ > " + marker + "; exec sleep 30' & exit 0"

	var out, errb bytes.Buffer
	code, err := execRunner{waitDelay: time.Second}.Run(
		context.Background(), []string{"sh", "-c", script}, nil, &out, &errb)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if code != 0 {
		t.Fatalf("the shell exited %d; this fixture needs it to succeed so the test is about the SURVIVOR, not the failure", code)
	}

	raw, err := os.ReadFile(marker)
	if err != nil {
		t.Skipf("the grandchild never recorded its pid (%v); nothing to assert about", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		t.Skipf("unusable pid %q", strings.TrimSpace(string(raw)))
	}

	// Signal 0 asks "does this process exist" without touching it. Give the
	// kernel a moment to finish reaping before concluding.
	alive := true
	for range 20 {
		if err := syscall.Kill(pid, 0); err != nil {
			alive = false
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if alive {
		_ = syscall.Kill(pid, syscall.SIGKILL) // never leave the test's own mess behind
		t.Fatalf("grandchild %d outlived the task; on a warm worker it would run into the next attempt holding that attempt's memory (#1216)", pid)
	}
}
