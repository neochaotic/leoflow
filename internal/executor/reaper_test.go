package executor

import (
	"context"
	"sync"
	"testing"
	"time"
)

// capturingRecorder records every scheduler decision the reapers meter, so a
// test can assert on the decision counters. It satisfies DecisionRecorder.
// Concurrency-safe to match the production recorder.
type capturingRecorder struct {
	mu        sync.Mutex
	decisions []string
}

func (r *capturingRecorder) RecordSchedulerDecision(d string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.decisions = append(r.decisions, d)
}

func (r *capturingRecorder) count(decision string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, d := range r.decisions {
		if d == decision {
			n++
		}
	}
	return n
}

// fakeReaperStore satisfies the full ReaperStore (all four reap interfaces), so
// a test can drive NewReaper + ReapOnce end-to-end and assert which candidates
// each reaper marked. Empty candidate slices make a reaper a no-op.
type fakeReaperStore struct {
	orphanCands  []ReapCandidate
	reapedRuns   []string
	agentCands   []AgentLostCandidate
	agentMarked  []string
	queuedCands  []StaleQueuedCandidate
	queuedMarked []string
	runningCands []PodLostCandidate
	podMarked    []string
	warmBound    []WarmBoundTI
}

func (f *fakeReaperStore) ListReapCandidates(context.Context) ([]ReapCandidate, error) {
	return f.orphanCands, nil
}
func (f *fakeReaperStore) ReapRun(_ context.Context, runID string) error {
	f.reapedRuns = append(f.reapedRuns, runID)
	return nil
}
func (f *fakeReaperStore) ListAgentLostCandidates(context.Context) ([]AgentLostCandidate, error) {
	return f.agentCands, nil
}
func (f *fakeReaperStore) MarkTaskAgentLost(_ context.Context, tiID string) (bool, error) {
	f.agentMarked = append(f.agentMarked, tiID)
	return true, nil
}
func (f *fakeReaperStore) ListStaleQueuedCandidates(context.Context) ([]StaleQueuedCandidate, error) {
	return f.queuedCands, nil
}
func (f *fakeReaperStore) MarkTaskDispatchLost(_ context.Context, tiID string) error {
	f.queuedMarked = append(f.queuedMarked, tiID)
	return nil
}
func (f *fakeReaperStore) ListRunningTasks(context.Context) ([]PodLostCandidate, error) {
	return f.runningCands, nil
}
func (f *fakeReaperStore) MarkTaskPodLost(_ context.Context, tiID string) (bool, error) {
	f.podMarked = append(f.podMarked, tiID)
	return true, nil
}
func (f *fakeReaperStore) ListWarmBoundRunningTIs(context.Context) ([]WarmBoundTI, error) {
	return f.warmBound, nil
}

// TestReaperReapOnceDrivesReapers is the aggregate-seam contract, the executor
// side of what used to be TestStepReapsOrphanRunsOnLeader /
// TestStepRunsDispatchLostReaperOnLeader in the scheduler package: NewReaper
// wires the four reapers and ReapOnce runs them, so a stale orphan run and a
// stale queued TI are both reaped in one call. Nil pods (Lite) exercises the
// DB-only path — the orphan reaper still reaps and the dispatch-lost reaper
// falls back to its pure time threshold.
func TestReaperReapOnceDrivesReapers(t *testing.T) {
	past := time.Now().UTC().Add(-1 * time.Hour)
	store := &fakeReaperStore{
		orphanCands: []ReapCandidate{{RunID: "stuck-run", DagID: "etl", LastActivity: past}},
		queuedCands: []StaleQueuedCandidate{{TaskInstanceID: "stuck-ti", QueuedAt: past}},
	}
	rec := &capturingRecorder{}
	r := NewReaper(store, nil, nil, nil, rec, reapTestLogger(), DefaultReaperConfig(), nil)

	if err := r.ReapOnce(context.Background()); err != nil {
		t.Fatalf("ReapOnce: %v", err)
	}
	if len(store.reapedRuns) != 1 || store.reapedRuns[0] != "stuck-run" {
		t.Errorf("orphan reaper must reap stuck-run, got %v", store.reapedRuns)
	}
	if len(store.queuedMarked) != 1 || store.queuedMarked[0] != "stuck-ti" {
		t.Errorf("dispatch-lost reaper must fail stuck-ti, got %v", store.queuedMarked)
	}
}

// TestReaperThreadsPresenceCacheToDispatchLost is the wiring check re-homed from
// the scheduler's TestStepThreadsPresenceCacheToReapers: a presence cache passed
// to NewReaper must reach the dispatch-lost reaper. A stale queued TI whose pod
// the cache shows as active is deferred, so ReapOnce does NOT fail it — proving
// the cache is threaded, not dropped on the floor.
func TestReaperThreadsPresenceCacheToDispatchLost(t *testing.T) {
	store := &fakeReaperStore{
		queuedCands: []StaleQueuedCandidate{
			{TaskInstanceID: "slow", DagRunID: "run-a", TaskID: "work", QueuedAt: time.Now().UTC().Add(-10 * time.Minute)},
		},
	}
	pods := &fakePodManager{active: map[string]bool{}}
	cache := &fakePresenceCache{active: map[string]bool{"run-a/work": true}}
	r := NewReaper(store, pods, cache, nil, nil, reapTestLogger(), DefaultReaperConfig(), nil)

	if err := r.ReapOnce(context.Background()); err != nil {
		t.Fatalf("ReapOnce: %v", err)
	}
	if len(store.queuedMarked) != 0 {
		t.Errorf("a cache-active queued TI must be deferred through ReapOnce, got %v", store.queuedMarked)
	}
	if pods.activeCalls != 0 {
		t.Errorf("a cache-active defer must skip the live LIST, got %d live calls", pods.activeCalls)
	}
}
