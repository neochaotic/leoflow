package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/neochaotic/leoflow/internal/domain"
)

// RunState is the scheduler's snapshot of a dag run: its topology and the
// current state of each task.
type RunState struct {
	RunID    string
	DagID    string
	TenantID string
	State    domain.DagRunState
	Tasks    []domain.TaskSpec
	States   map[string]domain.TaskState
	// Tries and MaxTries hold the current and maximum attempt counts per task,
	// driving retry decisions. Absent entries mean no retry budget.
	Tries    map[string]int
	MaxTries map[string]int
}

// ScheduledDAG is a cron-scheduled DAG and the logical date of its latest run.
type ScheduledDAG struct {
	DagID       string
	Schedule    string
	LastLogical *time.Time
}

// Store is the scheduler's view of persistent state. The concrete
// implementation is sqlc-backed; tests use a fake.
type Store interface {
	ActiveRuns(ctx context.Context) ([]RunState, error)
	MaterializeTasks(ctx context.Context, runID string, tasks []domain.TaskSpec) error
	ApplyTransition(ctx context.Context, runID, taskID string, to domain.TaskState) error
	// ResetForRetry returns a task to 'none' and increments its try number so a
	// retry re-evaluates and re-runs it.
	ResetForRetry(ctx context.Context, runID, taskID string) error
	SetRunState(ctx context.Context, runID string, state domain.DagRunState) error
	ScheduledDAGs(ctx context.Context) ([]ScheduledDAG, error)
	CreateScheduledRun(ctx context.Context, dagID string, logical time.Time) error
}

// Recorder records scheduler metrics. observability.Metrics implements it.
type Recorder interface {
	RecordSchedulerDecision(decisionType string)
	RecordTaskTransition(from, to, dagID string)
}

// Dispatcher launches a task instance for execution. The scheduler dispatches a
// task as it becomes queued; the concrete implementation builds the executor
// request and routes it to the right executor.
type Dispatcher interface {
	Dispatch(ctx context.Context, runID, dagID string, task domain.TaskSpec) error
}

// InlineRunner executes an inline http_api task out of band in the control
// plane. Start reports whether the task was launched (false without error means
// it should be retried on the next tick); the runner owns the task's state once
// started, so the scheduler does not record a queued transition for it.
type InlineRunner interface {
	Start(ctx context.Context, runID, dagID, tenantID string, tryNumber int, task domain.TaskSpec) (started bool, err error)
}

// Scheduler advances dag runs by applying the planning rules each tick.
type Scheduler struct {
	store      Store
	logger     *slog.Logger
	interval   time.Duration
	recorder   Recorder
	dispatcher Dispatcher
	inline     InlineRunner
	lastTick   atomic.Int64 // unix-nano of the last loop iteration; 0 = not yet ticked
}

// NewScheduler builds a Scheduler over the given store, ticking every interval.
func NewScheduler(store Store, logger *slog.Logger, interval time.Duration) *Scheduler {
	return &Scheduler{store: store, logger: logger, interval: interval}
}

// SetRecorder attaches a metrics recorder (optional).
func (s *Scheduler) SetRecorder(r Recorder) { s.recorder = r }

// SetDispatcher attaches the executor dispatcher (optional; without it the
// scheduler advances state only and launches nothing).
func (s *Scheduler) SetDispatcher(d Dispatcher) { s.dispatcher = d }

// SetInlineRunner attaches the inline http_api runner (optional; without it
// inline http_api tasks fall back to the standard queued dispatch path).
func (s *Scheduler) SetInlineRunner(r InlineRunner) { s.inline = r }

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

// Heartbeat reports whether the scheduling loop is live and when it last ticked.
// It is healthy before the first tick (startup grace) and while ticks stay
// within a small multiple of the loop interval; a stalled leader goes unhealthy.
// A non-leader process never ticks (lastTick stays 0) and stays in grace.
func (s *Scheduler) Heartbeat() (bool, time.Time) {
	nanos := s.lastTick.Load()
	if nanos == 0 {
		return true, time.Now().UTC()
	}
	last := time.Unix(0, nanos).UTC()
	return time.Since(last) <= 3*s.interval+time.Second, last
}

// Step runs one deterministic scheduling iteration over every active run.
func (s *Scheduler) Step(ctx context.Context) error {
	s.lastTick.Store(time.Now().UnixNano())
	runs, err := s.store.ActiveRuns(ctx)
	if err != nil {
		return fmt.Errorf("listing active runs: %w", err)
	}
	for _, run := range runs {
		if err := s.advance(ctx, run); err != nil {
			return err
		}
	}
	return s.createDueRuns(ctx)
}

