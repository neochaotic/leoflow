package scheduler

import "time"

// Dispatch-failure backoff (ADR 0031 Amendment A). A synchronous dispatch
// failure (kube-apiserver unreachable, RBAC denied, quota, admission reject) is
// not retried every tick: the task instance records a backed-off next attempt,
// and after dispatchMaxAttempts the planner fails it as dispatch_failed rather
// than looping forever.
const (
	// dispatchBackoffBase is the delay after the first failed dispatch.
	dispatchBackoffBase = 5 * time.Second
	// dispatchBackoffCap bounds the delay: a permanent misconfiguration is
	// re-attempted no less often than this while it is still within the budget.
	dispatchBackoffCap = 5 * time.Minute
	// dispatchMaxAttempts is how many failed dispatches are tolerated before the
	// task instance is failed as dispatch_failed. With the schedule above this is
	// a few minutes of retrying — long enough to ride out a transient blip, short
	// enough that a permanent failure surfaces rather than looping silently.
	dispatchMaxAttempts = 6
	// infraMaxAttempts bounds the try_number-free re-placement of a task that fails
	// for an infrastructure reason (agent/pod/dispatch lost) — the async analog of
	// dispatchMaxAttempts (ADR 0051 Phase 1). Past this, a task that keeps hitting
	// infra faults fails terminally instead of re-placing forever.
	infraMaxAttempts = 6
)

// dispatchBackoff returns the delay before the next dispatch attempt after
// `attempts` consecutive failures: exponential from the base, capped. It is
// deterministic (no jitter) so the planner's time gate is testable; attempts <= 1
// yields the base.
func dispatchBackoff(attempts int) time.Duration {
	d := dispatchBackoffBase
	for i := 1; i < attempts; i++ {
		d *= 2
		if d >= dispatchBackoffCap {
			return dispatchBackoffCap
		}
	}
	return d
}
