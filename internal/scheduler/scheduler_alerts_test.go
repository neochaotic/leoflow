package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/neochaotic/leoflow/internal/domain"
)

// recordingAlerter signals each AlertRunFailed call over a buffered channel so a
// test can observe the scheduler's fire-and-forget dispatch deterministically.
type recordingAlerter struct{ ch chan RunState }

func (r *recordingAlerter) AlertRunFailed(_ context.Context, run RunState) { r.ch <- run }

// blockingAlerter signals when a dispatch starts and then holds the concurrency
// slot until released, so a test can prove the scheduler bounds concurrency.
type blockingAlerter struct {
	started chan RunState
	release chan struct{}
}

func (b *blockingAlerter) AlertRunFailed(_ context.Context, run RunState) {
	b.started <- run
	<-b.release
}

func exhaustedFailedRun(id string) RunState {
	return RunState{
		RunID: id, DagID: "etl", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States:   map[string]domain.TaskState{"a": domain.TaskStateFailed, "b": domain.TaskStateUpstreamFailed},
		Tries:    map[string]int{"a": 3, "b": 1},
		MaxTries: map[string]int{"a": 3, "b": 3},
		Alerts:   &domain.AlertsConfig{OnFailure: []domain.AlertRule{{Type: "slack", Conn: "slack"}}},
	}
}

// With the concurrency bound at 1, a second simultaneously-failing run is dropped
// (best-effort) rather than spawning an unbounded second dispatch goroutine.
func TestStepBoundsAlertConcurrency(t *testing.T) {
	al := &blockingAlerter{started: make(chan RunState, 4), release: make(chan struct{})}
	defer close(al.release)
	store := newFakeStore(exhaustedFailedRun("r1"), exhaustedFailedRun("r2"))
	s := newScheduler(store)
	s.SetAlerter(al)
	s.SetAlertConcurrency(1)
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-al.started:
	case <-time.After(2 * time.Second):
		t.Fatal("expected one alert to start")
	}
	select {
	case <-al.started:
		t.Fatal("the second alert must be dropped while the slot is held, not spawned")
	case <-time.After(150 * time.Millisecond):
	}
}

// A run that finalizes failed with on_failure rules fires the alerter exactly once.
func TestStepAlertsOnFailedFinalize(t *testing.T) {
	store := newFakeStore(RunState{
		RunID: "r1", DagID: "etl", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States:   map[string]domain.TaskState{"a": domain.TaskStateFailed, "b": domain.TaskStateUpstreamFailed},
		Tries:    map[string]int{"a": 3, "b": 1},
		MaxTries: map[string]int{"a": 3, "b": 3},
		Alerts:   &domain.AlertsConfig{OnFailure: []domain.AlertRule{{Type: "slack", Conn: "slack"}}},
	})
	al := &recordingAlerter{ch: make(chan RunState, 1)}
	s := newScheduler(store)
	s.SetAlerter(al)
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-al.ch:
		if got.RunID != "r1" {
			t.Errorf("alerted run = %q, want r1", got.RunID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected an on-failure alert to fire")
	}
}

// A successful run never alerts, even with on_failure rules configured.
func TestStepNoAlertOnSuccess(t *testing.T) {
	store := newFakeStore(RunState{
		RunID: "r1", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States: map[string]domain.TaskState{"a": domain.TaskStateSuccess, "b": domain.TaskStateSuccess},
		Alerts: &domain.AlertsConfig{OnFailure: []domain.AlertRule{{Type: "slack", Conn: "slack"}}},
	})
	al := &recordingAlerter{ch: make(chan RunState, 1)}
	s := newScheduler(store)
	s.SetAlerter(al)
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-al.ch:
		t.Fatal("no alert expected on a successful run")
	case <-time.After(150 * time.Millisecond):
	}
}

// A failed run with no alert rules does not fire the alerter (nothing to send).
func TestStepNoAlertWhenNoRules(t *testing.T) {
	store := newFakeStore(RunState{
		RunID: "r1", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States:   map[string]domain.TaskState{"a": domain.TaskStateFailed, "b": domain.TaskStateUpstreamFailed},
		Tries:    map[string]int{"a": 3, "b": 1},
		MaxTries: map[string]int{"a": 3, "b": 3},
	})
	al := &recordingAlerter{ch: make(chan RunState, 1)}
	s := newScheduler(store)
	s.SetAlerter(al)
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-al.ch:
		t.Fatal("no alert expected without on_failure rules")
	case <-time.After(150 * time.Millisecond):
	}
}
