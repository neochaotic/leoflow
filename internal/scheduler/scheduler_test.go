package scheduler

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/neochaotic/leoflow/internal/domain"
)

type transition struct {
	runID, taskID string
	to            domain.TaskState
}

type fakeStore struct {
	runs        []RunState
	materialize []string
	transitions []transition
	runStates   map[string]domain.DagRunState
}

func newFakeStore(runs ...RunState) *fakeStore {
	return &fakeStore{runs: runs, runStates: map[string]domain.DagRunState{}}
}

func (f *fakeStore) ActiveRuns(context.Context) ([]RunState, error) { return f.runs, nil }
func (f *fakeStore) MaterializeTasks(_ context.Context, runID string, _ []domain.TaskSpec) error {
	f.materialize = append(f.materialize, runID)
	return nil
}
func (f *fakeStore) ApplyTransition(_ context.Context, runID, taskID string, to domain.TaskState) error {
	f.transitions = append(f.transitions, transition{runID, taskID, to})
	return nil
}
func (f *fakeStore) SetRunState(_ context.Context, runID string, state domain.DagRunState) error {
	f.runStates[runID] = state
	return nil
}

func newScheduler(store Store) *Scheduler {
	return NewScheduler(store, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Millisecond)
}

func linearTasks() []domain.TaskSpec {
	return []domain.TaskSpec{
		{TaskID: "a", Type: domain.TaskTypePython},
		{TaskID: "b", Type: domain.TaskTypePython, DependsOn: []string{"a"}},
	}
}

func TestStepMaterializesQueuedRun(t *testing.T) {
	store := newFakeStore(RunState{RunID: "r1", State: domain.DagRunStateQueued, Tasks: linearTasks(), States: map[string]domain.TaskState{}})
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.materialize) != 1 || store.materialize[0] != "r1" {
		t.Errorf("expected materialize r1, got %v", store.materialize)
	}
	if store.runStates["r1"] != domain.DagRunStateRunning {
		t.Errorf("queued run should start running, got %q", store.runStates["r1"])
	}
}

func TestStepSchedulesRootTask(t *testing.T) {
	store := newFakeStore(RunState{
		RunID: "r1", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States: map[string]domain.TaskState{"a": domain.TaskStateNone, "b": domain.TaskStateNone},
	})
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.transitions) != 1 || store.transitions[0] != (transition{"r1", "a", domain.TaskStateScheduled}) {
		t.Errorf("expected a->scheduled, got %v", store.transitions)
	}
}

func TestStepFinalizesCompletedRun(t *testing.T) {
	store := newFakeStore(RunState{
		RunID: "r1", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States: map[string]domain.TaskState{"a": domain.TaskStateSuccess, "b": domain.TaskStateSuccess},
	})
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.runStates["r1"] != domain.DagRunStateSuccess {
		t.Errorf("completed run should be success, got %q", store.runStates["r1"])
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	store := newFakeStore()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := newScheduler(store).Run(ctx); err == nil {
		t.Error("Run should return ctx error after cancel")
	}
}
