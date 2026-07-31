package scheduler

import (
	"time"

	"github.com/neochaotic/leoflow/internal/domain"
)

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

	out = append(out, planRetryTransitions(run, effective, decided)...)

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
			// A previous dispatch may have failed; hold off re-dispatch until the
			// backoff elapses (ADR 0031 Amendment A), mirroring the reschedule gate.
			if !readyToDispatch(run, t.TaskID) {
				continue
			}
			out = append(out, PlannedTransition{TaskID: t.TaskID, To: domain.TaskStateQueued})
		default:
			// queued/running/terminal/up_for_retry: nothing to plan here.
		}
	}
	return out
}

// planRetryTransitions handles the retry/reschedule rail: a failed task with
// budget moves to up_for_retry; an up_for_retry or up_for_reschedule task resets
// to none once its cooldown/poke time elapses. It records the effective state and
// marks each handled task decided so the main loop leaves it alone, and returns
// the transitions to emit.
func planRetryTransitions(run RunState, effective map[string]domain.TaskState, decided map[string]bool) []PlannedTransition {
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
			if !readyToRetry(run, t.TaskID) {
				decided[t.TaskID] = true
				continue
			}
			out = append(out, PlannedTransition{TaskID: t.TaskID, To: domain.TaskStateNone})
			effective[t.TaskID] = domain.TaskStateNone
			decided[t.TaskID] = true
		case domain.TaskStateUpForReschedule:
			// Re-dispatch once reschedule_at passes, WITHOUT consuming retry budget
			// (reschedule is not a failure); until then keep it parked so downstream
			// waits. Mirrors the up_for_retry rail, gated on reschedule_at (#380).
			if !readyToReschedule(run, t.TaskID) {
				decided[t.TaskID] = true
				continue
			}
			out = append(out, PlannedTransition{TaskID: t.TaskID, To: domain.TaskStateNone})
			effective[t.TaskID] = domain.TaskStateNone
			decided[t.TaskID] = true
		default:
			// none/scheduled/queued/running/terminal: no retry decision here.
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

// readyToRetry reports whether the cooldown window from the user's declared
// retry_delay_seconds has elapsed since the task ended. The check honors the
// "absent data falls back to immediate retry" convention so legacy callers
// (tests, in-flight DAGs predating issue #201) keep working unchanged.
//
// Returns true when:
//   - delay is 0 (no cooldown declared), OR
//   - the task's EndedAt is not recorded (can't compute, retry immediately), OR
//   - run.Now is zero (no clock provided — test seam preserves old behavior), OR
//   - run.Now >= EndedAt + delay (cooldown has elapsed)
func readyToRetry(run RunState, taskID string) bool {
	delay := run.RetryDelaySeconds[taskID]
	if delay <= 0 {
		return true
	}
	ended := run.EndedAt[taskID]
	if ended == nil {
		return true
	}
	if run.Now.IsZero() {
		return true
	}
	return !run.Now.Before(ended.Add(time.Duration(delay) * time.Second))
}

// readyToDispatch reports whether a `scheduled` task may be dispatched now: true
// unless a prior synchronous dispatch failure set next_dispatch_at in the future.
// Honors the "absent data / zero clock falls back to immediate" convention
// (mirroring readyToReschedule), so the common case (never failed) and tests that
// do not populate NextDispatchAt/Now dispatch immediately.
func readyToDispatch(run RunState, taskID string) bool {
	at := run.NextDispatchAt[taskID]
	if at == nil {
		return true
	}
	if run.Now.IsZero() {
		return true
	}
	return !run.Now.Before(*at)
}

// readyToReschedule reports whether a task parked in up_for_reschedule may be
// re-dispatched: true when reschedule_at has passed. It honors the "absent data /
// zero clock falls back to immediate" convention (mirroring readyToRetry) so tests
// and callers that don't populate RescheduleAt/Now keep the simplest behavior.
//
// Returns true when:
//   - the task has no recorded reschedule_at (re-dispatch now), OR
//   - run.Now is zero (no clock provided — test seam), OR
//   - run.Now >= reschedule_at.
func readyToReschedule(run RunState, taskID string) bool {
	at := run.RescheduleAt[taskID]
	if at == nil {
		return true
	}
	if run.Now.IsZero() {
		return true
	}
	return !run.Now.Before(*at)
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
