package scheduler

import (
	"testing"
	"time"

	"github.com/neochaotic/leoflow/internal/domain"
)

func linear() []domain.TaskSpec {
	return []domain.TaskSpec{
		{TaskID: "a", Type: domain.TaskTypePython},
		{TaskID: "b", Type: domain.TaskTypePython, DependsOn: []string{"a"}},
	}
}

func planMap(run RunState) map[string]domain.TaskState {
	out := make(map[string]domain.TaskState)
	for _, p := range PlanRun(run) {
		out[p.TaskID] = p.To
	}
	return out
}

func st(tasks []domain.TaskSpec, states map[string]domain.TaskState) RunState {
	return RunState{Tasks: tasks, States: states}
}

func TestPlanRunSchedulesRootTask(t *testing.T) {
	got := planMap(st(linear(), map[string]domain.TaskState{"a": domain.TaskStateNone, "b": domain.TaskStateNone}))
	if got["a"] != domain.TaskStateScheduled {
		t.Errorf("root a = %q, want scheduled", got["a"])
	}
	if _, ok := got["b"]; ok {
		t.Errorf("b should wait on a, got %q", got["b"])
	}
}

func TestPlanRunPromotesScheduledToQueued(t *testing.T) {
	got := planMap(st(linear(), map[string]domain.TaskState{"a": domain.TaskStateScheduled}))
	if got["a"] != domain.TaskStateQueued {
		t.Errorf("scheduled a = %q, want queued", got["a"])
	}
}

func TestPlanRunSchedulesDownstreamAfterSuccess(t *testing.T) {
	got := planMap(st(linear(), map[string]domain.TaskState{"a": domain.TaskStateSuccess, "b": domain.TaskStateNone}))
	if got["b"] != domain.TaskStateScheduled {
		t.Errorf("b = %q, want scheduled after a success", got["b"])
	}
}

func TestPlanRunPropagatesUpstreamFailure(t *testing.T) {
	got := planMap(st(linear(), map[string]domain.TaskState{"a": domain.TaskStateFailed, "b": domain.TaskStateNone}))
	if got["b"] != domain.TaskStateUpstreamFailed {
		t.Errorf("b = %q, want upstream_failed", got["b"])
	}
}

func TestPlanRunAllDoneSchedulesAfterFailure(t *testing.T) {
	tasks := []domain.TaskSpec{
		{TaskID: "a", Type: domain.TaskTypePython},
		{TaskID: "cleanup", Type: domain.TaskTypePython, DependsOn: []string{"a"}, TriggerRule: domain.TriggerRuleAllDone},
	}
	got := planMap(st(tasks, map[string]domain.TaskState{"a": domain.TaskStateFailed, "cleanup": domain.TaskStateNone}))
	if got["cleanup"] != domain.TaskStateScheduled {
		t.Errorf("all_done cleanup = %q, want scheduled", got["cleanup"])
	}
}

func TestPlanRunRetriesFailedTaskWithBudget(t *testing.T) {
	run := RunState{
		Tasks:    linear(),
		States:   map[string]domain.TaskState{"a": domain.TaskStateFailed, "b": domain.TaskStateNone},
		Tries:    map[string]int{"a": 1},
		MaxTries: map[string]int{"a": 3},
	}
	got := planMap(run)
	if got["a"] != domain.TaskStateUpForRetry {
		t.Errorf("retriable a = %q, want up_for_retry", got["a"])
	}
	if _, ok := got["b"]; ok {
		t.Errorf("b must wait while a is retriable, got %q", got["b"])
	}
}

func TestPlanRunNoRetryWhenExhausted(t *testing.T) {
	run := RunState{
		Tasks:    linear(),
		States:   map[string]domain.TaskState{"a": domain.TaskStateFailed, "b": domain.TaskStateNone},
		Tries:    map[string]int{"a": 3},
		MaxTries: map[string]int{"a": 3},
	}
	got := planMap(run)
	if _, ok := got["a"]; ok {
		t.Errorf("exhausted a must not retry, got %q", got["a"])
	}
	if got["b"] != domain.TaskStateUpstreamFailed {
		t.Errorf("b = %q, want upstream_failed once a is terminally failed", got["b"])
	}
}

func TestPlanRunResetsUpForRetry(t *testing.T) {
	run := RunState{
		Tasks:    linear(),
		States:   map[string]domain.TaskState{"a": domain.TaskStateUpForRetry, "b": domain.TaskStateNone},
		Tries:    map[string]int{"a": 2},
		MaxTries: map[string]int{"a": 3},
	}
	if got := planMap(run); got["a"] != domain.TaskStateNone {
		t.Errorf("up_for_retry a = %q, want none (reset)", got["a"])
	}
}

