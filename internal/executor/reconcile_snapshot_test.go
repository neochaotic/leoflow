package executor

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// fakeSnapshotter serves a fixed pod set in place of a live LIST, so a test can
// prove the reconciler reads its terminal-pod set from the cache snapshot.
type fakeSnapshotter struct {
	pods []*corev1.Pod
	err  error
}

func (f *fakeSnapshotter) SnapshotTaskPods() ([]*corev1.Pod, error) { return f.pods, f.err }

// TestReconcile_FromSnapshot_AttemptGuarded: with a PodSnapshotter wired, the
// reconciler classifies and settles from the cache snapshot rather than a live
// LIST, and every settle still carries the pod's try-number (ADR 0052 attempt
// guard). The live clientset is deliberately empty, so a settle can only come
// from the snapshot — proving the 30s LIST was swapped for the cache read.
func TestReconcile_FromSnapshot_AttemptGuarded(t *testing.T) {
	cs := fake.NewClientset() // no pods live: a live LIST would settle nothing
	reporter := &fakeReporter{}
	r := NewReconciler(cs, "leoflow", reporter)

	failed := managedPod("p-fail", "ti-fail", corev1.PodFailed)
	failed.Labels["leoflow.io/try-number"] = "3"
	r.SetPodSnapshotter(&fakeSnapshotter{pods: []*corev1.Pod{
		failed,
		managedPod("p-run", "ti-run", corev1.PodRunning), // not terminal: no settle
	}})

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got, ok := reporter.settled["ti-fail"]
	if !ok || got.kind != settleFailed {
		t.Fatalf("snapshot's failed pod must settle failed, got %+v (ok=%v)", got, ok)
	}
	if got.tryNumber != 3 {
		t.Errorf("settle must be attempt-guarded on the pod's try-number, got %d", got.tryNumber)
	}
	if len(reporter.settled) != 1 {
		t.Errorf("only the terminal pod should settle, got %v", reporter.settled)
	}
}

// TestReconcile_SnapshotError_Surfaces: a snapshot error (e.g. cache not synced)
// is returned so the reconciler retries next tick instead of acting on a cold or
// partial view.
func TestReconcile_SnapshotError_Surfaces(t *testing.T) {
	r := NewReconciler(fake.NewClientset(), "leoflow", &fakeReporter{})
	r.SetPodSnapshotter(&fakeSnapshotter{err: errCacheNotSynced})
	if err := r.Reconcile(context.Background()); err == nil {
		t.Error("a snapshot error must surface so the tick retries")
	}
}
