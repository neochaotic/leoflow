package scheduler

import (
	"bytes"
	"context"
	"errors"
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
	runs               []RunState
	materialize        []string
	transitions        []transition
	retried            []transition
	runStates          map[string]domain.DagRunState
	scheduled          []ScheduledDAG
	createdRuns        []string
	notes              map[string]string
	reapCands          []ReapCandidate
	reaped             []string
	createErr          bool
	agentLostCands     []AgentLostCandidate
	agentLostMarked    []string
	staleQueuedCands   []StaleQueuedCandidate
	dispatchLostMarked []string
	// activeRunsCalls counts ActiveRuns invocations so the follower-side gate
	// can be asserted: a follower must NOT read run state (single-writer
	// invariant, ADR 0031 / issue #208).
	activeRunsCalls int
}

func newFakeStore(runs ...RunState) *fakeStore {
	return &fakeStore{runs: runs, runStates: map[string]domain.DagRunState{}}
}

func (f *fakeStore) ActiveRuns(context.Context) ([]RunState, error) {
	f.activeRunsCalls++
	return f.runs, nil
}
func (f *fakeStore) ScheduledDAGs(context.Context) ([]ScheduledDAG, error) {
	return f.scheduled, nil
}
func (f *fakeStore) CreateScheduledRun(_ context.Context, dagID string, _ time.Time) error {
	if f.createErr {
		return errors.New("create scheduled run failed")
	}
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
func (f *fakeStore) ListReapCandidates(context.Context) ([]ReapCandidate, error) {
	return f.reapCands, nil
}
func (f *fakeStore) ReapRun(_ context.Context, runID string) error {
	f.reaped = append(f.reaped, runID)
	return nil
}
func (f *fakeStore) ListAgentLostCandidates(context.Context) ([]AgentLostCandidate, error) {
	return f.agentLostCands, nil
}
func (f *fakeStore) MarkTaskAgentLost(_ context.Context, tiID string) error {
	f.agentLostMarked = append(f.agentLostMarked, tiID)
	return nil
}
func (f *fakeStore) ListStaleQueuedCandidates(context.Context) ([]StaleQueuedCandidate, error) {
	return f.staleQueuedCands, nil
}
func (f *fakeStore) MarkTaskDispatchLost(_ context.Context, tiID string) error {
	f.dispatchLostMarked = append(f.dispatchLostMarked, tiID)
	return nil
}

func newScheduler(store Store) *Scheduler {
	s := NewScheduler(store, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Millisecond)
	// Default tests to leader so the writer-path assertions (which is most of
	// them) execute. Follower-mode tests opt out with `s.SetLeading(false)`.
	// Without this default every Step() returns early at the leadership gate
	// (#208) and the test asserts on a no-op tick.
	s.SetLeading(true)
	return s
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

// TestStepReapsOrphanRunsOnLeader covers the #120 fix end-to-end at the
// scheduler layer: a leader tick must reap any candidate older than the orphan
// threshold (default 5 min), turning a stuck `running` dag run into `failed` so
// the dashboard's "Dags em Execução" gauge drops back to zero. Fresh candidates
// stay untouched.
func TestStepReapsOrphanRunsOnLeader(t *testing.T) {
	store := newFakeStore()
	now := time.Now().UTC()
	store.reapCands = []ReapCandidate{
		{RunID: "stuck", DagID: "etl", LastActivity: now.Add(-1 * time.Hour)},
		{RunID: "fresh", DagID: "etl", LastActivity: now.Add(-30 * time.Second)},
	}
	s := newScheduler(store)
	s.SetLeading(true)
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.reaped) != 1 || store.reaped[0] != "stuck" {
		t.Errorf("reaped = %v, want [stuck]", store.reaped)
	}
}

// TestStepReapsEvenIfCreateDueRunsFails covers an important resilience guard:
// the reaper is a backstop, so it must run even when the rest of the tick is
// degraded (a DB hiccup on createScheduledRun, for example). The reverse — a
// sick DB hiding orphans precisely when you want to see them — would be a
// silent failure mode the dashboard counter would never recover from.
func TestStepReapsEvenIfCreateDueRunsFails(t *testing.T) {
	store := newFakeStore()
	store.reapCands = []ReapCandidate{
		{RunID: "stuck", LastActivity: time.Now().UTC().Add(-1 * time.Hour)},
	}
	last := time.Now().UTC().Add(-2 * time.Hour)
	store.scheduled = []ScheduledDAG{{DagID: "etl", Schedule: "@hourly", LastLogical: &last}}
	store.createErr = true
	s := newScheduler(store)
	s.SetLeading(true)
	_ = s.Step(context.Background())
	if len(store.reaped) != 1 || store.reaped[0] != "stuck" {
		t.Errorf("reaper must run even when createScheduledRun fails; reaped = %v", store.reaped)
	}
}

// TestStepDoesNotReapOnFollower: a follower (or an instance that lost the
// lock) must not write — reaping is a state-changing operation reserved for the
// leader. The followers tick to keep their heartbeat path warm but skip the
// reaper.
func TestStepDoesNotReapOnFollower(t *testing.T) {
	store := newFakeStore()
	store.reapCands = []ReapCandidate{{RunID: "stuck", LastActivity: time.Now().UTC().Add(-1 * time.Hour)}}
	s := newScheduler(store)
	s.SetLeading(false) // newScheduler defaults to leader; this test wants follower.
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.reaped) != 0 {
		t.Errorf("follower must not reap; got %v", store.reaped)
	}
}

