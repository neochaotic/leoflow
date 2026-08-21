package executor

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"
)

// StaleQueuedCandidate is one task instance in `queued` whose dispatch may
// have been lost — typically because the scheduler crashed mid-tick between
// committing the scheduled→queued transition and actually dispatching the TI
// to an executor. The reaper compares the gap from QueuedAt to "now" against
// a dispatch-lost threshold; a non-zero gap larger than the threshold means
// the dispatch is presumed gone and the TI is failed with reason
// `dispatch_lost`. This unblocks the orphan-run reaper, which keeps stuck
// runs out of its candidate set as long as any TI looks active (#202).
type StaleQueuedCandidate struct {
	TaskInstanceID string
	DagRunID       string
	DagID          string
	TaskID         string
	// TryNumber is the attempt the queued row is on, so a best-effort pod
	// delete after the mark targets exactly that attempt's pod (#474).
	TryNumber int
	QueuedAt  time.Time
	// WarmWorkerID is the warm pod durably bound to this attempt (ADR 0058
	// N1d-a2), or "" for a dedicated task or a warm attempt not yet acked. When
	// set AND the worker is in the live warm-pod set, the dispatch-lost reaper
	// DEFERS: the warm worker holds this attempt and is merely slow to transition
	// queued->running, so failing it would double-run the task (review finding
	// H3). A warm attempt has no task pod, so the existing TaskPodActive gate
	// cannot protect it — this warm check is what does.
	WarmWorkerID string
}

// IsDispatchLost reports whether a queued TI has been waiting long enough to
// be declared dispatch-lost. A zero QueuedAt is treated as alive — a TI
// without that stamp is too poorly observed to reap defensively. Future
// timestamps (clock skew) are treated as alive. Mirrors IsAgentLost's
// "do no harm" rule (ADR 0031).
func IsDispatchLost(c StaleQueuedCandidate, threshold time.Duration, now time.Time) bool {
	if c.QueuedAt.IsZero() {
		return false
	}
	return now.Sub(c.QueuedAt) >= threshold
}

// DispatchLostReapStore is the slice of scheduler.Store the dispatch-lost
// reaper needs. The full scheduler.Store embeds this interface so production
// wires through one type; unit tests fake just this surface.
type DispatchLostReapStore interface {
	// ListStaleQueuedCandidates returns every `queued` TI alongside the
	// timestamp it entered the queue. The threshold decision is purely in Go
	// so the SQL stays simple.
	ListStaleQueuedCandidates(ctx context.Context) ([]StaleQueuedCandidate, error)
	// MarkTaskDispatchLost transitions one TI to `failed` with
	// error_message='dispatch_lost'. The WHERE state='queued' guard makes
	// this idempotent: a second call on a now-non-queued TI is a no-op.
	MarkTaskDispatchLost(ctx context.Context, taskInstanceID string) error
}

// dispatchLostReaper is the scheduler-internal worker that fails TIs whose
// dispatch was lost. Invoked once per scheduler tick, leader-only. Mirrors
// the shape of agentLostReaper deliberately so the three reapers share the
// same resilience invariants: panic-safe, per-candidate isolated, metered.
type dispatchLostReaper struct {
	store     DispatchLostReapStore
	logger    *slog.Logger
	threshold time.Duration
	recorder  DecisionRecorder
	// pods makes the reaper K8s-aware (#461): before failing a past-threshold
	// queued TI, it checks whether the TI's pod is actually live (Pending/
	// Running) — a slow image pull on a cold node means the dispatch DID land,
	// so the reaper must DEFER. Nil in Lite: with no pods, the reaper falls
	// back to the pure time-threshold behavior.
	pods PodManager
	// cache is an optional informer-backed presence cache (PR-10) consulted ONLY
	// to DEFER a reap: a cached Pending/Running pod skips the live LIST. A cache
	// miss is never authoritative — the reaper falls through to the live
	// TaskPodActive read, so cache lag can only delay a reap, never cause a
	// false-positive one (#461, the queued path). Nil keeps the live path.
	cache PodPresenceCache
	// warmPods is the live warm-pod seam (ADR 0058 N1d-a2), consulted ONLY to
	// DEFER a reap of a warm-bound queued TI: a warm attempt has no task pod, so
	// TaskPodActive cannot tell a slow queued->running transition from a lost
	// dispatch. Before failing a candidate whose WarmWorkerID is set, the reaper
	// checks whether that worker is still live; if so it defers (the worker holds
	// the attempt — failing it would double-run, review finding H3). Nil (warm
	// off / not wired) yields an empty live set, so the warm check never defers
	// and the dedicated pod-liveness path is byte-for-byte unchanged.
	warmPods WarmPodLister
}

func newDispatchLostReaper(store DispatchLostReapStore, logger *slog.Logger, threshold time.Duration, rec DecisionRecorder) *dispatchLostReaper {
	return &dispatchLostReaper{store: store, logger: logger, threshold: threshold, recorder: rec}
}

