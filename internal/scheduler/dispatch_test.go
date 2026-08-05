package scheduler

import (
	"context"
	"errors"
	"testing"

	"github.com/neochaotic/leoflow/internal/domain"
)

type fakeDispatcher struct {
	dispatched []string
	err        error
}

func (d *fakeDispatcher) Dispatch(_ context.Context, _, _ string, task domain.TaskSpec) error {
	d.dispatched = append(d.dispatched, task.TaskID)
	return d.err
}

func hasTransition(ts []transition, taskID string, to domain.TaskState) bool {
	for _, tr := range ts {
		if tr.taskID == taskID && tr.to == to {
			return true
		}
	}
	return false
}

func TestStepUsesDispatcherForPodTasks(t *testing.T) {
	store := runWithScheduledRoot() // python task "a"
	dispatcher := &fakeDispatcher{}
	s := newScheduler(store)
	s.SetDispatcher(dispatcher)

	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.dispatched) != 1 || !hasTransition(store.transitions, "a", domain.TaskStateQueued) {
		t.Errorf("python task should dispatch then queue, got dispatched=%v transitions=%v", dispatcher.dispatched, store.transitions)
	}
}

func runWithScheduledRoot() *fakeStore {
	return newFakeStore(RunState{
		RunID: "r1", DagID: "etl", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States: map[string]domain.TaskState{"a": domain.TaskStateScheduled, "b": domain.TaskStateNone},
	})
}

func TestStepDispatchesQueuedTaskBeforeTransition(t *testing.T) {
	store := runWithScheduledRoot()
	d := &fakeDispatcher{}
	s := newScheduler(store)
	s.SetDispatcher(d)

	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(d.dispatched) != 1 || d.dispatched[0] != "a" {
		t.Errorf("expected task a dispatched once, got %v", d.dispatched)
	}
	if len(store.transitions) != 1 || store.transitions[0] != (transition{"r1", "a", domain.TaskStateQueued}) {
		t.Errorf("expected a->queued after dispatch, got %v", store.transitions)
	}
}

func TestStepLeavesTaskScheduledWhenDispatchFails(t *testing.T) {
	store := runWithScheduledRoot()
	d := &fakeDispatcher{err: errors.New("executor unavailable")}
	s := newScheduler(store)
	s.SetDispatcher(d)

	if err := s.Step(context.Background()); err != nil {
		t.Fatalf("a dispatch failure must not abort the step: %v", err)
	}
	if len(d.dispatched) != 1 {
		t.Errorf("dispatch should have been attempted once, got %v", d.dispatched)
	}
	if len(store.transitions) != 0 {
		t.Errorf("failed dispatch must leave the task scheduled, got %v", store.transitions)
	}
}

func TestStepWithoutDispatcherFailsUndispatchable(t *testing.T) {
	// With no dispatcher and no inline runner the task can never run, so the
	// scheduler fails it fast (with a note) instead of queuing it forever (#50).
	store := runWithScheduledRoot()
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.transitions) != 1 || store.transitions[0].to != domain.TaskStateFailed {
		t.Errorf("state-only scheduler should fail the undispatchable task, got %v", store.transitions)
	}
}

// A synchronous dispatch failure records a backed-off retry (ADR 0031 Amendment
// A) instead of silently re-attempting every tick. The task stays scheduled; the
// backoff is what the planner gates the next attempt on.
func TestStepBacksOffOnDispatchFailure(t *testing.T) {
	store := runWithScheduledRoot()
	d := &fakeDispatcher{err: errors.New("kube-apiserver unreachable")}
	s := newScheduler(store)
	s.SetDispatcher(d)

	if err := s.Step(context.Background()); err != nil {
		t.Fatalf("a dispatch failure must not abort the step: %v", err)
	}
	if len(store.dispatchFailures) != 1 || store.dispatchFailures[0].taskID != "a" {
		t.Errorf("first dispatch failure should record a backoff for task a, got %v", store.dispatchFailures)
	}
	if len(store.dispatchExhausted) != 0 {
		t.Errorf("one failure must not exhaust the budget, got %v", store.dispatchExhausted)
	}
	if len(store.transitions) != 0 {
		t.Errorf("a failed dispatch must not transition the task, got %v", store.transitions)
	}
}

// Once the dispatch-attempt budget is spent, the task is failed as
// dispatch_failed so the run can finalize instead of looping forever.
func TestStepFailsTaskWhenDispatchExhausted(t *testing.T) {
	store := newFakeStore(RunState{
		RunID: "r1", DagID: "etl", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States:           map[string]domain.TaskState{"a": domain.TaskStateScheduled, "b": domain.TaskStateNone},
		DispatchAttempts: map[string]int{"a": dispatchMaxAttempts - 1}, // one more failure exhausts it
	})
	d := &fakeDispatcher{err: errors.New("RBAC: cannot create pods")}
	s := newScheduler(store)
	s.SetDispatcher(d)

	if err := s.Step(context.Background()); err != nil {
		t.Fatalf("dispatch exhaustion must not abort the step: %v", err)
	}
	if len(store.dispatchExhausted) != 1 || store.dispatchExhausted[0] != "a" {
		t.Errorf("the final dispatch failure should fail task a as dispatch_failed, got %v", store.dispatchExhausted)
	}
	if len(store.dispatchFailures) != 0 {
		t.Errorf("the exhausting attempt must fail, not back off again, got %v", store.dispatchFailures)
	}
}
