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
	retried     []transition
	runStates   map[string]domain.DagRunState
	scheduled   []ScheduledDAG
	createdRuns []string
}

func newFakeStore(runs ...RunState) *fakeStore {
	return &fakeStore{runs: runs, runStates: map[string]domain.DagRunState{}}
}

func (f *fakeStore) ActiveRuns(context.Context) ([]RunState, error) { return f.runs, nil }
func (f *fakeStore) ScheduledDAGs(context.Context) ([]ScheduledDAG, error) {
	return f.scheduled, nil
}
func (f *fakeStore) CreateScheduledRun(_ context.Context, dagID string, _ time.Time) error {
	f.createdRuns = append(f.createdRuns, dagID)
	return nil
}
func (f *fakeStore) MaterializeTasks(_ context.Context, runID string, _ []domain.TaskSpec) error {
	f.materialize = append(f.materialize, runID)
	return nil
}
func (f *fakeStore) ApplyTransition(_ context.Context, runID, taskID string, to domain.TaskState) error {
	f.transitions = append(f.transitions, transition{runID, taskID, to})
	return nil
}
func (f *fakeStore) ResetForRetry(_ context.Context, runID, taskID string) error {
	f.retried = append(f.retried, transition{runID, taskID, domain.TaskStateNone})
	return nil
}
func (f *fakeStore) SetRunState(_ context.Context, runID string, state domain.DagRunState) error {
	f.runStates[runID] = state
	return nil
}

func newScheduler(store Store) *Scheduler {
	return NewScheduler(store, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Millisecond)
}

func retriableRun(aState domain.TaskState, aTry int) *fakeStore {
	return newFakeStore(RunState{
		RunID: "r1", DagID: "etl", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States:   map[string]domain.TaskState{"a": aState, "b": domain.TaskStateNone},
		Tries:    map[string]int{"a": aTry, "b": 1},
		MaxTries: map[string]int{"a": 3, "b": 3},
	})
}

func TestStepMovesRetriableFailureToUpForRetry(t *testing.T) {
	store := retriableRun(domain.TaskStateFailed, 1)
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !hasTransition(store.transitions, "a", domain.TaskStateUpForRetry) {
		t.Errorf("retriable failed a should move to up_for_retry, got %v", store.transitions)
	}
	if _, finalized := store.runStates["r1"]; finalized {
		t.Error("run must not finalize while a can still retry")
	}
}

func TestStepResetsUpForRetryTask(t *testing.T) {
	store := retriableRun(domain.TaskStateUpForRetry, 1)
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.retried) != 1 || store.retried[0].taskID != "a" {
		t.Errorf("up_for_retry a should be reset for retry, got %v", store.retried)
	}
}

func TestStepFinalizesFailedWhenRetriesExhausted(t *testing.T) {
	store := newFakeStore(RunState{
		RunID: "r1", DagID: "etl", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States:   map[string]domain.TaskState{"a": domain.TaskStateFailed, "b": domain.TaskStateUpstreamFailed},
		Tries:    map[string]int{"a": 3, "b": 1},
		MaxTries: map[string]int{"a": 3, "b": 3},
	})
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.runStates["r1"] != domain.DagRunStateFailed {
		t.Errorf("exhausted failure should finalize the run failed, got %q", store.runStates["r1"])
	}
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

func TestStepCreatesDueScheduledRun(t *testing.T) {
	last := time.Now().UTC().Add(-2 * time.Hour)
	store := newFakeStore()
	store.scheduled = []ScheduledDAG{{DagID: "etl", Schedule: "@hourly", LastLogical: &last}}
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.createdRuns) != 1 || store.createdRuns[0] != "etl" {
		t.Errorf("expected one scheduled run for etl, got %v", store.createdRuns)
	}
}

func TestStepNoScheduledRunWhenNotDue(t *testing.T) {
	recent := time.Now().UTC().Add(-1 * time.Minute)
	store := newFakeStore()
	store.scheduled = []ScheduledDAG{{DagID: "etl", Schedule: "@hourly", LastLogical: &recent}}
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.createdRuns) != 0 {
		t.Errorf("no run should be created when not due, got %v", store.createdRuns)
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
