package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
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
	// SetTaskNote attaches operational context to a task instance (shown in the
	// UI), e.g. why it is queued but not running.
	SetTaskNote(ctx context.Context, runID, taskID, note string) error
	// ReapStore methods drive the orphan reaper (#120): they list running runs
	// that have gone quiet and fail them so the dashboard counter is correct.
	ReapStore
}

// Recorder records scheduler metrics. observability.Metrics implements it.
type Recorder interface {
	RecordSchedulerDecision(decisionType string)
	RecordTaskTransition(from, to, dagID string)
	// RecordUndispatchable counts tasks queued with no executor to launch them.
	RecordUndispatchable(reason string)
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

// defaultOrphanThreshold is how long a running dag run may stay quiet before
// the reaper declares it orphaned. Five minutes is well above any healthy tick
// or task hand-off latency, so a live run is never reaped, but short enough
// that a real orphan is reaped before the operator looks at the dashboard.
const defaultOrphanThreshold = 5 * time.Minute

// Scheduler advances dag runs by applying the planning rules each tick.
type Scheduler struct {
	store           Store
	logger          *slog.Logger
	interval        time.Duration
	stepTimeout     time.Duration
	recorder        Recorder
	dispatcher      Dispatcher
	inline          InlineRunner
	orphanThreshold time.Duration
	lastTick        atomic.Int64 // unix-nano of the last loop iteration; 0 = not yet ticked
	leading         atomic.Bool  // true only while this instance holds leadership and ticks
	// warnedSchedules dedupes the "unparseable schedule" warning per DAG (keyed by
	// the offending expression) so a bad cron logs once, not every tick. Accessed
	// only from the single-threaded tick (createDueRuns), so it needs no lock.
	warnedSchedules map[string]string
}

// NewScheduler builds a Scheduler over the given store, ticking every interval.
func NewScheduler(store Store, logger *slog.Logger, interval time.Duration) *Scheduler {
	return &Scheduler{
		store:           store,
		logger:          logger,
		interval:        interval,
		stepTimeout:     defaultStepTimeout(interval),
		orphanThreshold: defaultOrphanThreshold,
		warnedSchedules: map[string]string{},
	}
}

// SetOrphanThreshold overrides the stall-detection window the reaper uses to
// declare a running dag run orphaned (optional; mainly for tests). The default
// is defaultOrphanThreshold.
func (s *Scheduler) SetOrphanThreshold(d time.Duration) { s.orphanThreshold = d }

// defaultStepTimeout bounds how long one scheduling tick may run before it is
// canceled so the loop can recover, rather than hang forever on a stuck query.
// It is generous (well above a healthy tick) to avoid aborting legitimate work.
func defaultStepTimeout(interval time.Duration) time.Duration {
	if t := 30 * interval; t > 30*time.Second {
		return t
	}
	return 30 * time.Second
}

// SetStepTimeout overrides the per-tick timeout (optional; mainly for tests).
func (s *Scheduler) SetStepTimeout(d time.Duration) { s.stepTimeout = d }

// SetLeading marks whether this instance currently holds scheduler leadership.
// The leadership manager sets it true while the loop runs and false when it
// steps down (lost lock) or stops. Becoming leader resets the tick clock so the
// startup grace applies afresh and a stale pre-step-down heartbeat is not
// mistaken for a stall. It governs Heartbeat: only a leader is expected to tick.
func (s *Scheduler) SetLeading(on bool) {
	if on {
		s.lastTick.Store(0)
	}
	s.leading.Store(on)
}

// SetRecorder attaches a metrics recorder (optional).
func (s *Scheduler) SetRecorder(r Recorder) { s.recorder = r }

// SetDispatcher attaches the executor dispatcher (optional; without it the
// scheduler advances state only and launches nothing).
func (s *Scheduler) SetDispatcher(d Dispatcher) { s.dispatcher = d }

// SetInlineRunner attaches the inline http_api runner (optional; without it
// inline http_api tasks fall back to the standard queued dispatch path).
func (s *Scheduler) SetInlineRunner(r InlineRunner) { s.inline = r }

// Run drives the scheduling loop until ctx is canceled. The loop is crash-proof:
// a panic or error in a tick is recovered and logged, so the scheduler keeps
// ticking — it may fall behind, but it never dies (the critical invariant).
func (s *Scheduler) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

// tick runs one Step under a timeout, recovering any panic so a single bad tick
// can never crash the process or stop the loop. It is the top-level backstop;
// per-run isolation in Step quarantines individual poison runs underneath it.
func (s *Scheduler) tick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("scheduler tick panic recovered", "panic", r, "stack", string(debug.Stack()))
			s.record("panic")
		}
	}()
	stepCtx, cancel := context.WithTimeout(ctx, s.stepTimeout)
	defer cancel()
	if err := s.Step(stepCtx); err != nil {
		s.logger.Error("scheduler step", "error", err)
	}
}

