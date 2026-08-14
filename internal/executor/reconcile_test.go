package executor

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/neochaotic/leoflow/internal/taskoutcome"
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
		pod          *corev1.Pod
		wantTerminal bool
		wantSettle   settleKind
	}{
		"failed phase":       {podPhase(corev1.PodFailed), true, settleFailed},
		"succeeded phase":    {podPhase(corev1.PodSucceeded), true, settleNothing},
		"running":            {podPhase(corev1.PodRunning), false, settleNothing},
		"pending":            {podPhase(corev1.PodPending), false, settleNothing},
		"image pull backoff": {podWaiting("ImagePullBackOff"), true, settleFailed},
		"err image pull":     {podWaiting("ErrImagePull"), true, settleFailed},
		"crash loop":         {podWaiting("CrashLoopBackOff"), true, settleFailed},
		"benign waiting":     {podWaiting("ContainerCreating"), false, settleNothing},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := classifyPod(c.pod)
			if got.terminal != c.wantTerminal || got.settle != c.wantSettle {
				t.Errorf("classifyPod = {terminal:%v settle:%v}, want {terminal:%v settle:%v}",
					got.terminal, got.settle, c.wantTerminal, c.wantSettle)
			}
		})
	}
}

// settledOutcome captures one call the reconciler made to the outcome reporter.
type settledOutcome struct {
	kind      settleKind
	tryNumber int
	reason    string
	at        time.Time
}

type fakeReporter struct{ settled map[string]settledOutcome }

func (f *fakeReporter) put(id string, o settledOutcome) {
	if f.settled == nil {
		f.settled = map[string]settledOutcome{}
	}
	f.settled[id] = o
}

func (f *fakeReporter) FailTask(_ context.Context, id string, tryNumber int, reason string) error {
	f.put(id, settledOutcome{kind: settleFailed, tryNumber: tryNumber, reason: reason})
	return nil
}

func (f *fakeReporter) SucceedTask(_ context.Context, id string, tryNumber int) error {
	f.put(id, settledOutcome{kind: settleSucceeded, tryNumber: tryNumber})
	return nil
}

func (f *fakeReporter) RescheduleTask(_ context.Context, id string, tryNumber int, at time.Time) error {
	f.put(id, settledOutcome{kind: settleReschedule, tryNumber: tryNumber, at: at})
	return nil
}

func managedPod(name, tiID string, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "leoflow",
			Labels: map[string]string{
				"leoflow.io/run-id":     "r1",
				"leoflow.io/try-number": "1",
			},
			Annotations: map[string]string{"leoflow.io/task-instance-id": tiID},
		},
		Status: corev1.PodStatus{Phase: phase},
	}
}

// withRecord attaches a terminated task container carrying the encoded outcome
// record to a pod, so classifyPod reads it as the source of truth (ADR 0052).
func withRecord(pod *corev1.Pod, rec taskoutcome.Record) *corev1.Pod {
	enc, err := rec.Encode()
	if err != nil {
		panic(err)
	}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "task",
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Message: enc,
		}},
	}}
	return pod
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
	got, ok := reporter.settled["ti-fail"]
	if !ok || got.kind != settleFailed {
		t.Errorf("failed pod's task instance should be settled failed, got %+v (ok=%v)", got, ok)
	}
	if got.tryNumber != 1 {
		t.Errorf("settle must carry the pod's try-number, got %d", got.tryNumber)
	}
	if len(reporter.settled) != 1 {
		t.Errorf("only the failed pod should be settled, got %v", reporter.settled)
	}
}

// TestReconcileRecoversSuccessFromRecord is the headline of ADR 0052: a pod that
// Kubernetes reports as Failed, but whose task container left a `success` outcome
// record (the report was lost, the work was not), is settled SUCCEEDED — not
// failed. This is the zombie-task false-negative, closed.
func TestReconcileRecoversSuccessFromRecord(t *testing.T) {
	cs := fake.NewClientset(
		withRecord(managedPod("p-lost", "ti-lost", corev1.PodFailed), taskoutcome.Succeeded()),
	)
	reporter := &fakeReporter{}
	r := NewReconciler(cs, "leoflow", reporter)

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got, ok := reporter.settled["ti-lost"]
	if !ok || got.kind != settleSucceeded {
		t.Errorf("a Failed pod with a success record must settle succeeded, got %+v (ok=%v)", got, ok)
	}
}