// TestStepFollowerSkipsAllWrites: a follower (no leadership lock) MUST NOT read
// or write scheduler state. Today only the reaper is gated; ActiveRuns and
// createDueRuns also run on every follower, violating the single-writer
// invariant of ADR 0031. The fix lifts the leader gate to the top of Step()
// after the heartbeat store (issue #208).
func TestStepFollowerSkipsAllWrites(t *testing.T) {
	store := newFakeStore(RunState{
		RunID: "r1", DagID: "etl", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States: map[string]domain.TaskState{"a": domain.TaskStateNone, "b": domain.TaskStateNone},
		Tries:  map[string]int{"a": 1, "b": 1}, MaxTries: map[string]int{"a": 3, "b": 3},
	})
	store.scheduled = []ScheduledDAG{{DagID: "etl", Schedule: "@hourly"}}
	store.reapCands = []ReapCandidate{{RunID: "stuck", LastActivity: time.Now().UTC().Add(-1 * time.Hour)}}
	s := newScheduler(store)
	s.SetLeading(false) // newScheduler defaults to leader; opt out for this follower-mode test.

	if err := s.Step(context.Background()); err != nil {
		t.Fatalf("Step on follower must not return infra-level error: %v", err)
	}
	if store.activeRunsCalls != 0 {
		t.Errorf("follower must not read ActiveRuns; got %d calls", store.activeRunsCalls)
	}
	if len(store.transitions) != 0 {
		t.Errorf("follower must not write transitions; got %d", len(store.transitions))
	}
	if len(store.createdRuns) != 0 {
		t.Errorf("follower must not create scheduled runs; got %v", store.createdRuns)
	}
	if len(store.reaped) != 0 {
		t.Errorf("follower must not reap; got %v", store.reaped)
	}
	// Heartbeat MUST still fire — the leadership check sits AFTER lastTick.Store
	// so the orchestrator can prove the instance is alive without granting it
	// the writer role.
	if s.lastTick.Load() == 0 {
		t.Errorf("follower must still update lastTick (heartbeat); got 0")
	}
}

// TestStepCatchupBackfillsMissedSlots: a DAG with catchup=true whose leader
// was down for 6 hours produces 6 hourly runs on the next tick (#129).
// Without catchup wiring, the legacy single-run path silently dropped the 5
// intermediate slots.
func TestStepCatchupBackfillsMissedSlots(t *testing.T) {
	last := time.Now().UTC().Add(-6 * time.Hour).Truncate(time.Hour)
	store := newFakeStore()
	store.scheduled = []ScheduledDAG{{
		DagID: "etl", Schedule: "@hourly", LastLogical: &last, Catchup: true,
	}}
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The exact count varies by the wall clock fraction within the current
	// hour; assert "more than 1" to pin the catchup behavior without
	// flakiness — 1 means catchup did not trigger.
	if len(store.createdRuns) < 2 {
		t.Errorf("catchup=true with 6h gap should create multiple runs, got %d (%v)",
			len(store.createdRuns), store.createdRuns)
	}
}