// record reports a scheduler decision metric, ignoring a nil recorder.
func (s *Scheduler) record(decision string) {
	if s.recorder != nil {
		s.recorder.RecordSchedulerDecision(decision)
	}
}

// Heartbeat reports whether the scheduling loop is live and when it last ticked.
// Only a leader is expected to tick, so a non-leader (a follower, or an instance
// that stepped down after losing the lock) reports healthy without ticking — it
// is correctly idle, not stalled. A leader is healthy during the startup grace
// (before its first tick) and while ticks stay within a small multiple of the
// loop interval; a stalled leader goes unhealthy so the UI/monitor surfaces it.
func (s *Scheduler) Heartbeat() (bool, time.Time) {
	if !s.leading.Load() {
		return true, time.Now().UTC()
	}
	nanos := s.lastTick.Load()
	if nanos == 0 {
		return true, time.Now().UTC()
	}
	last := time.Unix(0, nanos).UTC()
	return time.Since(last) <= 3*s.interval+time.Second, last
}

// Step runs one deterministic scheduling iteration over every active run. Each
// run is advanced in isolation (see advanceSafely): a panic or error in one
// run is contained, so it never blocks the other runs or new-run creation.
// The reaper runs independently of createDueRuns success — they share no
// dependency, and silencing the reaper when scheduling has a hiccup would let
// orphans accumulate exactly when the operator is most likely to notice the
// counter is wrong. The first non-nil infra-level error is returned (logged
// by the caller); the later phases still execute.
func (s *Scheduler) Step(ctx context.Context) error {
	s.lastTick.Store(time.Now().UnixNano())
	runs, err := s.store.ActiveRuns(ctx)
	if err != nil {
		return fmt.Errorf("listing active runs: %w", err)
	}
	for _, run := range runs {
		s.advanceSafely(ctx, run)
	}
	createErr := s.createDueRuns(ctx)
	s.reapOrphansIfLeader(ctx)
	return createErr
}

// reapOrphansIfLeader runs the orphan reaper exactly once per tick, only on the
// leader: reaping writes state and we want one writer at a time across the
// fleet. A list error is logged (not returned) because it should not stall the
// rest of the loop — the reaper is a backstop, not on the critical path.
func (s *Scheduler) reapOrphansIfLeader(ctx context.Context) {
	if !s.leading.Load() {
		return
	}
	r := newOrphanReaper(s.store, s.logger, s.orphanThreshold, s.recorder)
	if err := r.run(ctx); err != nil {
		s.logger.Error("orphan reaper", "error", err)
		s.record("orphan_list_error")
	}
}

