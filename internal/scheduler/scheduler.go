package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/neochaotic/leoflow/internal/domain"
)

// RunState is the scheduler's snapshot of a dag run: its topology and the
// current state of each task.
type RunState struct {
	RunID  string
	State  domain.DagRunState
	Tasks  []domain.TaskSpec
	States map[string]domain.TaskState
}

// Store is the scheduler's view of persistent state. The concrete
// implementation is sqlc-backed; tests use a fake.
type Store interface {
	ActiveRuns(ctx context.Context) ([]RunState, error)
	MaterializeTasks(ctx context.Context, runID string, tasks []domain.TaskSpec) error
	ApplyTransition(ctx context.Context, runID, taskID string, to domain.TaskState) error
	SetRunState(ctx context.Context, runID string, state domain.DagRunState) error
}

// Scheduler advances dag runs by applying the planning rules each tick.
type Scheduler struct {
	store    Store
	logger   *slog.Logger
	interval time.Duration
}

// NewScheduler builds a Scheduler over the given store, ticking every interval.
func NewScheduler(store Store, logger *slog.Logger, interval time.Duration) *Scheduler {
	return &Scheduler{store: store, logger: logger, interval: interval}
}

// Run drives the scheduling loop until ctx is canceled.
func (s *Scheduler) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := s.Step(ctx); err != nil {
				s.logger.Error("scheduler step", "error", err)
			}
		}
	}
}

// Step runs one deterministic scheduling iteration over every active run.
func (s *Scheduler) Step(ctx context.Context) error {
	runs, err := s.store.ActiveRuns(ctx)
	if err != nil {
		return fmt.Errorf("listing active runs: %w", err)
	}
	for _, run := range runs {
		if err := s.advance(ctx, run); err != nil {
			return err
		}
	}
	return nil
}

func (s *Scheduler) advance(ctx context.Context, run RunState) error {
	// Materialize task instances on first sight of a queued run, then start it.
	if run.State == domain.DagRunStateQueued && len(run.States) == 0 {
		if err := s.store.MaterializeTasks(ctx, run.RunID, run.Tasks); err != nil {
			return fmt.Errorf("materializing tasks: %w", err)
		}
		if err := s.store.SetRunState(ctx, run.RunID, domain.DagRunStateRunning); err != nil {
			return fmt.Errorf("starting run: %w", err)
		}
		return nil
	}
	for _, t := range PlanRun(run.Tasks, run.States) {
		if err := s.store.ApplyTransition(ctx, run.RunID, t.TaskID, t.To); err != nil {
			return fmt.Errorf("applying transition for %s: %w", t.TaskID, err)
		}
	}
	if state, done := FinalizeRun(run.Tasks, run.States); done {
		if err := s.store.SetRunState(ctx, run.RunID, state); err != nil {
			return fmt.Errorf("finalizing run: %w", err)
		}
	}
	return nil
}
