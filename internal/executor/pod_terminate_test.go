package executor

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// taskPod builds a minimal pod carrying the label scheme the dispatcher stamps
// (see BuildPod), so the reaper's label-selector deletes can find it.
func taskPod(name, runID, taskID string, try int, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "leoflow",
			Labels: map[string]string{
				"leoflow.io/run-id":     sanitizeLabel(runID),
				"leoflow.io/task-id":    sanitizeLabel(taskID),
				"leoflow.io/try-number": itoa(try),
			},
		},
		Status: corev1.PodStatus{Phase: phase},
	}
}

func itoa(n int) string {
	// tiny helper so the test does not import strconv just for one call.
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

func podNames(t *testing.T, cs *fake.Clientset) map[string]bool {
	t.Helper()
	list, err := cs.CoreV1().Pods("leoflow").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing pods: %v", err)
	}
	out := map[string]bool{}
	for i := range list.Items {
		out[list.Items[i].Name] = true
	}
	return out
}

// TestDeleteTaskPod_TargetsExactlyTheReapedTI is the load-bearing invariant for
// #474: a reaped TI's pod is deleted, but a DIFFERENT run's pod and a DIFFERENT
// attempt's pod are left untouched. The reaper must only tear down the exact
// (run, task, try) it just settled — never a live neighbor.
func TestDeleteTaskPod_TargetsExactlyTheReapedTI(t *testing.T) {
	cs := fake.NewSimpleClientset(
		taskPod("victim", "run-a", "extract", 2, corev1.PodRunning),
		taskPod("other-run", "run-b", "extract", 2, corev1.PodRunning),    // different run
		taskPod("other-task", "run-a", "transform", 2, corev1.PodRunning), // different task
		taskPod("newer-try", "run-a", "extract", 3, corev1.PodRunning),    // different (newer) attempt
	)
	e := NewKubernetesExecutor(cs, "leoflow")

	if err := e.DeleteTaskPod(context.Background(), "run-a", "extract", 2); err != nil {
		t.Fatalf("DeleteTaskPod: %v", err)
	}
	got := podNames(t, cs)
	if got["victim"] {
		t.Errorf("reaped TI's pod was NOT deleted: %v", got)
	}
	for _, keep := range []string{"other-run", "other-task", "newer-try"} {
		if !got[keep] {
			t.Errorf("pod %q was wrongly deleted (must survive): %v", keep, got)
		}
	}
}

// TestDeleteTaskPod_ToleratesNotFound: deleting when no matching pod exists is a
// no-op, not an error — the pod may already have been GC'd.
func TestDeleteTaskPod_ToleratesNotFound(t *testing.T) {
	cs := fake.NewSimpleClientset()
	e := NewKubernetesExecutor(cs, "leoflow")
	if err := e.DeleteTaskPod(context.Background(), "run-a", "extract", 1); err != nil {
		t.Errorf("DeleteTaskPod on absent pod = %v, want nil", err)
	}
}

// TestDeleteRunPods_DeletesAllOfTheRunOnly: the orphan reaper abandons a whole
// run, so every pod of that run is torn down — and no other run's is.
func TestDeleteRunPods_DeletesAllOfTheRunOnly(t *testing.T) {
	cs := fake.NewSimpleClientset(
		taskPod("a1", "run-a", "extract", 1, corev1.PodRunning),
		taskPod("a2", "run-a", "transform", 1, corev1.PodPending),
		taskPod("b1", "run-b", "extract", 1, corev1.PodRunning),
	)
	e := NewKubernetesExecutor(cs, "leoflow")
	if err := e.DeleteRunPods(context.Background(), "run-a"); err != nil {
		t.Fatalf("DeleteRunPods: %v", err)
	}
	got := podNames(t, cs)
	if got["a1"] || got["a2"] {
		t.Errorf("run-a pods survived: %v", got)
	}
	if !got["b1"] {
		t.Errorf("run-b pod wrongly deleted: %v", got)
	}
}

