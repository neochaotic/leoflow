package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakePodManager records the pod-teardown / liveness calls the reapers make, so
// the tests can assert exactly which pods were deleted and that the dispatch-lost
// reaper consulted pod liveness before failing a queued TI.
type fakePodManager struct {
	deletedTasks []deletedTask
	deletedRuns  []string
	// active maps "runID/taskID" -> whether a live pod exists; absent key is false.
	active map[string]bool
	// activeCalls counts TaskPodActive invocations, so a test can assert the live
	// (quorum) read was skipped when the presence cache already deferred.
	activeCalls int
	activeErr   error
	deleteErr   error
	deleteRunE  error
}

type deletedTask struct {
	runID  string
	taskID string
	try    int
}

func (f *fakePodManager) DeleteTaskPod(_ context.Context, runID, taskID string, try int) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deletedTasks = append(f.deletedTasks, deletedTask{runID, taskID, try})
	return nil
}

func (f *fakePodManager) DeleteRunPods(_ context.Context, runID string) error {
	if f.deleteRunE != nil {
		return f.deleteRunE
	}
	f.deletedRuns = append(f.deletedRuns, runID)
	return nil
}

func (f *fakePodManager) TaskPodActive(_ context.Context, runID, taskID string) (bool, error) {
	f.activeCalls++
	if f.activeErr != nil {
		return false, f.activeErr
	}
	return f.active[runID+"/"+taskID], nil
}

// --- Dispatch-lost reaper: K8s-aware deferral (part C) ---------------------

// TestDispatchLostReaper_DefersWhenPodLive is the #461 false-positive fix: a TI
// that has been `queued` past the threshold is NOT failed if its pod is actually
// Pending/Running — the dispatch landed, the node is just slow to pull the
// image. The invariant: never mark dispatch_lost while a live pod for the TI
// exists.
func TestDispatchLostReaper_DefersWhenPodLive(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeStaleQueuedStore{candidates: []StaleQueuedCandidate{
		{TaskInstanceID: "slow-pull", DagRunID: "run-a", TaskID: "extract", TryNumber: 1, QueuedAt: now.Add(-10 * time.Minute)},
	}}
	pods := &fakePodManager{active: map[string]bool{"run-a/extract": true}}
	r := newDispatchLostReaper(store, reapTestLogger(), 3*time.Minute, nil)
	r.pods = pods

	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run err = %v", err)
	}
	if len(store.failed) != 0 {
		t.Errorf("dispatch_lost fired on a TI with a live pod: %v", store.failed)
	}
	if len(pods.deletedTasks) != 0 {
		t.Errorf("a live TI's pod was deleted: %v", pods.deletedTasks)
	}
}

// TestDispatchLostReaper_ReapsWhenNoPod: no pod exists for a long-queued TI, so
// the dispatch is genuinely lost — mark it and (best-effort) delete any pod.
func TestDispatchLostReaper_ReapsWhenNoPod(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeStaleQueuedStore{candidates: []StaleQueuedCandidate{
		{TaskInstanceID: "lost", DagRunID: "run-a", TaskID: "extract", TryNumber: 2, QueuedAt: now.Add(-10 * time.Minute)},
	}}
	pods := &fakePodManager{active: map[string]bool{}} // no live pod
	r := newDispatchLostReaper(store, reapTestLogger(), 3*time.Minute, nil)
	r.pods = pods

	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run err = %v", err)
	}
	if len(store.failed) != 1 || store.failed[0] != "lost" {
		t.Errorf("expected the lost TI to be failed, got %v", store.failed)
	}
	if len(pods.deletedTasks) != 1 || pods.deletedTasks[0] != (deletedTask{"run-a", "extract", 2}) {
		t.Errorf("expected the reaped TI's pod (run-a/extract/try2) deleted, got %v", pods.deletedTasks)
	}
}

