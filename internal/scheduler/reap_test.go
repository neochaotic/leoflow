package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

func reapTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestIsOrphaned: the pure decision function returns true iff the gap between
// the candidate's last activity and now reaches the threshold. This is the
// single rule the reaper applies — keep it deterministic and table-driven so
// future tweaks (e.g. tighter threshold) cannot regress it.
func TestIsOrphaned(t *testing.T) {
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	const threshold = 5 * time.Minute

	tests := []struct {
		name string
		last time.Time
		want bool
	}{
		{"fresh run is not orphaned", now.Add(-1 * time.Minute), false},
		{"exactly at threshold is orphaned", now.Add(-5 * time.Minute), true},
		{"well past threshold is orphaned", now.Add(-1 * time.Hour), true},
		{"future timestamp (clock skew) is not orphaned", now.Add(1 * time.Minute), false},
		{"zero last-activity is orphaned (no signal == stale)", time.Time{}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsOrphaned(ReapCandidate{LastActivity: tc.last}, threshold, now)
			if got != tc.want {
				t.Errorf("IsOrphaned(last=%v) = %v, want %v", tc.last, got, tc.want)
			}
		})
	}
}

// fakeReapStore is the minimal store the reaper needs in unit tests: it records
// every ReapRun call so the test can assert on which candidates were reaped.
type fakeReapStore struct {
	candidates []ReapCandidate
	listErr    error
	reaped     []string
	reapErr    error
}

func (f *fakeReapStore) ListReapCandidates(_ context.Context) ([]ReapCandidate, error) {
	return f.candidates, f.listErr
}

func (f *fakeReapStore) ReapRun(_ context.Context, runID string) error {
	if f.reapErr != nil {
		return f.reapErr
	}
	f.reaped = append(f.reaped, runID)
	return nil
}

// TestReapOrphans_MarksStaleRuns: only candidates older than the threshold are
// reaped; fresh ones are left alone. This is the contract the user expects on
// the dashboard counter — the gauge drops once a stale run is reaped, never for
// a freshly-started run.
func TestReapOrphans_MarksStaleRuns(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeReapStore{candidates: []ReapCandidate{
		{RunID: "fresh", LastActivity: now.Add(-1 * time.Minute)},
		{RunID: "stale", LastActivity: now.Add(-1 * time.Hour)},
	}}
	rec := &capturingRecorder{}
	r := newOrphanReaper(store, reapTestLogger(), 5*time.Minute, rec)

	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run err = %v", err)
	}
	if len(store.reaped) != 1 || store.reaped[0] != "stale" {
		t.Errorf("reaped = %v, want [stale]", store.reaped)
	}
	if got := rec.count("orphan_reaped"); got != 1 {
		t.Errorf("orphan_reaped decisions = %d, want 1", got)
	}
}

// TestReapOrphans_ListErrorDoesNotPanic: a list failure is logged and surfaced
// as the run error so the tick keeps going on the next interval. The reaper
// must never panic — it is the backstop that brings stuck runs back, and a dead
// reaper means stuck runs stay stuck forever.
func TestReapOrphans_ListErrorDoesNotPanic(t *testing.T) {
	store := &fakeReapStore{listErr: errors.New("db down")}
	r := newOrphanReaper(store, reapTestLogger(), 5*time.Minute, nil)
	if err := r.run(context.Background()); err == nil {
		t.Error("expected the list error to be returned so the caller can log it")
	}
}

// TestReapOrphans_ReapErrorIsPerRunIsolated: a failure reaping one run does not
// block reaping the others — the tick is isolated per candidate just like the
// main scheduler step (advanceSafely pattern).
func TestReapOrphans_ReapErrorIsPerRunIsolated(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeReapStore{
		candidates: []ReapCandidate{
			{RunID: "first-stale", LastActivity: now.Add(-1 * time.Hour)},
			{RunID: "second-stale", LastActivity: now.Add(-2 * time.Hour)},
		},
		reapErr: errors.New("write failed"),
	}
	r := newOrphanReaper(store, reapTestLogger(), 5*time.Minute, nil)
	// Even with reapErr, run() must return nil so the scheduler loop keeps
	// ticking; the per-run failure is recorded as a metric and logged.
	if err := r.run(context.Background()); err != nil {
		t.Errorf("run err = %v, want nil (per-run errors isolated)", err)
	}
}
