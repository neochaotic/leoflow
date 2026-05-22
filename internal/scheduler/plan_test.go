package scheduler

import (
	"testing"

	"github.com/neochaotic/leoflow/internal/domain"
)

func linear() []domain.TaskSpec {
	return []domain.TaskSpec{
		{TaskID: "a", Type: domain.TaskTypePython},
		{TaskID: "b", Type: domain.TaskTypePython, DependsOn: []string{"a"}},
	}
}

func planMap(tasks []domain.TaskSpec, states map[string]domain.TaskState) map[string]domain.TaskState {
	out := make(map[string]domain.TaskState)
	for _, p := range PlanRun(tasks, states) {
		out[p.TaskID] = p.To
	}
	return out
}

func TestPlanRunSchedulesRootTask(t *testing.T) {
	got := planMap(linear(), map[string]domain.TaskState{"a": domain.TaskStateNone, "b": domain.TaskStateNone})
	if got["a"] != domain.TaskStateScheduled {
		t.Errorf("root a = %q, want scheduled", got["a"])
	}
	if _, ok := got["b"]; ok {
		t.Errorf("b should wait on a, got %q", got["b"])
	}
}

func TestPlanRunPromotesScheduledToQueued(t *testing.T) {
	got := planMap(linear(), map[string]domain.TaskState{"a": domain.TaskStateScheduled})
	if got["a"] != domain.TaskStateQueued {
		t.Errorf("scheduled a = %q, want queued", got["a"])
	}
}

func TestPlanRunSchedulesDownstreamAfterSuccess(t *testing.T) {
	got := planMap(linear(), map[string]domain.TaskState{"a": domain.TaskStateSuccess, "b": domain.TaskStateNone})
	if got["b"] != domain.TaskStateScheduled {
		t.Errorf("b = %q, want scheduled after a success", got["b"])
	}
}

func TestPlanRunPropagatesUpstreamFailure(t *testing.T) {
	got := planMap(linear(), map[string]domain.TaskState{"a": domain.TaskStateFailed, "b": domain.TaskStateNone})
	if got["b"] != domain.TaskStateUpstreamFailed {
		t.Errorf("b = %q, want upstream_failed", got["b"])
	}
}

func TestPlanRunAllDoneSchedulesAfterFailure(t *testing.T) {
	tasks := []domain.TaskSpec{
		{TaskID: "a", Type: domain.TaskTypePython},
		{TaskID: "cleanup", Type: domain.TaskTypePython, DependsOn: []string{"a"}, TriggerRule: domain.TriggerRuleAllDone},
	}
	got := planMap(tasks, map[string]domain.TaskState{"a": domain.TaskStateFailed, "cleanup": domain.TaskStateNone})
	if got["cleanup"] != domain.TaskStateScheduled {
		t.Errorf("all_done cleanup = %q, want scheduled", got["cleanup"])
	}
}

func TestFinalizeRun(t *testing.T) {
	tasks := linear()
	if _, done := FinalizeRun(tasks, map[string]domain.TaskState{"a": domain.TaskStateSuccess, "b": domain.TaskStateRunning}); done {
		t.Error("should not finalize while b is running")
	}
	if st, done := FinalizeRun(tasks, map[string]domain.TaskState{"a": domain.TaskStateSuccess, "b": domain.TaskStateSuccess}); !done || st != domain.DagRunStateSuccess {
		t.Errorf("all success => (%q,%v), want (success,true)", st, done)
	}
	if st, done := FinalizeRun(tasks, map[string]domain.TaskState{"a": domain.TaskStateSuccess, "b": domain.TaskStateFailed}); !done || st != domain.DagRunStateFailed {
		t.Errorf("one failed => (%q,%v), want (failed,true)", st, done)
	}
}