// TestDispatchLostReaper_DefersWhenPodQueryFails: if pod liveness cannot be
// determined (K8s API error), the reaper must DEFER rather than risk failing a
// live TI. "When in doubt, do not reap."
func TestDispatchLostReaper_DefersWhenPodQueryFails(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeStaleQueuedStore{candidates: []StaleQueuedCandidate{
		{TaskInstanceID: "unknown", DagRunID: "run-a", TaskID: "extract", TryNumber: 1, QueuedAt: now.Add(-10 * time.Minute)},
	}}
	pods := &fakePodManager{activeErr: errors.New("apiserver down")}
	r := newDispatchLostReaper(store, reapTestLogger(), 3*time.Minute, nil)
	r.pods = pods

	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run err = %v", err)
	}
	if len(store.failed) != 0 {
		t.Errorf("dispatch_lost fired despite an unknown pod state: %v", store.failed)
	}
}

// TestDispatchLostReaper_NilPodManagerFallsBack: in Lite/subprocess there is no
// K8s client, so the reaper falls back to the pure time-threshold behavior.
func TestDispatchLostReaper_NilPodManagerFallsBack(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeStaleQueuedStore{candidates: []StaleQueuedCandidate{
		{TaskInstanceID: "lost", DagRunID: "run-a", TaskID: "extract", TryNumber: 1, QueuedAt: now.Add(-10 * time.Minute)},
	}}
	r := newDispatchLostReaper(store, reapTestLogger(), 3*time.Minute, nil) // r.pods stays nil
	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run err = %v", err)
	}
	if len(store.failed) != 1 {
		t.Errorf("nil pod manager should fall back to threshold reaping, got %v", store.failed)
	}
}

// --- Agent-lost reaper: pod teardown (part A) ------------------------------

// TestAgentLostReaper_DeletesReapedPod: after failing a silent-agent TI, the
// reaper tears down its pod (the zombie the agent was running in). Only the
// reaped (run, task, try) is targeted.
func TestAgentLostReaper_DeletesReapedPod(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeHeartbeatStore{candidates: []AgentLostCandidate{
		{TaskInstanceID: "zombie", DagRunID: "run-a", TaskID: "extract", TryNumber: 2, LastHeartbeat: now.Add(-10 * time.Minute)},
	}}
	pods := &fakePodManager{}
	r := newAgentLostReaper(store, reapTestLogger(), 90*time.Second, nil)
	r.pods = pods

	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run err = %v", err)
	}
	if len(store.failed) != 1 {
		t.Fatalf("expected the silent TI failed, got %v", store.failed)
	}
	if len(pods.deletedTasks) != 1 || pods.deletedTasks[0] != (deletedTask{"run-a", "extract", 2}) {
		t.Errorf("expected reaped pod (run-a/extract/try2) deleted, got %v", pods.deletedTasks)
	}
}

// TestAgentLostReaper_NoDeleteWhenMarkFails: if the DB mark fails, the pod is
// NOT deleted — we only ever tear down a pod for a TI we durably settled.
func TestAgentLostReaper_NoDeleteWhenMarkFails(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeHeartbeatStore{
		candidates: []AgentLostCandidate{{TaskInstanceID: "x", DagRunID: "run-a", TaskID: "extract", TryNumber: 1, LastHeartbeat: now.Add(-10 * time.Minute)}},
		failErr:    errors.New("db down"),
	}
	pods := &fakePodManager{}
	r := newAgentLostReaper(store, reapTestLogger(), 90*time.Second, nil)
	r.pods = pods
	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run err = %v", err)
	}
	if len(pods.deletedTasks) != 0 {
		t.Errorf("pod deleted despite a failed DB mark: %v", pods.deletedTasks)
	}
}

// --- Orphan reaper: run-level pod teardown (part A) ------------------------

// TestOrphanReaper_DeletesRunPods: reaping an orphaned run tears down every pod
// of that run.
func TestOrphanReaper_DeletesRunPods(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeReapStore{candidates: []ReapCandidate{
		{RunID: "run-a", DagID: "etl", LastActivity: now.Add(-10 * time.Minute)},
	}}
	pods := &fakePodManager{}
	r := newOrphanReaper(store, reapTestLogger(), 5*time.Minute, nil)
	r.pods = pods

	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run err = %v", err)
	}
	if len(pods.deletedRuns) != 1 || pods.deletedRuns[0] != "run-a" {
		t.Errorf("expected run-a pods deleted, got %v", pods.deletedRuns)
	}
}
