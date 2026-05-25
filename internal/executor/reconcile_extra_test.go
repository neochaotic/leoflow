package executor

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// TestPodFailureReasonSurfacesCause asserts the reconciler surfaces a precise,
// actionable failure reason — what an operator sees on a pod the agent never
// reported (OOMKilled, Evicted, image-pull failures), with a generic fallback.
func TestPodFailureReasonSurfacesCause(t *testing.T) {
	withReason := func(r string) *corev1.Pod {
		return &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodFailed, Reason: r}}
	}
	cases := map[string]struct {
		pod  *corev1.Pod
		want string
	}{
		"evicted":          {withReason("Evicted"), "Evicted"},
		"oom":              {withReason("OOMKilled"), "OOMKilled"},
		"failed no reason": {podPhase(corev1.PodFailed), "pod failed"},
		"image pull":       {podWaiting("ImagePullBackOff"), "ImagePullBackOff"},
		"invalid image":    {podWaiting("InvalidImageName"), "InvalidImageName"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, reason := classifyPod(c.pod); reason != c.want {
				t.Errorf("reason = %q, want %q", reason, c.want)
			}
		})
	}
}

// TestReconcileSkipsPodWithoutTaskInstance: a failed pod with no
// task-instance-id annotation cannot be mapped to a task, so it must be skipped
// (no FailTask, no panic) rather than reported against an empty id.
func TestReconcileSkipsPodWithoutTaskInstance(t *testing.T) {
	cs := fake.NewClientset(managedPod("p-fail", "", corev1.PodFailed)) // empty tiID
	reporter := &fakeReporter{}
	r := NewReconciler(cs, "leoflow", reporter)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(reporter.failed) != 0 {
		t.Errorf("a pod without a task-instance annotation must not be reported, got %v", reporter.failed)
	}
}

type errReporter struct{}

func (errReporter) FailTask(context.Context, string, string) error {
	return errors.New("metadatabase unavailable")
}

// TestReconcileToleratesReporterError: a failure to record one pod's failure must
// not abort the reconcile (the next tick retries; other pods still process).
func TestReconcileToleratesReporterError(t *testing.T) {
	cs := fake.NewClientset(managedPod("p-fail", "ti1", corev1.PodFailed))
	r := NewReconciler(cs, "leoflow", errReporter{})
	if err := r.Reconcile(context.Background()); err != nil {
		t.Errorf("a reporter error must not fail the reconcile, got %v", err)
	}
}

// TestReconcileToleratesGCDeleteError: a failure to garbage-collect a finished
// pod must be logged, not fatal — the reconcile still completes.
func TestReconcileToleratesGCDeleteError(t *testing.T) {
	now := time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC)
	cs := fake.NewClientset(agedPod("old-success", corev1.PodSucceeded, now.Add(-30*time.Minute)))
	cs.PrependReactor("delete", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("delete forbidden")
	})
	r := NewReconciler(cs, "leoflow", &fakeReporter{})
	r.now = func() time.Time { return now }
	r.ttl = 10 * time.Minute
	if err := r.Reconcile(context.Background()); err != nil {
		t.Errorf("a GC delete error must not fail the reconcile, got %v", err)
	}
}
