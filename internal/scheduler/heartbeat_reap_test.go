package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestIsAgentLost: the pure decision returns true iff the gap between the
// candidate's last heartbeat and now reaches the threshold. A zero
// LastHeartbeat (never reported) is treated as still alive — the TI may be
// inline (no agent) or fresh — so this reaper only fires on TIs that *did*
// heartbeat at least once and then went silent. The "do no harm" rule
// (ADR 0031): a positive observable signal is required.
func TestIsAgentLost(t *testing.T) {
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	const threshold = 90 * time.Second

	tests := []struct {
		name string
		last time.Time
		want bool
	}{
		{"fresh heartbeat is alive", now.Add(-1 * time.Second), false},
		{"exactly at threshold is lost", now.Add(-90 * time.Second), true},
		{"well past threshold is lost", now.Add(-10 * time.Minute), true},
		{"future heartbeat (clock skew) is alive", now.Add(1 * time.Second), false},
		{"zero heartbeat is alive (never reported, out of scope)", time.Time{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsAgentLost(AgentLostCandidate{LastHeartbeat: tc.last}, threshold, now)
			if got != tc.want {
				t.Errorf("IsAgentLost(last=%v) = %v, want %v", tc.last, got, tc.want)
			}
		})
	}
}

// fakeHeartbeatStore is the minimal store the reaper needs in unit tests.
type fakeHeartbeatStore struct {
	candidates []AgentLostCandidate
	listErr    error
	failed     []string
	failErr    error
}

func (f *fakeHeartbeatStore) ListAgentLostCandidates(context.Context) ([]AgentLostCandidate, error) {
	return f.candidates, f.listErr
}

func (f *fakeHeartbeatStore) MarkTaskAgentLost(_ context.Context, tiID string) error {
	if f.failErr != nil {
		return f.failErr
	}
	f.failed = append(f.failed, tiID)
	return nil
}

// TestReapAgentLost_MarksStaleTIs covers the success path: only candidates
// older than the threshold get failed; fresh ones are left alone. This is
// the contract that makes "stuck dag_run because one TI's agent died"
// recoverable without manual intervention.
func TestReapAgentLost_MarksStaleTIs(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeHeartbeatStore{candidates: []AgentLostCandidate{
		{TaskInstanceID: "fresh-ti", LastHeartbeat: now.Add(-1 * time.Second)},
		{TaskInstanceID: "stale-ti", LastHeartbeat: now.Add(-5 * time.Minute)},
	}}
	rec := &capturingRecorder{}
	r := newAgentLostReaper(store, reapTestLogger(), 90*time.Second, rec)

	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run err = %v", err)
	}
	if len(store.failed) != 1 || store.failed[0] != "stale-ti" {
		t.Errorf("failed = %v, want [stale-ti]", store.failed)
	}
	if got := rec.count("agent_lost"); got != 1 {
		t.Errorf("agent_lost decisions = %d, want 1", got)
	}
}

// TestReapAgentLost_ListErrorSurfaces: a list failure is returned so the
// caller can log it (the scheduler's reapHeartbeatLossesIfLeader treats this
// as a "next tick will try again" condition). It must NEVER panic.
func TestReapAgentLost_ListErrorSurfaces(t *testing.T) {
	r := newAgentLostReaper(&fakeHeartbeatStore{listErr: errors.New("db down")},
		reapTestLogger(), 90*time.Second, nil)
	if err := r.run(context.Background()); err == nil {
		t.Error("expected list error to be returned")
	}
}

// TestReapAgentLost_PerTIErrorIsolated: a failure on one TI does not stall
// the rest of the candidate set — same isolation pattern as the run reaper
// and advanceSafely.
func TestReapAgentLost_PerTIErrorIsolated(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeHeartbeatStore{
		candidates: []AgentLostCandidate{
			{TaskInstanceID: "a", LastHeartbeat: now.Add(-5 * time.Minute)},
			{TaskInstanceID: "b", LastHeartbeat: now.Add(-5 * time.Minute)},
		},
		failErr: errors.New("write failed"),
	}
	r := newAgentLostReaper(store, reapTestLogger(), 90*time.Second, nil)
	if err := r.run(context.Background()); err != nil {
		t.Errorf("run err = %v, want nil (per-TI errors isolated)", err)
	}
}

// panicHeartbeatStore is the resilience pin — any panic in the store layer
// must be recovered. The scheduler must never die.
type panicHeartbeatStore struct {
	panicOnList bool
	panicOnFail bool
}

func (p *panicHeartbeatStore) ListAgentLostCandidates(context.Context) ([]AgentLostCandidate, error) {
	if p.panicOnList {
		panic("boom: ListAgentLostCandidates")
	}
	return []AgentLostCandidate{{TaskInstanceID: "doomed", LastHeartbeat: time.Now().Add(-1 * time.Hour)}}, nil
}
func (p *panicHeartbeatStore) MarkTaskAgentLost(context.Context, string) error {
	if p.panicOnFail {
		panic("boom: MarkTaskAgentLost")
	}
	return nil
}

func TestReapAgentLost_PanicInListDoesNotCrash(t *testing.T) {
	r := newAgentLostReaper(&panicHeartbeatStore{panicOnList: true}, reapTestLogger(), 90*time.Second, nil)
	if err := r.run(context.Background()); err != nil {
		t.Errorf("panic in list must be recovered; got error %v", err)
	}
}

func TestReapAgentLost_PanicInMarkDoesNotCrash(t *testing.T) {
	r := newAgentLostReaper(&panicHeartbeatStore{panicOnFail: true}, reapTestLogger(), 90*time.Second, nil)
	if err := r.run(context.Background()); err != nil {
		t.Errorf("panic in MarkTaskAgentLost must be recovered; got error %v", err)
	}
}
