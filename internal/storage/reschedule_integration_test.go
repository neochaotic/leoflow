//go:build integration

// Reschedule-mode sensor re-dispatch, end to end at the SQL layer (#380). The
// unit tests cover each layer with fakes; this proves the real DB round-trip the
// fakes cannot: the agent's Reschedule write parks the TI with reschedule_at, the
// scheduler loads it back into RunState, the planner releases it, and the
// re-dispatch returns it to none WITHOUT consuming a retry attempt.
//
// See [[tests-must-be-reality-anchored]] memory. Skips without DATABASE_URL.

package storage_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/neochaotic/leoflow/internal/auth"
	"github.com/neochaotic/leoflow/internal/domain"
	"github.com/neochaotic/leoflow/internal/scheduler"
	"github.com/neochaotic/leoflow/internal/storage"
)

func TestRescheduleRedispatchPreservesTryNumberIntegration(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)

	dagID := fmt.Sprintf("reschedule_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{{TaskID: "probe", Type: domain.TaskTypePython}}
	registerSpec(t, repo, ctx, dagID, tasks)
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateDagRun: %v", err)
	}
	runUUID := resolveRunUUID(t, sched, ctx, dagID)
	if err := sched.MaterializeTasks(ctx, runUUID, tasks); err != nil {
		t.Fatalf("MaterializeTasks: %v", err)
	}
	// Bring the probe to running — a reschedule-mode sensor pod executing.
	for _, st := range []domain.TaskState{domain.TaskStateScheduled, domain.TaskStateQueued, domain.TaskStateRunning} {
		if err := sched.ApplyTransition(ctx, runUUID, "probe", st); err != nil {
			t.Fatalf("ApplyTransition %s: %v", st, err)
		}
	}
	tryBefore := rescheduleTryNumber(t, sched, ctx, runUUID, "probe")

	// Poke-not-ready: the agent reports up_for_reschedule with a reschedule time
	// already in the past (so it is immediately ready to re-dispatch).
	id := auth.AgentIdentity{RunID: runUUID, TaskID: "probe", TryNumber: tryBefore}
	if err := exec.Reschedule(ctx, id, time.Now().UTC().Add(-time.Minute)); err != nil {
		t.Fatalf("Reschedule: %v", err)
	}
	if got := taskInstanceState(t, sched, ctx, runUUID, "probe"); got != domain.TaskStateUpForReschedule {
		t.Fatalf("after Reschedule, state = %q, want up_for_reschedule", got)
	}

	// The scheduler loads reschedule_at and the planner releases the ready task to
	// none — the real DB round-trip driving the re-dispatch decision.
	rs := rescheduleActiveRun(t, sched, ctx, runUUID)
	if rs.RescheduleAt["probe"] == nil {
		t.Fatal("ActiveRuns did not load reschedule_at for the parked task")
	}
	if !reschedulePlanned(scheduler.PlanRun(rs), "probe", domain.TaskStateNone) {
		t.Fatal("planner did not release the ready up_for_reschedule task to none")
	}

	// Re-dispatch: back to none, try_number PRESERVED (no attempt consumed), and
	// reschedule_at cleared — the contract that distinguishes reschedule from retry.
	if err := sched.RedispatchReschedule(ctx, runUUID, "probe"); err != nil {
		t.Fatalf("RedispatchReschedule: %v", err)
	}
	if got := taskInstanceState(t, sched, ctx, runUUID, "probe"); got != domain.TaskStateNone {
		t.Fatalf("after re-dispatch, state = %q, want none", got)
	}
	rs = rescheduleActiveRun(t, sched, ctx, runUUID)
	if got := rs.Tries["probe"]; got != tryBefore {
		t.Fatalf("try_number = %d after reschedule, want %d (reschedule must not consume an attempt)", got, tryBefore)
	}
	if rs.RescheduleAt["probe"] != nil {
		t.Fatalf("reschedule_at not cleared on re-dispatch: %v", *rs.RescheduleAt["probe"])
	}
}

// rescheduleActiveRun returns the RunState for runUUID from ActiveRuns (carrying
// States, Tries, and RescheduleAt loaded from the DB).
func rescheduleActiveRun(t *testing.T, sched *storage.SchedulerStore, ctx context.Context, runUUID string) scheduler.RunState {
	t.Helper()
	runs, err := sched.ActiveRuns(ctx)
	if err != nil {
		t.Fatalf("ActiveRuns: %v", err)
	}
	for _, r := range runs {
		if r.RunID == runUUID {
			return r
		}
	}
	t.Fatalf("active run %s not found", runUUID)
	return scheduler.RunState{}
}

func rescheduleTryNumber(t *testing.T, sched *storage.SchedulerStore, ctx context.Context, runUUID, taskID string) int {
	t.Helper()
	return rescheduleActiveRun(t, sched, ctx, runUUID).Tries[taskID]
}

func reschedulePlanned(plan []scheduler.PlannedTransition, taskID string, to domain.TaskState) bool {
	for _, p := range plan {
		if p.TaskID == taskID && p.To == to {
			return true
		}
	}
	return false
}