// TestReconcileRecordDrivesFailedAndReschedule: a `failed` record settles failed
// (naming the exit code), and a `reschedule` record settles up_for_reschedule with
// the record's next-poke time — never as a failure (#386 exclusion holds).
func TestReconcileRecordDrivesFailedAndReschedule(t *testing.T) {
	at := time.Date(2099, 1, 2, 3, 4, 5, 0, time.UTC)
	cs := fake.NewClientset(
		withRecord(managedPod("p-fail", "ti-fail", corev1.PodFailed), taskoutcome.FailedWith(42)),
		withRecord(managedPod("p-resched", "ti-resched", corev1.PodRunning), taskoutcome.RescheduledAt(at)),
	)
	reporter := &fakeReporter{}
	r := NewReconciler(cs, "leoflow", reporter)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := reporter.settled["ti-fail"]; got.kind != settleFailed || !strings.Contains(got.reason, "exit 42") {
		t.Errorf("failed record should settle failed naming the exit code, got %+v", got)
	}
	got := reporter.settled["ti-resched"]
	if got.kind != settleReschedule || !got.at.Equal(at) {
		t.Errorf("reschedule record should settle up_for_reschedule at %v, got %+v", at, got)
	}
}

// TestReconcileUndecodableRecordFallsBackToPhase: a termination message that is not
// a valid outcome record (an old agent, a log tail) is ignored and the pod is
// classified by phase — here a Failed pod settles failed.
func TestReconcileUndecodableRecordFallsBackToPhase(t *testing.T) {
	pod := managedPod("p-garbage", "ti-garbage", corev1.PodFailed)
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:  "task",
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: "Traceback (most recent call last): ..."}},
	}}
	cs := fake.NewClientset(pod)
	reporter := &fakeReporter{}
	r := NewReconciler(cs, "leoflow", reporter)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := reporter.settled["ti-garbage"]; got.kind != settleFailed {
		t.Errorf("an undecodable message must fall back to phase (failed), got %+v", got)
	}
}

// TestReconcileSkipsSettleWithoutTryNumber: a pod missing the try-number label
// cannot be attempt-guarded, so the reconciler must NOT settle it (risking a
// clobber of a different attempt) — but it must still age out normally rather than
// leak forever (the label will never appear); the heartbeat reaper settles the TI.
func TestReconcileSkipsSettleWithoutTryNumber(t *testing.T) {
	pod := withRecord(managedPod("p-nolabel", "ti-nolabel", corev1.PodFailed), taskoutcome.Succeeded())
	delete(pod.Labels, "leoflow.io/try-number")
	pod.CreationTimestamp = metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) // aged past the grace period
	cs := fake.NewClientset(pod)
	reporter := &fakeReporter{}
	r := NewReconciler(cs, "leoflow", reporter)
	r.now = func() time.Time { return time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC) }

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(reporter.settled) != 0 {
		t.Errorf("a pod without a try-number must not be settled, got %v", reporter.settled)
	}
	if _, err := cs.CoreV1().Pods("leoflow").Get(context.Background(), "p-nolabel", metav1.GetOptions{}); err == nil {
		t.Error("an aged label-less pod must still be garbage-collected (no forever-leak)")
	}
}

// TestReconcileCollectsReschedulePodImmediately pins the ADR 0052 B1 fix: a
// reschedule pod is collected as soon as its outcome is settled — NOT kept for the
// grace period — because reschedule redispatch reuses the same try_number, so a
// lingering poke pod would re-park the re-dispatched live attempt and flap the
// sensor. Here the pod is young (well within the grace period) yet must be gone.
func TestReconcileCollectsReschedulePodImmediately(t *testing.T) {
	now := time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC)
	pod := withRecord(managedPod("p-poke", "ti-poke", corev1.PodFailed), taskoutcome.RescheduledAt(now.Add(time.Minute)))
	pod.CreationTimestamp = metav1.NewTime(now.Add(-time.Second)) // brand new
	cs := fake.NewClientset(pod)
	reporter := &fakeReporter{}
	r := NewReconciler(cs, "leoflow", reporter)
	r.now = func() time.Time { return now }

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := reporter.settled["ti-poke"]; got.kind != settleReschedule {
		t.Errorf("reschedule pod must be settled up_for_reschedule, got %+v", got)
	}
	if _, err := cs.CoreV1().Pods("leoflow").Get(context.Background(), "p-poke", metav1.GetOptions{}); err == nil {
		t.Error("a settled reschedule pod must be collected immediately, not left to linger and re-park the next attempt")
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