func TestFinalizeRun(t *testing.T) {
	tasks := linear()
	if _, done := FinalizeRun(st(tasks, map[string]domain.TaskState{"a": domain.TaskStateSuccess, "b": domain.TaskStateRunning})); done {
		t.Error("should not finalize while b is running")
	}
	if state, done := FinalizeRun(st(tasks, map[string]domain.TaskState{"a": domain.TaskStateSuccess, "b": domain.TaskStateSuccess})); !done || state != domain.DagRunStateSuccess {
		t.Errorf("all success => (%q,%v), want (success,true)", state, done)
	}
	if state, done := FinalizeRun(st(tasks, map[string]domain.TaskState{"a": domain.TaskStateSuccess, "b": domain.TaskStateFailed})); !done || state != domain.DagRunStateFailed {
		t.Errorf("one failed => (%q,%v), want (failed,true)", state, done)
	}
}

// TestPlanRunHoldsUpForRetryDuringDelay pins issue #201: a task in up_for_retry
// must NOT be released back to `none` until `retry_delay_seconds` have elapsed
// since `ended_at`. Without this gate, retries fire on the next tick (~1s),
// defeating the user-declared backoff that exists precisely to give transient
// failure causes (rate limits, upstream blips) time to recover.
func TestPlanRunHoldsUpForRetryDuringDelay(t *testing.T) {
	endedAt := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	run := RunState{
		Tasks:             linear(),
		States:            map[string]domain.TaskState{"a": domain.TaskStateUpForRetry, "b": domain.TaskStateNone},
		Tries:             map[string]int{"a": 2},
		MaxTries:          map[string]int{"a": 3},
		EndedAt:           map[string]*time.Time{"a": &endedAt},
		RetryDelaySeconds: map[string]int{"a": 60}, // 1 minute cooldown
		Now:               endedAt.Add(10 * time.Second),
	}
	got := planMap(run)
	if _, ok := got["a"]; ok {
		t.Errorf("a planned %q at 10s after failure; should remain up_for_retry until +60s", got["a"])
	}
}

// TestPlanRunReleasesUpForRetryAfterDelay completes the contract: the same
// task IS released to `none` (and then to `scheduled` via decideStart in the
// same plan pass) once the cooldown has elapsed.
func TestPlanRunReleasesUpForRetryAfterDelay(t *testing.T) {
	endedAt := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	run := RunState{
		Tasks:             linear(),
		States:            map[string]domain.TaskState{"a": domain.TaskStateUpForRetry, "b": domain.TaskStateNone},
		Tries:             map[string]int{"a": 2},
		MaxTries:          map[string]int{"a": 3},
		EndedAt:           map[string]*time.Time{"a": &endedAt},
		RetryDelaySeconds: map[string]int{"a": 60},
		Now:               endedAt.Add(61 * time.Second),
	}
	got := planMap(run)
	// up_for_retry → none in this tick; subsequent tick handles none → scheduled
	// (the existing TestPlanRunResetsUpForRetry pins the same single-step shape).
	if got["a"] != domain.TaskStateNone {
		t.Errorf("a = %q at 61s after failure; want none (cooldown elapsed, ready to reset)", got["a"])
	}
}

// TestPlanRunHoldsUpForRescheduleUntilTime: a reschedule-mode sensor parked in
// up_for_reschedule is NOT re-dispatched until reschedule_at passes, and its
// downstream waits meanwhile (ADR 0040 Phase B, #380).
func TestPlanRunHoldsUpForRescheduleUntilTime(t *testing.T) {
	at := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	run := RunState{
		Tasks:        linear(),
		States:       map[string]domain.TaskState{"a": domain.TaskStateUpForReschedule, "b": domain.TaskStateNone},
		RescheduleAt: map[string]*time.Time{"a": &at},
		Now:          at.Add(-30 * time.Second),
	}
	got := planMap(run)
	if _, ok := got["a"]; ok {
		t.Errorf("a planned %q before reschedule_at; should stay up_for_reschedule", got["a"])
	}
	if _, ok := got["b"]; ok {
		t.Errorf("downstream b planned %q while a is up_for_reschedule; should wait", got["b"])
	}
}