// createDueRuns creates a new run for each scheduled DAG whose next cron slot
// after its latest run has arrived.
func (s *Scheduler) createDueRuns(ctx context.Context) error {
	dags, err := s.store.ScheduledDAGs(ctx)
	if err != nil {
		return fmt.Errorf("listing scheduled dags: %w", err)
	}
	now := time.Now().UTC()
	for _, d := range dags {
		logical, due := nextScheduledRun(d.Schedule, d.LastLogical, now)
		if !due {
			continue
		}
		if err := s.store.CreateScheduledRun(ctx, d.DagID, logical); err != nil {
			return fmt.Errorf("creating scheduled run for %s: %w", d.DagID, err)
		}
		if s.recorder != nil {
			s.recorder.RecordSchedulerDecision("create_run")
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
	for _, t := range PlanRun(run) {
		if err := s.applyPlanned(ctx, run, t); err != nil {
			return err
		}
	}
	if state, done := FinalizeRun(run); done {
		if err := s.store.SetRunState(ctx, run.RunID, state); err != nil {
			return fmt.Errorf("finalizing run: %w", err)
		}
	}
	return nil
}

// applyPlanned launches a task as it becomes queued and records the resulting
// transition. Non-queued transitions are recorded directly.
func (s *Scheduler) applyPlanned(ctx context.Context, run RunState, t PlannedTransition) error {
	switch t.To {
	case domain.TaskStateQueued:
		return s.launchQueued(ctx, run, t)
	case domain.TaskStateNone:
		return s.resetForRetry(ctx, run, t.TaskID)
	default:
		return s.recordTransition(ctx, run, t.TaskID, t.To)
	}
}

// resetForRetry returns a task to 'none' with an incremented try number so the
// next tick re-evaluates and re-runs it.
func (s *Scheduler) resetForRetry(ctx context.Context, run RunState, taskID string) error {
	if err := s.store.ResetForRetry(ctx, run.RunID, taskID); err != nil {
		return fmt.Errorf("resetting %s for retry: %w", taskID, err)
	}
	if s.recorder != nil {
		s.recorder.RecordSchedulerDecision("retry")
		s.recorder.RecordTaskTransition(string(run.States[taskID]), string(domain.TaskStateNone), run.DagID)
	}
	return nil
}

// launchQueued routes a queued task to the inline runner (inline http_api) or
// the dispatcher (pod path), recording the appropriate transition. A transient
// failure leaves the task scheduled so the next tick retries.
func (s *Scheduler) launchQueued(ctx context.Context, run RunState, t PlannedTransition) error {
	task, ok := findTask(run.Tasks, t.TaskID)
	if !ok {
		return fmt.Errorf("task %s not found in run %s", t.TaskID, run.RunID)
	}
	if s.inline != nil && task.Type == domain.TaskTypeHTTPAPI && task.EffectiveExecutionMode() == domain.ExecutionModeInline {
		return s.runInline(ctx, run, task)
	}
	if s.dispatcher != nil {
		if err := s.dispatcher.Dispatch(ctx, run.RunID, run.DagID, task); err != nil {
			s.logger.Error("dispatching task", "run", run.RunID, "task", t.TaskID, "error", err)
			return nil
		}
	}
	return s.recordTransition(ctx, run, t.TaskID, domain.TaskStateQueued)
}

// runInline starts an inline http_api task. The runner owns the task's state
// once started, so no queued transition is recorded; a start error marks the
// task failed, and an at-capacity result leaves it scheduled for retry.
func (s *Scheduler) runInline(ctx context.Context, run RunState, task domain.TaskSpec) error {
	started, err := s.inline.Start(ctx, run.RunID, run.DagID, run.TenantID, run.Tries[task.TaskID], task)
	if err != nil {
		s.logger.Error("starting inline task", "run", run.RunID, "task", task.TaskID, "error", err)
		return s.recordTransition(ctx, run, task.TaskID, domain.TaskStateFailed)
	}
	if !started {
		s.logger.Debug("inline runner at capacity; will retry", "run", run.RunID, "task", task.TaskID)
	}
	return nil
}

// recordTransition persists a task transition and records its metrics.
func (s *Scheduler) recordTransition(ctx context.Context, run RunState, taskID string, to domain.TaskState) error {
	from := run.States[taskID]
	if err := s.store.ApplyTransition(ctx, run.RunID, taskID, to); err != nil {
		return fmt.Errorf("applying transition for %s: %w", taskID, err)
	}
	if s.recorder != nil {
		s.recorder.RecordSchedulerDecision(string(to))
		s.recorder.RecordTaskTransition(string(from), string(to), run.DagID)
	}
	return nil
}

// findTask returns the task with the given ID from the run topology.
func findTask(tasks []domain.TaskSpec, taskID string) (domain.TaskSpec, bool) {
	for _, task := range tasks {
		if task.TaskID == taskID {
			return task, true
		}
	}
	return domain.TaskSpec{}, false
}
