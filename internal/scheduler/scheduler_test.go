package scheduler

import (
	"context"
	"io"
	"log/slog"
	"strings"
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
	notes       map[string]string
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
func (f *fakeStore) SetTaskNote(_ context.Context, _, taskID, note string) error {
	if f.notes == nil {
		f.notes = map[string]string{}
	}
	f.notes[taskID] = note
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

func TestHeartbeatTracksTicks(t *testing.T) {
	s := NewScheduler(&fakeStore{}, slog.New(slog.NewTextHandler(io.Discard, nil)), 10*time.Millisecond)

	// Before any tick: healthy by startup grace.
	if ok, _ := s.Heartbeat(); !ok {
		t.Error("fresh scheduler should be healthy (startup grace)")
	}
	// After a Step: healthy with a recent heartbeat.
	if err := s.Step(context.Background()); err != nil {
		t.Fatalf("Step: %v", err)
	}
	ok, last := s.Heartbeat()
	if !ok {
		t.Error("scheduler should be healthy right after a tick")
	}
	if time.Since(last) > time.Second {
		t.Errorf("heartbeat %v is not recent", last)
	}
}

func TestHeartbeatIsLeadershipAware(t *testing.T) {
	s := NewScheduler(&fakeStore{}, slog.New(slog.NewTextHandler(io.Discard, nil)), 10*time.Millisecond)

	// Not leading: healthy regardless of ticks — a follower (or an instance that
	// stepped down) is correctly idle, not stalled. Even a very old lastTick is OK.
	s.lastTick.Store(time.Now().Add(-time.Hour).UnixNano())
	if ok, _ := s.Heartbeat(); !ok {
		t.Error("a non-leader must report healthy (idle), not stalled")
	}

	// Becoming leader resets the clock (startup grace), then a tick is fresh+healthy.
	s.SetLeading(true)
	if ok, _ := s.Heartbeat(); !ok {
		t.Error("a fresh leader should be healthy (startup grace)")
	}
	if err := s.Step(context.Background()); err != nil {
		t.Fatalf("Step: %v", err)
	}
	if ok, _ := s.Heartbeat(); !ok {
		t.Error("a leader that just ticked should be healthy")
	}

	// A leader whose ticks went stale is unhealthy, so the UI/monitor surfaces it.
	s.lastTick.Store(time.Now().Add(-time.Hour).UnixNano())
	if ok, _ := s.Heartbeat(); ok {
		t.Error("a leader with a stale heartbeat must be unhealthy")
	}

	// After stepping down it is idle again — healthy, not falsely stalled.
	s.SetLeading(false)
	if ok, _ := s.Heartbeat(); !ok {
		t.Error("after stepping down, an idle instance should be healthy")
	}
}

type fakeRecorder struct{ undispatchable []string }

func (r *fakeRecorder) RecordSchedulerDecision(string)      {}
func (r *fakeRecorder) RecordTaskTransition(_, _, _ string) {}
func (r *fakeRecorder) RecordUndispatchable(reason string) {
	r.undispatchable = append(r.undispatchable, reason)
}

func freshRun() *fakeStore {
	// 'a' starts scheduled so a single Step plans it none->queued via launchQueued
	// (the planner moves none->scheduled->queued across ticks).
	return newFakeStore(RunState{
		RunID: "r1", DagID: "etl", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States:   map[string]domain.TaskState{"a": domain.TaskStateScheduled, "b": domain.TaskStateNone},
		Tries:    map[string]int{"a": 0, "b": 0},
		MaxTries: map[string]int{"a": 1, "b": 1},
	})
}

func TestStepRecordsUndispatchableWhenNoExecutor(t *testing.T) {
	store := freshRun()
	rec := &fakeRecorder{}
	s := newScheduler(store)
	s.SetRecorder(rec)
	// No dispatcher and no inline runner: task 'a' becomes queued with nothing
	// to launch it -> the undispatchable signal must fire (#46).
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(rec.undispatchable) != 1 || rec.undispatchable[0] != "no_executor" {
		t.Errorf("expected one no_executor record, got %v", rec.undispatchable)
	}
	// 'a' is FAILED fast (it can never run) rather than left queued forever (#50).
	if !hasTransition(store.transitions, "a", domain.TaskStateFailed) {
		t.Errorf("task a should be failed (no executor), got %v", store.transitions)
	}
	if hasTransition(store.transitions, "a", domain.TaskStateQueued) {
		t.Errorf("task a must NOT be left queued; it can never run, got %v", store.transitions)
	}
	// The reason is surfaced as a task note for the UI.
	if note := store.notes["a"]; !strings.Contains(note, "no executor available") {
		t.Errorf("task a should get an explanatory note, got %q", note)
	}
}

func TestStepDoesNotRecordUndispatchableWithDispatcher(t *testing.T) {
	store := freshRun()
	rec := &fakeRecorder{}
	disp := &fakeDispatcher{}
	s := newScheduler(store)
	s.SetRecorder(rec)
	s.SetDispatcher(disp)
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(rec.undispatchable) != 0 {
		t.Errorf("with a dispatcher there should be no undispatchable records, got %v", rec.undispatchable)
	}
	if len(disp.dispatched) != 1 || disp.dispatched[0] != "a" {
		t.Errorf("task a should be dispatched, got %v", disp.dispatched)
	}
}
