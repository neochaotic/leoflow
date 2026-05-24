// Package agent contains the worker-side logic that runs inside the task
// container: building the user process command, injecting XCom inputs, reading
// the return value, and retry backoff. The gRPC client lives in cmd/leoflow-agent.
package agent

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// ReturnValuePath is where the Python helper writes a task's return value.
const ReturnValuePath = "/tmp/leoflow_return_value.json"

const maxRetryAttempts = 5

// BuildCommand returns the argv to execute the user's task for the given
// operator. http_api tasks are executed by the control plane, not the agent.
func BuildCommand(operator, entrypoint string) ([]string, error) {
	switch operator {
	case "python":
		module, fn, ok := strings.Cut(entrypoint, ":")
		if !ok || module == "" || fn == "" {
			return nil, fmt.Errorf("python entrypoint must be module:callable, got %q", entrypoint)
		}
		// Delegate to the leoflow_runtime helper, which runs the callable and
		// writes its return value for the agent to push as an XCom. The interpreter
		// is configurable (LEOFLOW_PYTHON) because task images standardize on
		// "python" while a dev host may only have "python3".
		return []string{pythonInterpreter(), "-m", "leoflow_runtime", entrypoint}, nil
	case "bash":
		if entrypoint == "" {
			return nil, errors.New("bash operator requires a command")
		}
		return []string{"bash", "-c", entrypoint}, nil
	case "http_api":
		return nil, errors.New("http_api is executed by the control plane, not the agent")
	default:
		return nil, fmt.Errorf("unsupported operator %q", operator)
	}
}

// pythonInterpreter returns the Python executable the agent runs, from
// LEOFLOW_PYTHON when set, defaulting to "python" (the task-image convention).
func pythonInterpreter() string {
	if p := os.Getenv("LEOFLOW_PYTHON"); p != "" {
		return p
	}
	return "python"
}

// XComEnvVar formats an XCom input as a LEOFLOW_XCOM_<NAME>=<json> env entry.
func XComEnvVar(name string, value []byte) string {
	return "LEOFLOW_XCOM_" + strings.ToUpper(name) + "=" + string(value)
}

// ReadReturnValue reads the optional return-value file. ok is false (no error)
// when the file does not exist.
func ReadReturnValue(path string) (value []byte, ok bool, err error) {
	data, rerr := os.ReadFile(path) //nolint:gosec // path is the fixed helper output location
	if errors.Is(rerr, os.ErrNotExist) {
		return nil, false, nil
	}
	if rerr != nil {
		return nil, false, fmt.Errorf("reading return value: %w", rerr)
	}
	return data, true, nil
}

// Backoff returns the delay before retry attempt n (1-based: 1s, 2s, 4s, 8s,
// 16s). ok is false once the maximum number of attempts is exceeded.
func Backoff(attempt int) (delay time.Duration, ok bool) {
	if attempt < 1 || attempt > maxRetryAttempts {
		return 0, false
	}
	return (1 << (attempt - 1)) * time.Second, true
}
