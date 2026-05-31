package scheduler

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
	QueuedAt       time.Time
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
	recorder  Recorder
}

func newDispatchLostReaper(store DispatchLostReapStore, logger *slog.Logger, threshold time.Duration, rec Recorder) *dispatchLostReaper {
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
	now := time.Now().UTC()
	for _, c := range candidates {
		if !IsDispatchLost(c, r.threshold, now) {
			continue
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
	}
	return nil
}

func (r *dispatchLostReaper) record(decision string) {
	if r.recorder != nil {
		r.recorder.RecordSchedulerDecision(decision)
	}
}