// TestStepCatchupFalseCreatesOnlyLatest: same gap, catchup=false → exactly
// one run is created (the most recent slot). This is the operator-opt-out
// for "I don't want SLA misses backfilled."
func TestStepCatchupFalseCreatesOnlyLatest(t *testing.T) {
	last := time.Now().UTC().Add(-6 * time.Hour).Truncate(time.Hour)
	store := newFakeStore()
	store.scheduled = []ScheduledDAG{{
		DagID: "etl", Schedule: "@hourly", LastLogical: &last, Catchup: false,
	}}
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.createdRuns) != 1 || store.createdRuns[0] != "etl" {
		t.Errorf("catchup=false should create exactly one run, got %v", store.createdRuns)
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
	// last == now, so the next @hourly slot is strictly after now and the DAG is
	// not due. Using now-1min was clock-dependent: when the test ran within a
	// minute after the top of the hour, the just-passed hour boundary fell inside
	// [last, now] and the DAG became "due", flaking CI (it ran at 02:01).
	recent := time.Now().UTC()
	store := newFakeStore()
	store.scheduled = []ScheduledDAG{{DagID: "etl", Schedule: "@hourly", LastLogical: &recent}}
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.createdRuns) != 0 {
		t.Errorf("no run should be created when not due, got %v", store.createdRuns)
	}
}

func TestStepOnceScheduleFiresExactlyOnce(t *testing.T) {
	// @once with no prior run -> create exactly one run.
	store := newFakeStore()
	store.scheduled = []ScheduledDAG{{DagID: "once_dag", Schedule: "@once"}}
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.createdRuns) != 1 || store.createdRuns[0] != "once_dag" {
		t.Errorf("@once with no prior run should create exactly one run, got %v", store.createdRuns)
	}

	// @once that has ALREADY run (LastLogical set) -> create nothing more.
	ran := time.Now().UTC().Add(-time.Hour)
	store2 := newFakeStore()
	store2.scheduled = []ScheduledDAG{{DagID: "once_dag", Schedule: "@once", LastLogical: &ran}}
	if err := newScheduler(store2).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store2.createdRuns) != 0 {
		t.Errorf("@once that already ran must not run again, got %v", store2.createdRuns)
	}
}

// TestStepRespectsMaxActiveRunsWhenAtCap: with max_active_runs=1 and one
// already-active run for that DAG, even a backfill that would otherwise
// produce many catchup slots must produce zero new runs — the cap is a
// hard ceiling on concurrent active runs per DAG (Airflow #200).
//
// The fake run carries Tasks so PlanRun/FinalizeRun see a non-terminal
// topology and do not auto-finalize it during this tick (which would change
// the snapshot the cap is checked against).
func TestStepRespectsMaxActiveRunsWhenAtCap(t *testing.T) {
	last := time.Now().UTC().Add(-6 * time.Hour).Truncate(time.Hour)
	store := newFakeStore(RunState{
		RunID: "r-existing", DagID: "etl", State: domain.DagRunStateRunning,
		Tasks:  linearTasks(),
		States: map[string]domain.TaskState{"a": domain.TaskStateRunning, "b": domain.TaskStateNone},
	})
	store.scheduled = []ScheduledDAG{{
		DagID: "etl", Schedule: "@hourly", LastLogical: &last,
		Catchup: true, MaxActiveRuns: 1,
	}}
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.createdRuns) != 0 {
		t.Errorf("DAG already at max_active_runs cap must create no new run, got %v", store.createdRuns)
	}
}

