package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// execWaitDelay bounds how long Wait may spend on the two delays os/exec cannot
// otherwise escape: a child that has not exited after its context was canceled,
// and a child that has exited but left its I/O pipes open (#943).
//
// It is a BACKSTOP, not the mechanism. The process-group kill below is what
// normally ends both, and it leaves nothing holding the pipe, so this timer
// fires only for a descendant that escaped the group — one that called setsid,
// or was handed to a system service manager. Without it that descendant would
// hold the wait open for as long as it lives, and the agent's clock would stop
// being the authority on execution_timeout.
//
// The size is set by the pod deadline's shutdown tail: the kubelet must not
// preempt the agent between its own clock firing and its report landing. That
// budget is a pod's termination grace, which defaults to 30 seconds
// (corev1.DefaultTerminationGracePeriodSeconds, the value
// executor.podDeadlineGraceTerm falls back to), so ten seconds here leaves
// twenty for the durable outcome record and one report RPC. It is also far more
// than a healthy drain needs: once every writer is dead the pipe reaches EOF at
// once and only what is already buffered (a pipe's worth, 64 KiB) is left to
// copy. A task whose DAG declares a grace SHORTER than this delay can still be
// preempted by the kubelet in the escaped-descendant case; that corner predates
// this bound and is not made worse by it.
const execWaitDelay = 10 * time.Second

// execRunner is the production CommandRunner: it spawns the user task as a child
// process bound to the supplied context.
//
// waitDelay is a field rather than the constant so a test can bound a wait
// without spending the production delay; every caller outside tests goes through
// NewExecRunner.
type execRunner struct{ waitDelay time.Duration }

// NewExecRunner returns a CommandRunner that executes tasks as child processes.
func NewExecRunner() CommandRunner { return execRunner{waitDelay: execWaitDelay} }

// Run executes argv with env, streaming output to stdout and stderr. A non-zero
// process exit is returned as the exit code with a nil error; only failure to
// start or wait on the process yields an error.
//
// The child leads its OWN process group and the context's cancel kills that
// group, not just the child (#943). This is what makes execution_timeout apply
// to a task rather than to its first process. Two properties combine to defeat
// the default otherwise: the default cancel signals the direct child alone, and
// stdout here is a log writer rather than an *os.File, so the standard library
// interposes a pipe whose copying goroutine Wait blocks on until EVERY writer
// closes it. A surviving grandchild holds that pipe — and the bash operator runs
// its entrypoint under `bash -c`, so any compound command makes the user's real
// programs grandchildren — so the deadline would fire, the child would die, and
// the task would keep running with the agent still blocked in Wait.
//
// Unix-only, deliberately without a build-tagged Windows twin: the agent is
// built for linux and darwin only (.goreleaser.yaml) and runs inside a Linux
// task container.
func (r execRunner) Run(ctx context.Context, argv, env []string, stdout, stderr io.Writer) (int, error) {
	if len(argv) == 0 {
		return -1, errors.New("empty command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // argv is derived from the validated task spec
	cmd.Env = env
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Same signal the default cancel sends, widened to the group: an immediate
	// SIGKILL. Cancel runs only when the context is done AND the process has not
	// already been reaped, so cmd.Process is set and its pid cannot have been
	// recycled by the time the group is addressed.
	cmd.Cancel = func() error { return killProcessGroup(cmd.Process) }
	cmd.WaitDelay = r.waitDelay

	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		// The process itself exited with a SUCCESSFUL status and the delay expired
		// with its pipes still held open by something it left behind; os/exec
		// reports the forced close this way. That is an agent-side I/O outcome,
		// not the task's verdict, so the task keeps the exit status it chose. The
		// cost is the tail of its output, which is why this is logged.
		slog.Warn("task output was truncated: the process exited but left its output pipe open",
			"wait_delay", r.waitDelay)
		if cmd.ProcessState != nil {
			return cmd.ProcessState.ExitCode(), nil
		}
		return 0, nil
	}
	if err != nil {
		return -1, fmt.Errorf("running command: %w", err)
	}
	return 0, nil
}

// killProcessGroup SIGKILLs every process in the group led by p, falling back to
// p alone when no such group exists.
//
// A process group is named by its LEADER's pid, so kill(-pid) reaches nothing at
// all — ESRCH, harming no other group — when the process is not a leader. That
// is the case whenever the Setpgid above did not take effect, and answering it
// with "already done" would leave the child running with the cancel reporting
// success. Falling back to killing the process is what the default cancel does,
// so the worst case degrades to the old behavior rather than to no behavior.
// p.Kill reports an already-exited process as os.ErrProcessDone, which os/exec
// treats as nothing to do rather than as a failure of the run.
func killProcessGroup(p *os.Process) error {
	err := syscall.Kill(-p.Pid, syscall.SIGKILL)
	if err == nil {
		return nil
	}
	if !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("killing process group %d: %w", p.Pid, err)
	}
	return p.Kill()
}
