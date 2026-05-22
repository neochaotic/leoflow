// Package scheduler implements the Leoflow scheduling state machine and loop.
package scheduler

import "github.com/neochaotic/leoflow/internal/domain"

// TriggerDecision is the scheduler's decision for a task given its trigger rule
// and the states of its upstream tasks.
type TriggerDecision int

// Possible scheduler decisions for a task.
const (
	// DecisionWait means the upstreams are not yet settled; re-evaluate later.
	DecisionWait TriggerDecision = iota
	// DecisionSchedule means dependencies are satisfied; move the task to scheduled.
	DecisionSchedule
	// DecisionSkip means the trigger rule can no longer be satisfied; skip the task.
	DecisionSkip
	// DecisionUpstreamFailed means a required upstream failed; propagate the failure.
	DecisionUpstreamFailed
)

// CanTransition reports whether a task instance may move from one state to
// another under the Leoflow state machine.
func CanTransition(from, to domain.TaskState) bool {
	return false
}

// CanTransitionDagRun reports whether a dag run may move from one state to another.
func CanTransitionDagRun(from, to domain.DagRunState) bool {
	return false
}

// EvaluateTriggerRule decides what to do with a task given its trigger rule and
// the current states of its upstream tasks.
func EvaluateTriggerRule(rule domain.TriggerRule, upstreams []domain.TaskState) TriggerDecision {
	return DecisionWait
}