// TestStepOnceScheduleIgnoresMaxActiveRuns covers the documented
// invariant after dropping the dead headroom check on the @once branch:
// @once is single-shot regardless of cap. LastLogical=nil means "never
// fired"; the cap is irrelevant because @once will never compete with
// itself.
func TestStepOnceScheduleIgnoresMaxActiveRuns(t *testing.T) {
	store := newFakeStore()
	store.scheduled = []ScheduledDAG{{
		DagID: "once_dag", Schedule: "@once", MaxActiveRuns: 1,
	}}
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.createdRuns) != 1 || store.createdRuns[0] != "once_dag" {
		t.Errorf("@once must fire its single run regardless of cap, got %v", store.createdRuns)
	}
}

// TestStepRespectsMaxActiveRunsCapsBackfill: same backfill window but no
// existing active runs and max_active_runs=1 — at most ONE new run should be
// created this tick (the remaining slots are skipped, not queued).
func TestStepRespectsMaxActiveRunsCapsBackfill(t *testing.T) {
	last := time.Now().UTC().Add(-6 * time.Hour).Truncate(time.Hour)
	store := newFakeStore()
	store.scheduled = []ScheduledDAG{{
		DagID: "etl", Schedule: "@hourly", LastLogical: &last,
		Catchup: true, MaxActiveRuns: 1,
	}}
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.createdRuns) != 1 {
		t.Errorf("max_active_runs=1 should cap backfill at one new run, got %d (%v)",
			len(store.createdRuns), store.createdRuns)
	}
}

// TestStepMaxActiveRunsZeroMeansUnlimited: cap=0 preserves the legacy
// behavior (Airflow-faithful: a missing limit is unlimited). Backfill
// proceeds as if there were no cap.
func TestStepMaxActiveRunsZeroMeansUnlimited(t *testing.T) {
	last := time.Now().UTC().Add(-6 * time.Hour).Truncate(time.Hour)
	store := newFakeStore()
	store.scheduled = []ScheduledDAG{{
		DagID: "etl", Schedule: "@hourly", LastLogical: &last,
		Catchup: true, MaxActiveRuns: 0,
	}}
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.createdRuns) < 2 {
		t.Errorf("max_active_runs=0 (unlimited) should backfill the whole gap, got %d (%v)",
			len(store.createdRuns), store.createdRuns)
	}
}

func TestStepWarnsOnUnparseableScheduleAndCreatesNoRun(t *testing.T) {
	store := newFakeStore()
	// A 4-field cron (the real bug): the scheduler must not silently ignore it.
	store.scheduled = []ScheduledDAG{{DagID: "etl", Schedule: "*/3 * * *"}}
	var buf bytes.Buffer
	s := NewScheduler(store, slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})), time.Millisecond)
	s.SetLeading(true) // direct NewScheduler call — needs explicit leader so createDueRuns runs (#208 gate).

	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.createdRuns) != 0 {
		t.Errorf("a bad schedule must create no run, got %v", store.createdRuns)
	}
	if !strings.Contains(buf.String(), "unparseable") || !strings.Contains(buf.String(), "*/3 * * *") {
		t.Errorf("expected a WARN naming the bad schedule, got: %q", buf.String())
	}
	// A second tick with the same bad schedule must not re-warn (deduped).
	buf.Reset()
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "unparseable") {
		t.Errorf("the warning should be deduped per expression, but it logged again: %q", buf.String())
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

type fakeRecorder struct {
	undispatchable   []string
	stepDowns        map[string]int
	reacquireSamples []time.Duration
}

func (r *fakeRecorder) RecordSchedulerDecision(string)      {}
func (r *fakeRecorder) RecordTaskTransition(_, _, _ string) {}
func (r *fakeRecorder) RecordUndispatchable(reason string) {
	r.undispatchable = append(r.undispatchable, reason)
}
func (r *fakeRecorder) RecordSchedulerStepDown(reason string) {
	if r.stepDowns == nil {
		r.stepDowns = map[string]int{}
	}
	r.stepDowns[reason]++
}
func (r *fakeRecorder) ObserveSchedulerReacquire(d time.Duration) {
	r.reacquireSamples = append(r.reacquireSamples, d)
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
