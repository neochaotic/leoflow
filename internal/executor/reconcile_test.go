package executor

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func podPhase(phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{Status: corev1.PodStatus{Phase: phase}}
}

func podWaiting(reason string) *corev1.Pod {
	return &corev1.Pod{Status: corev1.PodStatus{
		Phase: corev1.PodPending,
		ContainerStatuses: []corev1.ContainerStatus{
			{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason}}},
		},
	}}
}

func TestClassifyPod(t *testing.T) {
	cases := map[string]struct {
		pod  *corev1.Pod
		want podOutcome
	}{
		"failed phase":       {podPhase(corev1.PodFailed), podFailed},
		"succeeded phase":    {podPhase(corev1.PodSucceeded), podSucceeded},
		"running":            {podPhase(corev1.PodRunning), podPending},
		"pending":            {podPhase(corev1.PodPending), podPending},
		"image pull backoff": {podWaiting("ImagePullBackOff"), podFailed},
		"err image pull":     {podWaiting("ErrImagePull"), podFailed},
		"crash loop":         {podWaiting("CrashLoopBackOff"), podFailed},
		"benign waiting":     {podWaiting("ContainerCreating"), podPending},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got, _ := classifyPod(c.pod); got != c.want {
				t.Errorf("classifyPod = %v, want %v", got, c.want)
			}
		})
	}
}

type fakeReporter struct{ failed map[string]string }

func (f *fakeReporter) FailTask(_ context.Context, taskInstanceID, reason string) error {
	if f.failed == nil {
		f.failed = map[string]string{}
	}
	f.failed[taskInstanceID] = reason
	return nil
}

func managedPod(name, tiID string, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   "leoflow",
			Labels:      map[string]string{"leoflow.io/run-id": "r1"},
			Annotations: map[string]string{"leoflow.io/task-instance-id": tiID},
		},
		Status: corev1.PodStatus{Phase: phase},
	}
}

func TestReconcileReportsOnlyFailedPods(t *testing.T) {
	cs := fake.NewClientset(
		managedPod("p-fail", "ti-fail", corev1.PodFailed),
		managedPod("p-run", "ti-run", corev1.PodRunning),
		managedPod("p-ok", "ti-ok", corev1.PodSucceeded),
	)
	reporter := &fakeReporter{}
	r := NewReconciler(cs, "leoflow", reporter)

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, ok := reporter.failed["ti-fail"]; !ok {
		t.Error("failed pod's task instance should be reported")
	}
	if len(reporter.failed) != 1 {
		t.Errorf("only the failed pod should be reported, got %v", reporter.failed)
	}
}

func agedPod(name string, phase corev1.PodPhase, created time.Time) *corev1.Pod {
	p := managedPod(name, "ti-"+name, phase)
	p.CreationTimestamp = metav1.NewTime(created)
	return p
}

func TestReconcileGarbageCollectsOldTerminalPods(t *testing.T) {
	now := time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC)
	cs := fake.NewClientset(
		agedPod("old-success", corev1.PodSucceeded, now.Add(-30*time.Minute)),
		agedPod("old-failed", corev1.PodFailed, now.Add(-30*time.Minute)),
		agedPod("recent-success", corev1.PodSucceeded, now.Add(-1*time.Minute)),
		agedPod("running", corev1.PodRunning, now.Add(-30*time.Minute)),
	)
	r := NewReconciler(cs, "leoflow", &fakeReporter{})
	r.now = func() time.Time { return now }
	r.ttl = 10 * time.Minute

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	remaining := map[string]bool{}
	pods, _ := cs.CoreV1().Pods("leoflow").List(context.Background(), metav1.ListOptions{})
	for _, p := range pods.Items {
		remaining[p.Name] = true
	}
	if remaining["old-success"] || remaining["old-failed"] {
		t.Errorf("old terminal pods should be GC'd, remaining: %v", remaining)
	}
	if !remaining["recent-success"] || !remaining["running"] {
		t.Errorf("recent and running pods must be kept, remaining: %v", remaining)
	}
}
