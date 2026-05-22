package scheduler

import "github.com/neochaotic/leoflow/internal/domain"

// PlannedTransition is a decided state change for a task instance within a run.
type PlannedTransition struct {
	TaskID string
	To     domain.TaskState
}

// PlanRun computes the task transitions for one dag run given the DAG topology
// and the current state of each task. It moves runnable tasks none -> scheduled
// (or skipped / upstream_failed per the trigger rule) and scheduled -> queued.
// The result is deterministic: identical inputs yield identical output.
func PlanRun(tasks []domain.TaskSpec, states map[string]domain.TaskState) []PlannedTransition {
	upstreams := make(map[string][]string, len(tasks))
	for _, t := range tasks {
		upstreams[t.TaskID] = t.DependsOn
	}
	out := make([]PlannedTransition, 0, len(tasks))
	for _, t := range tasks {
		switch states[t.TaskID] {
		case domain.TaskStateNone:
			if to, ok := decideStart(t, upstreams[t.TaskID], states); ok {
				out = append(out, PlannedTransition{TaskID: t.TaskID, To: to})
			}
		case domain.TaskStateScheduled:
			out = append(out, PlannedTransition{TaskID: t.TaskID, To: domain.TaskStateQueued})
		default:
			// queued/running/terminal/up_for_retry: nothing to plan here.
		}
	}
	return out
}

func decideStart(t domain.TaskSpec, deps []string, states map[string]domain.TaskState) (domain.TaskState, bool) {
	upstreamStates := make([]domain.TaskState, 0, len(deps))
	for _, dep := range deps {
		upstreamStates = append(upstreamStates, states[dep])
	}
	switch EvaluateTriggerRule(triggerRuleOf(t), upstreamStates) {
	case DecisionSchedule:
		return domain.TaskStateScheduled, true
	case DecisionSkip:
		return domain.TaskStateSkipped, true
	case DecisionUpstreamFailed:
		return domain.TaskStateUpstreamFailed, true
	default:
		return "", false
	}
}

func triggerRuleOf(t domain.TaskSpec) domain.TriggerRule {
	if t.TriggerRule == "" {
		return domain.TriggerRuleAllSuccess
	}
	return t.TriggerRule
}

// FinalizeRun reports the terminal dag-run state once every task is terminal.
// The boolean is false while any task is still non-terminal.
func FinalizeRun(tasks []domain.TaskSpec, states map[string]domain.TaskState) (domain.DagRunState, bool) {
	anyFailed := false
	for _, t := range tasks {
		st := states[t.TaskID]
		if !st.IsTerminal() {
			return "", false
		}
		if st == domain.TaskStateFailed || st == domain.TaskStateUpstreamFailed {
			anyFailed = true
		}
	}
	if anyFailed {
		return domain.DagRunStateFailed, true
	}
	return domain.DagRunStateSuccess, true
}
