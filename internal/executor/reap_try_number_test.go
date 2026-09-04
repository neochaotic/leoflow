package executor

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// The tests in this file lock issue #723: the reapers' liveness gate must ask
// about the exact ATTEMPT it is about to fail, not the (run, task) as a whole.
//
// The retry rail resets up_for_retry -> none with try_number = try_number + 1
// (storage/queries/runs.sql) and the planner re-queues the TI, so a `queued` (or
// `running`) TI can legitimately be on try 2 while try 1's pod still lingers
// Pending (unschedulable / image-pull backoff) after a best-effort delete failed.
// A liveness selector pinned only to (run, task) then matches the stale try-1 pod
// and false-defers forever — the wedge #723 describes. Pinning try-number makes
// the reaper ask about try 2's pod, which is genuinely gone, so the reap proceeds.
//
// Each test wires the REAL executor / informer against a fake cluster holding
// only the older attempt's pod, so it exercises the actual label selector rather
// than a hand-rolled fake predicate.

// TestDispatchLostReaper_TryNumberPinned_LiveRead is the #723 lock on the
// dispatch-lost/queued path via the live TaskPodPresence read: a try-1 pod lingers
// Pending, but the dispatch-lost candidate is on try 2. Try 2's dispatch is
// genuinely lost, so the reaper MUST fail it as dispatch_lost — it must not defer
// on the stale try-1 pod.
func TestDispatchLostReaper_TryNumberPinned_LiveRead(t *testing.T) {
	now := time.Now().UTC()
	cs := fake.NewSimpleClientset(
		taskPod("try1-pending", "run-a", "extract", 1, corev1.PodPending), // stale older attempt
	)
	store := &fakeStaleQueuedStore{candidates: []StaleQueuedCandidate{
		{TaskInstanceID: "stuck", DagRunID: "run-a", TaskID: "extract", TryNumber: 2, QueuedAt: now.Add(-10 * time.Minute)},
	}}
	r := newDispatchLostReaper(store, reapTestLogger(), 3*time.Minute, nil)
	r.pods = NewKubernetesExecutor(cs, "leoflow")

	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.failed) != 1 || store.failed[0] != "stuck" {
		t.Fatalf("#723: try-2 dispatch is lost (only try-1 pod lingers) — TI must be failed dispatch_lost, got %v", store.failed)
	}
}

// TestDispatchLostReaper_TryNumberPinned_CacheRead is the #723 lock on the same
// path via the informer cache fast-path (CachedPodActive). The cache holds only
// the stale try-1 pod; the candidate is try 2. The cache lookup must be pinned to
// try 2, miss, and fall through to the live read (also try-2-absent) so the reap
// proceeds — the cache branch must not false-defer on the older attempt.
func TestDispatchLostReaper_TryNumberPinned_CacheRead(t *testing.T) {
	now := time.Now().UTC()
	cs := fake.NewClientset(
		taskPod("try1-pending", "run-a", "extract", 1, corev1.PodPending),
	)
	pi := NewPodInformer(cs, "leoflow")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pi.Start(ctx)
	if !pi.WaitForCacheSync(ctx) {
		t.Fatal("cache did not sync")
	}
	store := &fakeStaleQueuedStore{candidates: []StaleQueuedCandidate{
		{TaskInstanceID: "stuck", DagRunID: "run-a", TaskID: "extract", TryNumber: 2, QueuedAt: now.Add(-10 * time.Minute)},
	}}
	r := newDispatchLostReaper(store, reapTestLogger(), 3*time.Minute, nil)
	r.pods = NewKubernetesExecutor(cs, "leoflow")
	r.cache = pi

	if err := r.run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.failed) != 1 || store.failed[0] != "stuck" {
		t.Fatalf("#723: cache fast-path must pin try-number and not defer on the stale try-1 pod, got %v", store.failed)
	}
}

// TestPodLostReaper_TryNumberPinned_LiveRead is the #723 lock on the pod-lost/
// running path via the live read: a try-1 pod lingers Running, but the running
// candidate is on try 2 whose pod is genuinely gone. The reaper must fail it as
// pod_lost rather than false-defer on the stale try-1 pod.
func TestPodLostReaper_TryNumberPinned_LiveRead(t *testing.T) {
	past := time.Now().UTC().Add(-2 * time.Minute)
	cs := fake.NewSimpleClientset(
		taskPod("try1-running", "run-a", "work", 1, corev1.PodRunning), // stale older attempt
	)
	store := &fakePodLostStore{candidates: []PodLostCandidate{
		{TaskInstanceID: "lost", DagRunID: "run-a", TaskID: "work", TryNumber: 2, RunningSince: past},
	}}
	r := newPodLostReaper(store, reapTestLogger(), 60*time.Second, nil)
	r.pods = NewKubernetesExecutor(cs, "leoflow")

	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.marked) != 1 || store.marked[0] != "lost" {
		t.Fatalf("#723: try-2 running pod is gone (only try-1 pod lingers) — TI must be failed pod_lost, got %v", store.marked)
	}
}

// TestPodLostReaper_TryNumberPinned_CacheRead is the #723 lock on the pod-lost
// path via the informer cache: the cache holds only the stale try-1 pod, the
// candidate is try 2. The cache lookup must pin try 2, miss, and fall through to
// the live read so the reap proceeds.
func TestPodLostReaper_TryNumberPinned_CacheRead(t *testing.T) {
	past := time.Now().UTC().Add(-2 * time.Minute)
	cs := fake.NewClientset(
		taskPod("try1-running", "run-a", "work", 1, corev1.PodRunning),
	)
	pi := NewPodInformer(cs, "leoflow")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pi.Start(ctx)
	if !pi.WaitForCacheSync(ctx) {
		t.Fatal("cache did not sync")
	}
	store := &fakePodLostStore{candidates: []PodLostCandidate{
		{TaskInstanceID: "lost", DagRunID: "run-a", TaskID: "work", TryNumber: 2, RunningSince: past},
	}}
	r := newPodLostReaper(store, reapTestLogger(), 60*time.Second, nil)
	r.pods = NewKubernetesExecutor(cs, "leoflow")
	r.cache = pi

	if err := r.run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.marked) != 1 || store.marked[0] != "lost" {
		t.Fatalf("#723: cache fast-path must pin try-number and not defer on the stale try-1 pod, got %v", store.marked)
	}
}