// TestTaskPodPresence covers the three-way liveness answer the reapers act on.
// A Pending/Running pod means the dispatch landed and the node is just slow
// (#461), so every reaper DEFERS. A pod that is present but no longer running is
// the reconciler's to settle from its termination log, so it must be
// distinguishable from a genuine absence — collapsing those two into one "not
// active" bool is what let the pod-lost reaper delete a finished pod and destroy
// the evidence of its outcome.
func TestTaskPodPresence(t *testing.T) {
	tests := []struct {
		name  string
		phase corev1.PodPhase
		want  PodPresence
	}{
		{"pending pod is live (defer)", corev1.PodPending, PodPresenceLive},
		{"running pod is live (defer)", corev1.PodRunning, PodPresenceLive},
		{"failed pod is present-but-terminal", corev1.PodFailed, PodPresenceTerminal},
		{"succeeded pod is present-but-terminal", corev1.PodSucceeded, PodPresenceTerminal},
		{"unknown-phase pod is present, so not absent", corev1.PodUnknown, PodPresenceTerminal},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cs := fake.NewSimpleClientset(taskPod("p", "run-a", "extract", 1, tc.phase))
			e := NewKubernetesExecutor(cs, "leoflow")
			got, err := e.TaskPodPresence(context.Background(), "run-a", "extract", 1)
			if err != nil {
				t.Fatalf("TaskPodPresence: %v", err)
			}
			if got != tc.want {
				t.Errorf("TaskPodPresence(phase=%s) = %v, want %v", tc.phase, got, tc.want)
			}
		})
	}
}

// TestTaskPodPresence_LiveWinsOverTerminalSibling: a lingering terminal pod must
// never mask a live one for the same attempt — presence answers about the
// attempt, and any live pod for it means work may still be running.
func TestTaskPodPresence_LiveWinsOverTerminalSibling(t *testing.T) {
	cs := fake.NewSimpleClientset(
		taskPod("dead", "run-a", "extract", 1, corev1.PodFailed),
		taskPod("live", "run-a", "extract", 1, corev1.PodRunning),
	)
	e := NewKubernetesExecutor(cs, "leoflow")
	got, err := e.TaskPodPresence(context.Background(), "run-a", "extract", 1)
	if err != nil {
		t.Fatalf("TaskPodPresence: %v", err)
	}
	if got != PodPresenceLive {
		t.Errorf("TaskPodPresence = %v, want %v", got, PodPresenceLive)
	}
}

// TestTaskPodPresence_PinsTryNumber is the #723 selector lock: a try-1 pod
// lingers Pending, but a liveness query for try 2 must NOT match it. Asking
// about the attempt the reaper is about to fail (try 2) must report an absence,
// so the reaper neither false-defers on a stale older attempt's pod nor mistakes
// it for that attempt's own outcome.
func TestTaskPodPresence_PinsTryNumber(t *testing.T) {
	cs := fake.NewSimpleClientset(taskPod("try1", "run-a", "extract", 1, corev1.PodPending))
	e := NewKubernetesExecutor(cs, "leoflow")

	got, err := e.TaskPodPresence(context.Background(), "run-a", "extract", 2)
	if err != nil {
		t.Fatalf("TaskPodPresence: %v", err)
	}
	if got != PodPresenceAbsent {
		t.Errorf("#723: a try-2 liveness query saw %v; try-number must be pinned so try 1's pod is invisible", got)
	}
	// Sanity: the same query for the attempt that DOES have a Pending pod is live.
	got, err = e.TaskPodPresence(context.Background(), "run-a", "extract", 1)
	if err != nil {
		t.Fatalf("TaskPodPresence(try1): %v", err)
	}
	if got != PodPresenceLive {
		t.Errorf("try-1's own Pending pod read as %v, want %v", got, PodPresenceLive)
	}
}

// TestTaskPodPresence_AbsentIsNotTerminal: no pod at all is a genuine absence —
// the state that authorizes a pod-lost reap — and must not be reported as a
// present-but-terminal pod.
func TestTaskPodPresence_AbsentIsNotTerminal(t *testing.T) {
	cs := fake.NewSimpleClientset()
	e := NewKubernetesExecutor(cs, "leoflow")
	got, err := e.TaskPodPresence(context.Background(), "run-a", "extract", 1)
	if err != nil {
		t.Fatalf("TaskPodPresence: %v", err)
	}
	if got != PodPresenceAbsent {
		t.Errorf("TaskPodPresence with no pods = %v, want %v", got, PodPresenceAbsent)
	}
}
