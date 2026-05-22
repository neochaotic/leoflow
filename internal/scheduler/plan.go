package scheduler

import "github.com/neochaotic/leoflow/internal/domain"

// PlannedTransition is a decided state change for a task instance within a run.
type PlannedTransition struct {
	TaskID string
	To     domain.TaskState
}

// PlanRun computes the task transitions for one dag run. It first handles
// retries — a failed task with retry budget moves to up_for_retry, and an
// up_for_retry task resets (none, try_number+1) — then plans the rest off the
// resulting effective states: none -> scheduled (or skipped / upstream_failed
// per the trigger rule) and scheduled -> queued. A retriable failed task is
// treated as still active, so downstream tasks wait rather than seeing a
// failure. The result is deterministic: identical inputs yield identical output.
func PlanRun(run RunState) []PlannedTransition {
	upstreams := make(map[string][]string, len(run.Tasks))
	for _, t := range run.Tasks {
		upstreams[t.TaskID] = t.DependsOn
	}

	// Effective states fold pending retries in so downstream planning sees a
	// retriable failure as active rather than terminal.
	effective := make(map[string]domain.TaskState, len(run.States))
	for k, v := range run.States {
		effective[k] = v
	}
	decided := make(map[string]bool, len(run.Tasks))
	out := make([]PlannedTransition, 0, len(run.Tasks))

	for _, t := range run.Tasks {
		switch run.States[t.TaskID] {
		case domain.TaskStateFailed:
			if retriable(run, t.TaskID) {
				out = append(out, PlannedTransition{TaskID: t.TaskID, To: domain.TaskStateUpForRetry})
				effective[t.TaskID] = domain.TaskStateUpForRetry
				decided[t.TaskID] = true
			}
		case domain.TaskStateUpForRetry:
			out = append(out, PlannedTransition{TaskID: t.TaskID, To: domain.TaskStateNone})
			effective[t.TaskID] = domain.TaskStateNone
			decided[t.TaskID] = true
		default:
			// none/scheduled/queued/running/terminal: no retry decision here.
		}
	}

	for _, t := range run.Tasks {
		if decided[t.TaskID] {
			continue
		}
		switch effective[t.TaskID] {
		case domain.TaskStateNone:
			if to, ok := decideStart(t, upstreams[t.TaskID], effective); ok {
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

// retriable reports whether a failed task still has retry budget (the current
// try number is below its max). Absent budget data it is false, so tasks fail
// terminally by default.
func retriable(run RunState, taskID string) bool {
	return run.Tries[taskID] < run.MaxTries[taskID]
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
// A failed task that still has retry budget counts as non-terminal, so the run
// keeps running until the retry resolves. The boolean is false while any task is
// still non-terminal.
func FinalizeRun(run RunState) (domain.DagRunState, bool) {
	anyFailed := false
	for _, t := range run.Tasks {
		st := run.States[t.TaskID]
		if st == domain.TaskStateFailed && retriable(run, t.TaskID) {
			return "", false
		}
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
