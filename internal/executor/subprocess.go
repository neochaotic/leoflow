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
	logger    *slog.Logger
}

// NewSubprocessExecutor builds a SubprocessExecutor running the given agent
// binary. It warns that user code runs unsandboxed.
func NewSubprocessExecutor(agentPath string, logger *slog.Logger) *SubprocessExecutor {
	logger.Warn("subprocess executor active; user code runs without isolation. Do NOT use in production")
	return &SubprocessExecutor{agentPath: agentPath, logger: logger}
}

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

// Execute runs the agent subprocess to completion (the agent reports task state
// over gRPC while it runs); the returned error reflects the process outcome.
func (e *SubprocessExecutor) Execute(ctx context.Context, req Request) error {
	cmd := exec.CommandContext(ctx, e.agentPath) //nolint:gosec // dev-only executor running the trusted agent binary
	cmd.Env = append(os.Environ(), agentEnv(req)...)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("agent subprocess for task %s exited: %w", req.TaskID, err)
	}
	return nil
}
