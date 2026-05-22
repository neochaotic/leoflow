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

func TestStepWithoutDispatcherStillQueues(t *testing.T) {
	store := runWithScheduledRoot()
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.transitions) != 1 || store.transitions[0].to != domain.TaskStateQueued {
		t.Errorf("state-only scheduler should still queue, got %v", store.transitions)
	}
}
