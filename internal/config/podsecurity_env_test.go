package config_test

import (
	"testing"

	"github.com/neochaotic/leoflow/internal/config"
)

// The task-pod hardening defaults are cluster-operator policy, so they must be
// reachable the way an operator actually configures the control plane: an env
// var on the deployment. viper's AutomaticEnv only binds keys it has already
// seen via SetDefault, so a new field added to the struct without a matching
// entry in serverDefaults is silently unreachable — the struct compiles, the
// env var is ignored, and the escape hatch does not exist. This pins both keys
// against that.
func TestPodSecurityDefaultsBindFromEnv(t *testing.T) {
	t.Setenv("LEOFLOW_EXECUTOR_DEFAULTS_ALLOW_ROOT_TASKS", "true")
	t.Setenv("LEOFLOW_EXECUTOR_DEFAULTS_READ_ONLY_TASK_ROOT_FILESYSTEM", "true")
	c, err := config.LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if !c.Executor.Defaults.AllowRootTasks {
		t.Error("LEOFLOW_EXECUTOR_DEFAULTS_ALLOW_ROOT_TASKS did not bind: the root escape hatch would be unreachable")
	}
	if !c.Executor.Defaults.ReadOnlyTaskRootFilesystem {
		t.Error("LEOFLOW_EXECUTOR_DEFAULTS_READ_ONLY_TASK_ROOT_FILESYSTEM did not bind")
	}
}

// The secure posture must be what an operator gets without configuring anything.
func TestPodSecurityDefaultsAreSecureWhenUnset(t *testing.T) {
	c, err := config.LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if c.Executor.Defaults.AllowRootTasks {
		t.Error("AllowRootTasks must default to false")
	}
	if c.Executor.Defaults.ReadOnlyTaskRootFilesystem {
		t.Error("ReadOnlyTaskRootFilesystem must default to false (restricted does not require it)")
	}
}
