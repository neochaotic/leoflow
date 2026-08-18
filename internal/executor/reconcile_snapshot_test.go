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

// TestReconcile_SnapshotError_FallsBackToLiveList: a snapshot error (e.g. the
// informer never synced — RBAC drift, a broken watch) must NOT stall the
// reconciler. It degrades to the authoritative live LIST, so ADR-0052
// lost-outcome recovery and finished-pod GC keep running during an informer
// outage instead of being silently disabled every tick.
func TestReconcile_SnapshotError_FallsBackToLiveList(t *testing.T) {
	failed := managedPod("p-fail", "ti-fail", corev1.PodFailed) // live: the fallback LIST must find it
	reporter := &fakeReporter{}
	r := NewReconciler(fake.NewClientset(failed), "leoflow", reporter)
	r.SetPodSnapshotter(&fakeSnapshotter{err: errCacheNotSynced})

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("a snapshot error must fall back to the live LIST, not surface, got %v", err)
	}
	if got, ok := reporter.settled["ti-fail"]; !ok || got.kind != settleFailed {
		t.Fatalf("on a snapshot error the reconciler must fall back to the live LIST and still settle the terminal pod, got %+v (ok=%v)", got, ok)
	}
}
