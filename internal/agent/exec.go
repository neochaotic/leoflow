package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
)

// execRunner is the production CommandRunner: it spawns the user task as a child
// process bound to the supplied context.
type execRunner struct{}

// NewExecRunner returns a CommandRunner that executes tasks as child processes.
func NewExecRunner() CommandRunner { return execRunner{} }

// Run executes argv with env, streaming output to stdout and stderr. A non-zero
// process exit is returned as the exit code with a nil error; only failure to
// start or wait on the process yields an error.
func (execRunner) Run(ctx context.Context, argv, env []string, stdout, stderr io.Writer) (int, error) {
	if len(argv) == 0 {
		return -1, errors.New("empty command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // argv is derived from the validated task spec
	cmd.Env = env
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	if err != nil {
		return -1, fmt.Errorf("running command: %w", err)
	}
	return 0, nil
}
