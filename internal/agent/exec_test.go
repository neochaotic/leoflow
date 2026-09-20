package agent

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
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

	// Signal 0 asks "does this process exist" without touching it, and that is
	// NOT enough on its own: it answers yes for a zombie too.
	//
	// That distinction is the whole difference between the two halves of this
	// fix, and it is why this assertion nearly shipped meaning nothing. Run
	// under `go test` the orphan reparents to a real init that reaps it, so
	// kill(pid, 0) starts failing and a test that only checked liveness passed.
	// Run as PID 1, which is production (runtime/Dockerfile ENTRYPOINT is the
	// agent), the corpse stays in the table as a zombie, kill(pid, 0) keeps
	// succeeding, and the same assertion fails. Measured: it did.
	//
	// So the state is read rather than inferred where the kernel exposes it.
	alive := true
	for range 40 {
		if !processPresent(pid) {
			alive = false
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if alive {
		_ = syscall.Kill(pid, syscall.SIGKILL) // never leave the test's own mess behind
		t.Fatalf("grandchild %d outlived the task and was not collected; on a warm worker it would run into the next attempt holding that attempt's environment (#1216)", pid)
	}
}

// TestCleanTaskLogsNoReapWarning pins that the reap is silent when there was
// nothing to reap.
//
// The first version of it warned on EVERY successful task and stayed silent on
// the only case it exists to report. After cmd.Run the child is already
// collected, so kill(-pgid) answers ESRCH and the fallback answers
// ErrProcessDone; treating those as failures inverted the signal. Measured:
//
//	no survivor: err="os: process already finished"   (warned, wrongly)
//	a survivor:  err=<nil>                            (silent, wrongly)
//
// A permanent false alarm in every agent log is worse than no log line at all,
// because it trains whoever reads it to ignore the one that matters.
func TestCleanTaskLogsNoReapWarning(t *testing.T) {
	var logged bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	var out, errb bytes.Buffer
	code, err := NewExecRunner().Run(context.Background(),
		[]string{"sh", "-c", "echo fine"}, nil, &out, &errb)
	if err != nil || code != 0 {
		t.Fatalf("the fixture task must succeed: code=%d err=%v", code, err)
	}

	if strings.Contains(logged.String(), "could not reap") {
		t.Fatalf("a clean task warned about a failed reap; this fires on every successful task and trains the reader to ignore it:\n%s", logged.String())
	}
}

// processPresent reports whether a pid is still a RUNNING process, treating a
// zombie as absent.
//
// A zombie executes no code and holds no memory, so for the isolation claim it
// is gone. It is not gone for the pid table, which is what the drain is about,
// but conflating the two is what let the first version of this test pass
// everywhere except production.
//
// On Linux the state comes from /proc. The third field of /proc/<pid>/stat is
// the state character, read after the comm field, which is parenthesised and
// may itself contain spaces and parentheses: splitting on whitespace from the
// left is the classic way to get this wrong, so the scan starts after the LAST
// ')'. Elsewhere (the maintainer's macOS) there is no /proc and signal 0 is all
// there is; the assertion is weaker there and that is stated rather than
// hidden.
func processPresent(pid int) bool {
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		s := string(b)
		if i := strings.LastIndex(s, ")"); i >= 0 && i+2 < len(s) {
			return s[i+2] != 'Z'
		}
	}
	return syscall.Kill(pid, 0) == nil
}
