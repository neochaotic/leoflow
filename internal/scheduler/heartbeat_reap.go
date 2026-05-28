package scheduler

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"
)

// AgentLostCandidate is one task instance in `running` whose agent may have
// gone silent, with the timestamp of its most recent heartbeat. The reaper
// compares the gap from this stamp to "now" against a stall threshold; a
// non-zero gap larger than the threshold means the agent is presumed gone
// and the TI is failed with reason "agent_lost".
type AgentLostCandidate struct {
	TaskInstanceID string
	DagRunID       string
	DagID          string
	TaskID         string
	LastHeartbeat  time.Time
}

// IsAgentLost reports whether the agent has been silent long enough to be
// declared lost. A zero LastHeartbeat (never reported) is treated as alive,
// not lost — the TI may be inline (no agent ever exists), or simply has not
// completed its first interval yet. The reaper only fires on TIs that did
// heartbeat at least once and then went silent; this is the "do no harm"
// rule of ADR 0031. Future timestamps (clock skew) are treated as alive.
func IsAgentLost(c AgentLostCandidate, threshold time.Duration, now time.Time) bool {
	if c.LastHeartbeat.IsZero() {
		return false
	}
	return now.Sub(c.LastHeartbeat) >= threshold
}

// HeartbeatReapStore is the slice of scheduler.Store the TI heartbeat reaper
// needs. The full scheduler.Store embeds this interface so production wires
// through one type; unit tests fake just this surface.
type HeartbeatReapStore interface {
	// ListAgentLostCandidates returns every `running` TI whose last heartbeat
	// is non-null (it has heartbeated at least once). The reaper applies the
	// threshold per candidate so the SQL stays simple and the decision is
	// purely in Go.
	ListAgentLostCandidates(ctx context.Context) ([]AgentLostCandidate, error)
	// MarkTaskAgentLost transitions one TI to `failed` with
	// error_message='agent_lost'. The WHERE state='running' guard makes this
	// idempotent: a second call on a now-failed TI is a no-op.
	MarkTaskAgentLost(ctx context.Context, taskInstanceID string) error
}

// agentLostReaper is the scheduler-internal worker that fails TIs whose agent
// went silent. Invoked once per scheduler tick, leader-only. Mirrors the
// shape of orphanReaper deliberately so the two reapers share the same
// resilience invariants: panic-safe, per-candidate isolated, metered.
type agentLostReaper struct {
	store     HeartbeatReapStore
	logger    *slog.Logger
	threshold time.Duration
	recorder  Recorder
}

func newAgentLostReaper(store HeartbeatReapStore, logger *slog.Logger, threshold time.Duration, rec Recorder) *agentLostReaper {
	return &agentLostReaper{store: store, logger: logger, threshold: threshold, recorder: rec}
}

// run lists every candidate, fails the stale ones, returns any infra-level
// list error so the caller can log it. Per-TI failures are isolated; a panic
// at any point is recovered so the scheduler tick stays alive.
func (r *agentLostReaper) run(ctx context.Context) error {
	defer func() {
		if rec := recover(); rec != nil {
			r.logger.Error("agent-lost reaper panic recovered", "panic", rec, "stack", string(debug.Stack()))
			r.record("agent_lost_panic")
		}
	}()
	candidates, err := r.store.ListAgentLostCandidates(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, c := range candidates {
		if !IsAgentLost(c, r.threshold, now) {
			continue
		}
		if ferr := r.store.MarkTaskAgentLost(ctx, c.TaskInstanceID); ferr != nil {
			r.logger.Error("marking task agent-lost",
				"ti", c.TaskInstanceID, "run", c.DagRunID, "dag", c.DagID, "task", c.TaskID, "error", ferr)
			r.record("agent_lost_error")
			continue
		}
		r.logger.Warn("task agent silent past threshold; failing as agent_lost",
			"ti", c.TaskInstanceID, "run", c.DagRunID, "dag", c.DagID, "task", c.TaskID,
			"last_heartbeat", c.LastHeartbeat)
		r.record("agent_lost")
	}
	return nil
}

func (r *agentLostReaper) record(decision string) {
	if r.recorder != nil {
		r.recorder.RecordSchedulerDecision(decision)
	}
}
