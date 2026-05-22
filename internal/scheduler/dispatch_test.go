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

type fakeInline struct {
	started []string
	start   bool
	err     error
}

func (f *fakeInline) Start(_ context.Context, _, _, _ string, _ int, task domain.TaskSpec) (bool, error) {
	f.started = append(f.started, task.TaskID)
	return f.start, f.err
}

func httpRun() *fakeStore {
	return newFakeStore(RunState{
		RunID: "r1", DagID: "etl", State: domain.DagRunStateRunning,
		Tasks: []domain.TaskSpec{{
			TaskID: "hook", Type: domain.TaskTypeHTTPAPI,
			HTTPRequest: &domain.HTTPRequest{Method: "GET", URL: "http://x"},
		}},
		States: map[string]domain.TaskState{"hook": domain.TaskStateScheduled},
	})
}

func hasTransition(ts []transition, taskID string, to domain.TaskState) bool {
	for _, tr := range ts {
		if tr.taskID == taskID && tr.to == to {
			return true
		}
	}
	return false
}

func TestStepRunsInlineHTTPTaskOutOfBand(t *testing.T) {
	store := httpRun()
	inline := &fakeInline{start: true}
	s := newScheduler(store)
	s.SetInlineRunner(inline)

	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(inline.started) != 1 || inline.started[0] != "hook" {
		t.Errorf("inline runner should run hook once, got %v", inline.started)
	}
	if len(store.transitions) != 0 {
		t.Errorf("started inline task: scheduler must not also queue it, got %v", store.transitions)
	}
}

func TestStepLeavesInlineTaskScheduledWhenAtCapacity(t *testing.T) {
	store := httpRun()
	inline := &fakeInline{start: false} // semaphore full
	s := newScheduler(store)
	s.SetInlineRunner(inline)

	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(inline.started) != 1 {
		t.Errorf("inline start should be attempted once, got %v", inline.started)
	}
	if len(store.transitions) != 0 {
		t.Errorf("at capacity: task must stay scheduled, got %v", store.transitions)
	}
}

func TestStepFailsInlineTaskOnStartError(t *testing.T) {
	store := httpRun()
	inline := &fakeInline{err: errors.New("timeout exceeds cap")}
	s := newScheduler(store)
	s.SetInlineRunner(inline)

	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !hasTransition(store.transitions, "hook", domain.TaskStateFailed) {
		t.Errorf("invalid inline task should be marked failed, got %v", store.transitions)
	}
}

func TestStepUsesDispatcherForPodTasks(t *testing.T) {
	store := runWithScheduledRoot() // python task "a"
	dispatcher := &fakeDispatcher{}
	inline := &fakeInline{start: true}
	s := newScheduler(store)
	s.SetDispatcher(dispatcher)
	s.SetInlineRunner(inline)

	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(inline.started) != 0 {
		t.Errorf("python task must not use the inline runner, got %v", inline.started)
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

func TestStepWithoutDispatcherStillQueues(t *testing.T) {
	store := runWithScheduledRoot()
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.transitions) != 1 || store.transitions[0].to != domain.TaskStateQueued {
		t.Errorf("state-only scheduler should still queue, got %v", store.transitions)
	}
}
