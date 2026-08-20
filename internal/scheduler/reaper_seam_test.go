package scheduler

import (
	"context"
	"testing"
	"time"
)

// spyExecutionReaper records how many times the scheduler drove it, so the seam
// tests can assert the leader-gating contract without depending on the executor
// package. The reaper's own marking behavior is covered by the executor-package
// reaper tests; here we only prove the scheduler invokes it at the right time.
type spyExecutionReaper struct {
	calls int
}

func (s *spyExecutionReaper) ReapOnce(context.Context) error {
	s.calls++
	return nil
}

// TestStepDrivesExecutionReaperOnLeader is the leader half of the reap-gate
// contract (#120/#208), re-expressed at the new seam: a leader tick drives the
// execution reaper exactly once. It replaces the pre-refactor
// TestStepReapsOrphanRunsOnLeader / TestStepRunsDispatchLostReaperOnLeader,
// whose end-to-end marking behavior now lives in the executor package.
func TestStepDrivesExecutionReaperOnLeader(t *testing.T) {
	s := newScheduler(newFakeStore())
	s.SetLeading(true)
	reaper := &spyExecutionReaper{}
	s.SetExecutionReaper(reaper)

	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reaper.calls != 1 {
		t.Errorf("a leader tick must drive the execution reaper once, got %d", reaper.calls)
	}
}

// TestStepDrivesExecutionReaperEvenIfCreateDueRunsFails preserves the backstop
// guard from the pre-refactor TestStepReapsEvenIfCreateDueRunsFails: the reaper
// runs even when the rest of the tick is degraded (a DB hiccup on
// createScheduledRun), because a sick DB hiding orphans exactly when you want to
// see them would be a silent failure mode.
func TestStepDrivesExecutionReaperEvenIfCreateDueRunsFails(t *testing.T) {
	store := newFakeStore()
	last := time.Now().UTC().Add(-2 * time.Hour)
	store.scheduled = []ScheduledDAG{{DagID: "etl", Schedule: "@hourly", LastLogical: &last}}
	store.createErr = true
	s := newScheduler(store)
	s.SetLeading(true)
	reaper := &spyExecutionReaper{}
	s.SetExecutionReaper(reaper)

	_ = s.Step(context.Background())
	if reaper.calls != 1 {
		t.Errorf("the reaper must run even when createScheduledRun fails, got %d calls", reaper.calls)
	}
}

// TestStepSkipsExecutionReaperOnFollower is the follower half: reaping writes
// state and must be single-writer across the fleet, so a follower tick never
// drives the reaper. Replaces the pre-refactor TestStepDoesNotReapOnFollower /
// TestStepSkipsDispatchLostReaperOnFollower.
func TestStepSkipsExecutionReaperOnFollower(t *testing.T) {
	s := newScheduler(newFakeStore())
	s.SetLeading(false) // newScheduler defaults to leader; opt out for follower-mode test.
	reaper := &spyExecutionReaper{}
	s.SetExecutionReaper(reaper)

	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reaper.calls != 0 {
		t.Errorf("a follower must not drive the execution reaper, got %d calls", reaper.calls)
	}
}