// TestPlanRunReleasesUpForRescheduleAfterTime: once reschedule_at passes, the task
// returns to none for re-dispatch (next tick does none → scheduled), the same
// single-step shape as the retry release.
func TestPlanRunReleasesUpForRescheduleAfterTime(t *testing.T) {
	at := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	run := RunState{
		Tasks:        linear(),
		States:       map[string]domain.TaskState{"a": domain.TaskStateUpForReschedule, "b": domain.TaskStateNone},
		RescheduleAt: map[string]*time.Time{"a": &at},
		Now:          at.Add(time.Second),
	}
	if got := planMap(run); got["a"] != domain.TaskStateNone {
		t.Errorf("a = %q after reschedule_at; want none (ready to re-dispatch)", got["a"])
	}
}

// TestPlanRunRescheduleIgnoresRetryBudget: reschedule is not a retry — re-dispatch
// happens even with the retry budget exhausted (try_number is preserved, no budget
// consumed; #380). This is the key difference from the up_for_retry rail.
func TestPlanRunRescheduleIgnoresRetryBudget(t *testing.T) {
	at := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	run := RunState{
		Tasks:        linear(),
		States:       map[string]domain.TaskState{"a": domain.TaskStateUpForReschedule, "b": domain.TaskStateNone},
		Tries:        map[string]int{"a": 3},
		MaxTries:     map[string]int{"a": 3}, // exhausted — must not block a reschedule
		RescheduleAt: map[string]*time.Time{"a": &at},
		Now:          at.Add(time.Second),
	}
	if got := planMap(run); got["a"] != domain.TaskStateNone {
		t.Errorf("a = %q with retry budget exhausted; reschedule must ignore budget", got["a"])
	}
}

// TestPlanRunBackwardsCompatNoRetryDelay confirms that when retry_delay_seconds
// is 0 or absent (legacy behavior), the task transitions immediately — no
// regression for DAGs that opted out of backoff.
func TestPlanRunBackwardsCompatNoRetryDelay(t *testing.T) {
	run := RunState{
		Tasks:             linear(),
		States:            map[string]domain.TaskState{"a": domain.TaskStateUpForRetry, "b": domain.TaskStateNone},
		Tries:             map[string]int{"a": 2},
		MaxTries:          map[string]int{"a": 3},
		RetryDelaySeconds: map[string]int{"a": 0}, // no delay
		// EndedAt absent, Now zero — neither matters when delay == 0
	}
	if got := planMap(run); got["a"] != domain.TaskStateNone {
		t.Errorf("a = %q with no retry_delay; want none (immediate reset, same as existing behavior)", got["a"])
	}
}

func TestFinalizeRunWaitsForRetriableFailure(t *testing.T) {
	run := RunState{
		Tasks:    linear(),
		States:   map[string]domain.TaskState{"a": domain.TaskStateFailed, "b": domain.TaskStateSuccess},
		Tries:    map[string]int{"a": 1},
		MaxTries: map[string]int{"a": 3},
	}
	if _, done := FinalizeRun(run); done {
		t.Error("must not finalize the run while a failed task can still retry")
	}
}

// A scheduled task whose next_dispatch_at is in the future is NOT promoted to
// queued this tick — its previous dispatch failed and the backoff has not
// elapsed (ADR 0031 Amendment A). Mirrors the up_for_reschedule gate.
func TestPlanRunDefersScheduledDuringDispatchBackoff(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	future := now.Add(30 * time.Second)
	run := RunState{
		Tasks:          linear(),
		States:         map[string]domain.TaskState{"a": domain.TaskStateScheduled},
		Now:            now,
		NextDispatchAt: map[string]*time.Time{"a": &future},
	}
	if got := planMap(run)["a"]; got == domain.TaskStateQueued {
		t.Error("a scheduled task still in dispatch backoff must not be promoted to queued")
	}
}

// Once next_dispatch_at has passed, the scheduled task is promoted to queued so
// the dispatch is re-attempted.
func TestPlanRunRedispatchesAfterBackoffElapses(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	past := now.Add(-1 * time.Second)
	run := RunState{
		Tasks:          linear(),
		States:         map[string]domain.TaskState{"a": domain.TaskStateScheduled},
		Now:            now,
		NextDispatchAt: map[string]*time.Time{"a": &past},
	}
	if got := planMap(run)["a"]; got != domain.TaskStateQueued {
		t.Errorf("a scheduled task past its backoff should be queued, got %s", got)
	}
}

// Absent next_dispatch_at (the common case: never failed to dispatch) promotes
// immediately, preserving today's behavior.
func TestPlanRunSchedulesImmediatelyWithoutBackoff(t *testing.T) {
	run := RunState{
		Tasks:  linear(),
		States: map[string]domain.TaskState{"a": domain.TaskStateScheduled},
	}
	if got := planMap(run)["a"]; got != domain.TaskStateQueued {
		t.Errorf("a scheduled task with no backoff should be queued, got %s", got)
	}
}
