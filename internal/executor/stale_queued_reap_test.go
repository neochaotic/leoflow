package executor

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestIsDispatchLost: the pure decision returns true iff the gap between the
// candidate's queued_at and now reaches the threshold. A zero QueuedAt is
// treated as alive — defensive: a TI without a queued_at stamp is too poorly
// observed to reap. Future timestamps (clock skew) are alive.
//
// Mirrors IsAgentLost's "do no harm" shape (ADR 0031): the reaper requires a
// positive observable signal (queued_at older than threshold) before failing
// the TI as `dispatch_lost`.
func TestIsDispatchLost(t *testing.T) {
	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	const threshold = 3 * time.Minute

	tests := []struct {
		name string
		qed  time.Time
		want bool
	}{
		{"fresh queue is alive", now.Add(-1 * time.Second), false},
		{"under threshold is alive", now.Add(-2 * time.Minute), false},
		{"exactly at threshold is lost", now.Add(-3 * time.Minute), true},
		{"well past threshold is lost", now.Add(-30 * time.Minute), true},
		{"future queued (clock skew) is alive", now.Add(1 * time.Second), false},
		{"zero queued_at is alive (out of scope)", time.Time{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsDispatchLost(StaleQueuedCandidate{QueuedAt: tc.qed}, threshold, now)
			if got != tc.want {
				t.Errorf("IsDispatchLost(queued=%v) = %v, want %v", tc.qed, got, tc.want)
			}
		})
	}
}

// fakeStaleQueuedStore is the minimal store the reaper needs in unit tests.
type fakeStaleQueuedStore struct {
	candidates []StaleQueuedCandidate
	listErr    error
	failed     []string
	failErr    error
}

func (f *fakeStaleQueuedStore) ListStaleQueuedCandidates(context.Context) ([]StaleQueuedCandidate, error) {
	return f.candidates, f.listErr
}

func (f *fakeStaleQueuedStore) MarkTaskDispatchLost(_ context.Context, tiID string) error {
	if f.failErr != nil {
		return f.failErr
	}
	f.failed = append(f.failed, tiID)
	return nil
}

// TestReapDispatchLost_MarksStaleTIs covers the success path: only TIs queued
// past the threshold get failed; freshly-queued ones are left alone. This is
// the contract that makes "stuck dag_run because a scheduler crashed
// mid-dispatch" recoverable (issue #202): once these TIs are failed, the
// no-active-TI guard on the orphan-run reaper passes and the run gets cleaned
// up on the next tick (two-step recovery via two backstops).
func TestReapDispatchLost_MarksStaleTIs(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeStaleQueuedStore{candidates: []StaleQueuedCandidate{
		{TaskInstanceID: "fresh-ti", QueuedAt: now.Add(-10 * time.Second)},
		{TaskInstanceID: "stuck-ti", QueuedAt: now.Add(-10 * time.Minute)},
	}}
	rec := &capturingRecorder{}
	r := newDispatchLostReaper(store, reapTestLogger(), 3*time.Minute, rec)

	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run err = %v", err)
	}
	if len(store.failed) != 1 || store.failed[0] != "stuck-ti" {
		t.Errorf("failed = %v, want [stuck-ti]", store.failed)
	}
	if got := rec.count("dispatch_lost"); got != 1 {
		t.Errorf("dispatch_lost decisions = %d, want 1", got)
	}
}

// TestReapDispatchLost_ListErrorSurfaces: a list failure is returned so the
// caller can log it. Must never panic.
func TestReapDispatchLost_ListErrorSurfaces(t *testing.T) {
	r := newDispatchLostReaper(&fakeStaleQueuedStore{listErr: errors.New("db down")},
		reapTestLogger(), 3*time.Minute, nil)
	if err := r.run(context.Background()); err == nil {
		t.Error("expected list error to be returned")
	}
}

// TestReapDispatchLost_PerTIErrorIsolated: a failure on one TI does not stall
// the rest — same isolation pattern as the agent-lost and orphan reapers.
func TestReapDispatchLost_PerTIErrorIsolated(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeStaleQueuedStore{
		candidates: []StaleQueuedCandidate{
			{TaskInstanceID: "a", QueuedAt: now.Add(-10 * time.Minute)},
			{TaskInstanceID: "b", QueuedAt: now.Add(-10 * time.Minute)},
		},
		failErr: errors.New("write failed"),
	}
	r := newDispatchLostReaper(store, reapTestLogger(), 3*time.Minute, nil)
	if err := r.run(context.Background()); err != nil {
		t.Errorf("run err = %v, want nil (per-TI errors isolated)", err)
	}
}
