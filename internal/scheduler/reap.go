package scheduler

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"
)

// ReapCandidate is one running dag run the reaper is considering, with the
// timestamp of its most recent observable activity (max of the run's started_at
// and its task instances' started_at / ended_at). The reaper compares the gap
// from this stamp to "now" against a stall threshold; a non-zero gap larger
// than the threshold means the run is orphaned and should be failed.
type ReapCandidate struct {
	RunID        string
	DagID        string
	LastActivity time.Time
}

// IsOrphaned reports whether a running run has been quiet long enough to be
// declared orphaned. A zero LastActivity (no progress signal at all) counts as
// orphaned: a running run with no recorded activity since at least its
// started_at is, by definition, a run nothing is touching. Future timestamps
// (clock skew) are treated as fresh — the reaper is a backstop, not a clock
// arbiter, so it errs on the side of leaving recent-looking runs alone.
func IsOrphaned(c ReapCandidate, threshold time.Duration, now time.Time) bool {
	if c.LastActivity.IsZero() {
		return true
	}
	return now.Sub(c.LastActivity) >= threshold
}

// ReapStore is the slice of scheduler.Store the reaper needs. The full
// scheduler.Store embeds this interface so production wires through one type;
// the unit tests fake just this surface.
type ReapStore interface {
	// ListReapCandidates returns every dag_run currently in 'running' state
	// alongside its last-activity timestamp. The query is the authority on what
	// "running" means and how to compute the timestamp; the reaper only decides
	// whether each one has been quiet for too long.
	ListReapCandidates(ctx context.Context) ([]ReapCandidate, error)
	// ReapRun transitions a run to 'failed' with an "orphaned" note and fails
	// any still-active task instances. It is idempotent: a second call on the
	// same run is a no-op.
	ReapRun(ctx context.Context, runID string) error
}

// orphanReaper is the scheduler-internal worker that fails dag runs whose
// last observed activity is older than threshold. It is invoked once per
// scheduler tick, only on the leader.
type orphanReaper struct {
	store     ReapStore
	logger    *slog.Logger
	threshold time.Duration
	recorder  Recorder
}

func newOrphanReaper(store ReapStore, logger *slog.Logger, threshold time.Duration, rec Recorder) *orphanReaper {
	return &orphanReaper{store: store, logger: logger, threshold: threshold, recorder: rec}
}

// run lists every candidate, reaps the stale ones, and returns any infra-level
// list error so the caller can log it. Per-run reap failures are isolated:
// they are logged and metered but never abort the loop, so one poison run
// never blocks the others (same pattern as advanceSafely).
func (r *orphanReaper) run(ctx context.Context) error {
	defer func() {
		if rec := recover(); rec != nil {
			r.logger.Error("orphan reaper panic recovered", "panic", rec, "stack", string(debug.Stack()))
			r.record("orphan_panic")
		}
	}()
	candidates, err := r.store.ListReapCandidates(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, c := range candidates {
		if !IsOrphaned(c, r.threshold, now) {
			continue
		}
		if rerr := r.store.ReapRun(ctx, c.RunID); rerr != nil {
			r.logger.Error("reaping orphan run", "run", c.RunID, "dag", c.DagID, "error", rerr)
			r.record("orphan_reap_error")
			continue
		}
		r.logger.Warn("reaped orphan run", "run", c.RunID, "dag", c.DagID, "last_activity", c.LastActivity)
		r.record("orphan_reaped")
	}
	return nil
}

func (r *orphanReaper) record(decision string) {
	if r.recorder != nil {
		r.recorder.RecordSchedulerDecision(decision)
	}
}
