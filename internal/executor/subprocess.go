package executor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
)

// SubprocessExecutor runs the agent as a host subprocess with no isolation. It
// is for dev mode only and logs a prominent warning on construction.
type SubprocessExecutor struct {
	agentPath string
	workDir   string
	logger    *slog.Logger
}

// NewSubprocessExecutor builds a SubprocessExecutor running the given agent
// binary. It warns that user code runs unsandboxed.
func NewSubprocessExecutor(agentPath string, logger *slog.Logger) *SubprocessExecutor {
	logger.Warn("subprocess executor active; user code runs without isolation. Do NOT use in production")
	return &SubprocessExecutor{agentPath: agentPath, logger: logger}
}

// SetWorkDir sets the working directory the agent runs in. In a task pod the
// image's WORKDIR holds the DAG code; on a dev host `leoflow dev` points this at
// the project directory so the agent can import the user's dag.py. Empty keeps
// the parent process's working directory.
func (e *SubprocessExecutor) SetWorkDir(dir string) { e.workDir = dir }

// agentEnv builds the environment injected into the agent process.
func agentEnv(req Request) []string {
	env := make([]string, 0, 3+len(req.Env))
	env = append(env,
		"LEOFLOW_CONTROL_PLANE_ADDR="+req.ControlPlaneAddr,
		"LEOFLOW_AGENT_TOKEN="+req.AgentToken,
		"LEOFLOW_TASK_INSTANCE_ID="+req.TaskInstanceID,
	)
	for k, v := range req.Env {
		env = append(env, k+"="+v)
	}
	return env
}

// Execute launches the agent subprocess and returns once it has started, like
// the Kubernetes executor creating a pod. The agent reports its own task state
// over gRPC, so the scheduler can record the task as queued before the agent
// finishes; running it synchronously here would let the agent report success
// before the scheduler recorded queued, and the queued write would clobber it.
// A non-zero exit is therefore NOT a synchronous error; only a failure to start
// is. The process is reaped in the background.
func (e *SubprocessExecutor) Execute(ctx context.Context, req Request) error {
	// WithoutCancel detaches the agent from the dispatch context: like a pod, it
	// must run to completion and report its own terminal state over gRPC. Binding
	// it to ctx would SIGKILL the agent when the dispatch ctx is canceled (surfacing
	// as "signal: killed" and a falsely failed task), mirroring the inline runner.
	cmd := exec.CommandContext(context.WithoutCancel(ctx), e.agentPath) //nolint:gosec // dev-only executor running the trusted agent binary
	cmd.Env = append(os.Environ(), agentEnv(req)...)
	cmd.Dir = e.workDir
	// Surface the agent's own diagnostics (it logs to stderr); otherwise an agent
	// that fails to start or connect fails silently. The task's stdout/stderr are
	// shipped separately over gRPC.
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting agent subprocess for task %s: %w", req.TaskID, err)
	}
	go func() {
		if werr := cmd.Wait(); werr != nil {
			e.logger.Warn("agent subprocess exited non-zero (the agent reports task state over gRPC)",
				"task", req.TaskID, "error", werr)
		}
	}()
	return nil
}