// advanceSafely advances one run, isolating it: a panic or error in a single run
// is recovered, logged, and metered, but never aborts the tick. This keeps one
// poison run (a malformed spec, a panicking dispatcher, a transient per-run DB
// error) from stalling every other run or crashing the process — the scheduler
// may fall behind on that run, but it stays alive and keeps the rest moving.
func (s *Scheduler) advanceSafely(ctx context.Context, run RunState) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("scheduler run panic recovered",
				"run", run.RunID, "dag", run.DagID, "panic", r, "stack", string(debug.Stack()))
			s.record("panic")
		}
	}()
	if err := s.advance(ctx, run); err != nil {
		s.logger.Error("advancing run", "run", run.RunID, "dag", run.DagID, "error", err)
		s.record("run_error")
	}
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
		if domain.IsOnceSchedule(d.Schedule) {
			// @once: fire exactly one run on first sight, then never again. Once the
			// run exists, the DAG's LastLogical is non-nil and this is skipped.
			if d.LastLogical == nil {
				s.createScheduledRun(ctx, d.DagID, now)
			}
			continue
		}
		if domain.IsCronlessSchedule(d.Schedule) {
			// Manual-only or @continuous: nothing to cron-schedule — skip quietly.
			continue
		}
		// A non-empty but unparseable schedule (a 4-field cron, a typo) would
		// otherwise be swallowed silently: nextScheduledRun returns "not due" and
		// the DAG never runs, with nothing logged. Surface it (once per bad
		// expression) so the operator sees why a scheduled DAG sits idle. Compile
		// validation (domain.ValidateSchedule) catches this earlier; this is the
		// backstop for DAGs registered before the fix.
		if !scheduleParseable(d.Schedule) {
			if s.warnedSchedules[d.DagID] != d.Schedule {
				s.logger.Warn("DAG has an unparseable cron schedule; it will not run on a schedule until fixed",
					"dag", d.DagID, "schedule", d.Schedule)
				s.warnedSchedules[d.DagID] = d.Schedule
			}
			continue
		}
		logical, due := nextScheduledRun(d.Schedule, d.LastLogical, now)
		if !due {
			continue
		}
		s.createScheduledRun(ctx, d.DagID, logical)
	}
	return nil
}

// createScheduledRun creates one scheduled run for a DAG, isolating per-DAG
// failures: a single DAG's creation error is logged and metered but never blocks
// run creation for the other scheduled DAGs in this tick.
func (s *Scheduler) createScheduledRun(ctx context.Context, dagID string, logical time.Time) {
	if err := s.store.CreateScheduledRun(ctx, dagID, logical); err != nil {
		s.logger.Error("creating scheduled run", "dag", dagID, "error", err)
		s.record("create_run_error")
		return
	}
	s.record("create_run")
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
		return s.recordTransition(ctx, run, t.TaskID, domain.TaskStateQueued)
	}
	return s.failUndispatchable(ctx, run, t.TaskID, task.Type)
}

// failUndispatchable fails a task that has no executor to run it (pod dispatch
// disabled and it is not an inline http_api task). The condition is
// deterministic for the process lifetime, so failing fast — with the reason on
// the task note for the UI — beats leaving the run "running" forever (#46, #50).
func (s *Scheduler) failUndispatchable(ctx context.Context, run RunState, taskID string, taskType domain.TaskType) error {
	s.logger.Warn("task has no available executor; failing it (it can never run)",
		"run", run.RunID, "dag", run.DagID, "task", taskID, "task_type", taskType,
		"reason", "no_executor",
		"hint", "pod dispatch disabled (no kubeconfig) or no executor handles this task type")
	if s.recorder != nil {
		s.recorder.RecordUndispatchable("no_executor")
	}
	note := fmt.Sprintf("Failed: no executor available for a %q task. "+
		"Pod dispatch is disabled (no Kubernetes config); only inline http_api tasks run without it.", taskType)
	if nerr := s.store.SetTaskNote(ctx, run.RunID, taskID, note); nerr != nil {
		s.logger.Warn("setting task note", "run", run.RunID, "task", taskID, "error", nerr)
	}
	return s.recordTransition(ctx, run, taskID, domain.TaskStateFailed)
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