// run lists every candidate, fails the stale ones, returns any infra-level
// list error so the caller can log it. Per-TI failures are isolated; a panic
// at any point is recovered so the scheduler tick stays alive.
func (r *dispatchLostReaper) run(ctx context.Context) error {
	defer func() {
		if rec := recover(); rec != nil {
			r.logger.Error("dispatch-lost reaper panic recovered", "panic", rec, "stack", string(debug.Stack()))
			r.record("dispatch_lost_panic")
		}
	}()
	candidates, err := r.store.ListStaleQueuedCandidates(ctx)
	if err != nil {
		return err
	}
	// Live warm-pod set for the H3 defer, read ONCE per tick (Θ(pools)). A nil
	// lister (warm off / not wired) yields an empty set, so the warm defer below
	// never triggers and the dedicated path is unchanged.
	liveWarm := liveWarmPodNames(ctx, r.warmPods, r.logger)
	now := time.Now().UTC()
	for _, c := range candidates {
		if !IsDispatchLost(c, r.threshold, now) {
			continue
		}
		// Warm-liveness gate (ADR 0058 N1d-a2, review finding H3): a warm attempt
		// can sit in `queued` past the threshold while its serving warm worker is
		// alive and merely slow to transition queued->running. A warm attempt has
		// no task pod, so the TaskPodActive gate below cannot protect it; failing
		// it would double-run the task once the live worker does transition it.
		// If this candidate is bound to a warm worker that is still live, DEFER.
		if c.WarmWorkerID != "" && liveWarm[c.WarmWorkerID] {
			r.logger.Info("dispatch-lost: warm worker live; deferring",
				"ti", c.TaskInstanceID, "run", c.DagRunID, "task", c.TaskID, "worker", c.WarmWorkerID)
			r.record("dispatch_lost_warm_deferred")
			continue
		}
		// K8s-aware liveness gate (#461): a TI can sit in `queued` past the
		// threshold while its pod is alive and just slow to pull the image on a
		// cold node. The pure time threshold cannot tell that apart from a truly
		// lost dispatch, so before failing, consult the pod:
		//   * pod Pending/Running  -> the dispatch landed; DEFER (do not reap).
		//   * pod query failed      -> liveness unknown; DEFER ("do no harm").
		//   * no/terminal pod       -> dispatch is genuinely lost; proceed.
		// Nil pods (Lite) has no pod concept, so it falls through to the
		// threshold behavior unchanged.
		//
		// Cache fast-path (PR-10), safe direction only: a cached Pending/Running
		// pod defers without an apiserver read. A cache MISS is NOT trusted — fall
		// through to the live read below, preserving the #461 fix.
		if r.cache != nil && r.cache.CachedPodActive(c.DagRunID, c.TaskID) {
			r.record("dispatch_lost_cache_active")
			continue
		}
		if r.pods != nil {
			active, perr := r.pods.TaskPodActive(ctx, c.DagRunID, c.TaskID)
			if perr != nil {
				r.logger.Warn("dispatch-lost: pod liveness unknown; deferring",
					"ti", c.TaskInstanceID, "run", c.DagRunID, "task", c.TaskID, "error", perr)
				r.record("dispatch_lost_pod_query_error")
				continue
			}
			if active {
				r.logger.Info("dispatch-lost: pod is live (slow start); deferring",
					"ti", c.TaskInstanceID, "run", c.DagRunID, "task", c.TaskID)
				r.record("dispatch_lost_deferred")
				continue
			}
		}
		if ferr := r.store.MarkTaskDispatchLost(ctx, c.TaskInstanceID); ferr != nil {
			r.logger.Error("marking task dispatch-lost",
				"ti", c.TaskInstanceID, "run", c.DagRunID, "dag", c.DagID, "task", c.TaskID, "error", ferr)
			r.record("dispatch_lost_error")
			continue
		}
		r.logger.Warn("task queued past dispatch threshold; failing as dispatch_lost",
			"ti", c.TaskInstanceID, "run", c.DagRunID, "dag", c.DagID, "task", c.TaskID,
			"queued_at", c.QueuedAt)
		r.record("dispatch_lost")
		// Best-effort teardown of any lingering pod for this attempt (#474). By
		// here TaskPodActive said no live pod exists (or pods is nil), so this
		// only cleans up a terminal/failed pod; pinned to (run, task, try).
		if r.pods != nil {
			if derr := r.pods.DeleteTaskPod(ctx, c.DagRunID, c.TaskID, c.TryNumber); derr != nil {
				r.logger.Error("deleting dispatch-lost task pod",
					"ti", c.TaskInstanceID, "run", c.DagRunID, "task", c.TaskID, "try", c.TryNumber, "error", derr)
				r.record("dispatch_lost_pod_delete_error")
			}
		}
	}
	return nil
}

func (r *dispatchLostReaper) record(decision string) {
	if r.recorder != nil {
		r.recorder.RecordSchedulerDecision(decision)
	}
}
