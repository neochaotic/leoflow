package executor

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"
)

// PodLostCandidate is one task instance in `running` whose backing pod may have
// vanished — deleted, evicted, OOM-killed, or lost with its node — before any
// other reaper could catch it. RunningSince is when the TI entered `running`;
// the reaper only checks pod liveness once the TI has been running past a grace
// period, so a just-dispatched TI whose pod is still materializing is never
// reaped on a transient "no pod yet".
type PodLostCandidate struct {
	TaskInstanceID string
	DagRunID       string
	DagID          string
	TaskID         string
	// TryNumber pins the best-effort pod delete to exactly this attempt, so a
	// retry's newer pod is never touched (same invariant as the #474 teardown).
	TryNumber    int
	RunningSince time.Time
}

// IsPodLostCandidate reports whether a running TI has been running long enough
// to warrant a pod-liveness check. A zero RunningSince is treated as alive (too
// poorly observed to reap — the "do no harm" rule of ADR 0031), and a future
// RunningSince (clock skew) is treated as alive. This gate is purely about
// elapsed time; the actual lost-vs-alive decision is the pod-liveness check.
func IsPodLostCandidate(c PodLostCandidate, grace time.Duration, now time.Time) bool {
	if c.RunningSince.IsZero() {
		return false
	}
	return now.Sub(c.RunningSince) >= grace
}

// PodLostReapStore is the slice of scheduler.Store the pod-lost reaper needs.
// The full scheduler.Store embeds this interface so production wires through one
// type; unit tests fake just this surface.
type PodLostReapStore interface {
	// ListRunningTasks returns every `running` TI with the timestamp it entered
	// running, so the reaper applies the grace period in Go and the SQL stays
	// simple.
	ListRunningTasks(ctx context.Context) ([]PodLostCandidate, error)
	// MarkTaskPodLost transitions one TI to `failed` with
	// error_message='pod_lost'. The WHERE state='running' guard makes this
	// idempotent. It returns whether a row was actually updated: false means a
	// late terminal report transitioned the TI between the list and this write,
	// so the caller must NOT treat it as reaped (no false log, no pod delete).
	MarkTaskPodLost(ctx context.Context, taskInstanceID string) (bool, error)
}

// podLostReaper fails `running` TIs whose pod has vanished — the gap between the
// agent-lost reaper (which skips a TI that never heartbeated, the ADR 0031
// zero-guard) and the reconciler (which only sees pods that still exist), so a
// pod killed before its first heartbeat used to sit `running` until the 5-minute
// orphan reaper failed the whole run (#527). Invoked once per scheduler tick,
// leader-only. Mirrors the other reapers' resilience: panic-safe, per-candidate
// isolated, metered.
//
// It is Kubernetes-ONLY. With no PodManager (Lite/subprocess) there is no pod to
// lose and a subprocess task legitimately has none, so the reaper is a no-op —
// it must never reap live subprocess work on a "no pod" signal.
type podLostReaper struct {
	store    PodLostReapStore
	logger   *slog.Logger
	grace    time.Duration
	recorder DecisionRecorder
	// pods is required for this reaper to do anything; nil (Lite) makes run a
	// no-op. See the type doc.
	pods PodManager
	// cache is an optional informer-backed presence cache (PR-10) consulted ONLY
	// to DEFER a reap: a cached Pending/Running pod skips the live LIST. A cache
	// miss is never authoritative — the reaper falls through to the live
	// TaskPodActive read below, so cache lag can only delay a reap, never cause a
	// false-positive one (#461). Nil keeps every candidate on the live path.
	cache PodPresenceCache
}

func newPodLostReaper(store PodLostReapStore, logger *slog.Logger, grace time.Duration, rec DecisionRecorder) *podLostReaper {
	return &podLostReaper{store: store, logger: logger, grace: grace, recorder: rec}
}

// run lists every running TI, checks pod liveness for the ones past the grace
// period, and fails those with no live pod as pod_lost. Per-TI failures are
// isolated; a panic anywhere is recovered so the scheduler tick stays alive.
func (r *podLostReaper) run(ctx context.Context) error {
	defer func() {
		if rec := recover(); rec != nil {
			r.logger.Error("pod-lost reaper panic recovered", "panic", rec, "stack", string(debug.Stack()))
			r.record("pod_lost_panic")
		}
	}()
	// K8s-only: without pods there is nothing to lose, and reaping a Lite
	// subprocess task on "no pod" would kill live work. Skip entirely.
	if r.pods == nil {
		return nil
	}
	candidates, err := r.store.ListRunningTasks(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, c := range candidates {
		if !IsPodLostCandidate(c, r.grace, now) {
			continue
		}
		// Cache fast-path (PR-10), safe direction only: a cached Pending/Running
		// pod defers the reap without an apiserver read. A cache MISS is NOT
		// trusted — fall through to the live read below, preserving the #461 fix.
		if r.cache != nil && r.cache.CachedPodActive(c.DagRunID, c.TaskID) {
			r.record("pod_lost_cache_active")
			continue
		}
		// Consult the pod before failing:
		//   * query failed     -> liveness unknown; DEFER ("do no harm").
		//   * pod Pending/Running -> a pod exists; not lost. Silence, if any, is
		//                            the agent-lost reaper's job.
		//   * no live pod       -> the pod is genuinely gone; fail as pod_lost.
		active, perr := r.pods.TaskPodActive(ctx, c.DagRunID, c.TaskID)
		if perr != nil {
			r.logger.Warn("pod-lost: pod liveness unknown; deferring",
				"ti", c.TaskInstanceID, "run", c.DagRunID, "task", c.TaskID, "error", perr)
			r.record("pod_lost_pod_query_error")
			continue
		}
		if active {
			continue
		}
		applied, ferr := r.store.MarkTaskPodLost(ctx, c.TaskInstanceID)
		if ferr != nil {
			r.logger.Error("marking task pod-lost",
				"ti", c.TaskInstanceID, "run", c.DagRunID, "dag", c.DagID, "task", c.TaskID, "error", ferr)
			r.record("pod_lost_error")
			continue
		}
		if !applied {
			// A late terminal report transitioned the TI between our list and our
			// write (WHERE state='running' matched 0 rows) — it settled on its own,
			// so do not log a false reap or run the teardown.
			r.record("pod_lost_noop")
			continue
		}
		r.logger.Warn("running task has no live pod past grace; failing as pod_lost",
			"ti", c.TaskInstanceID, "run", c.DagRunID, "dag", c.DagID, "task", c.TaskID, "running_since", c.RunningSince)
		r.record("pod_lost")
		// Best-effort teardown pinned to (run, task, try). TaskPodActive said no
		// live pod, so this only sweeps a lingering terminal pod at most; a
		// retry's newer pod has a different try-number label and is never touched.
		if derr := r.pods.DeleteTaskPod(ctx, c.DagRunID, c.TaskID, c.TryNumber); derr != nil {
			r.logger.Error("deleting pod-lost task pod",
				"ti", c.TaskInstanceID, "run", c.DagRunID, "task", c.TaskID, "try", c.TryNumber, "error", derr)
			r.record("pod_lost_pod_delete_error")
		}
	}
	return nil
}

func (r *podLostReaper) record(decision string) {
	if r.recorder != nil {
		r.recorder.RecordSchedulerDecision(decision)
	}
}
