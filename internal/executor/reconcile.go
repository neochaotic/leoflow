package executor

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/neochaotic/leoflow/internal/taskoutcome"
)

// settleKind is what the reconciler must record for a terminal task pod.
type settleKind int

const (
	// settleNothing means there is nothing to record (a succeeded pod with no
	// outcome record — its report already settled the task; the reconciler only
	// garbage-collects it).
	settleNothing settleKind = iota
	// settleFailed records the task instance as failed.
	settleFailed
	// settleSucceeded records the task instance as succeeded — recovering a success
	// whose report was lost (ADR 0052).
	settleSucceeded
	// settleReschedule parks the task instance in up_for_reschedule.
	settleReschedule
)

// verdict is the reconciler's classification of a task pod: whether it is terminal
// (garbage-collectable on age) and what, if anything, to record for it.
type verdict struct {
	terminal   bool       // the pod is done; eligible for GC on age
	settle     settleKind // what to record for the task instance
	reason     string     // failure reason (settleFailed)
	at         time.Time  // next-poke time (settleReschedule)
	fromRecord bool       // a durable outcome record drove this, not pod phase
}

// unrecoverableWaiting lists container "waiting" reasons that never self-resolve
// and mean the agent never started, so no state will be reported.
var unrecoverableWaiting = map[string]bool{
	"ImagePullBackOff":     true,
	"ErrImagePull":         true,
	"InvalidImageName":     true,
	"CreateContainerError": true,
	"CrashLoopBackOff":     true,
}

// taskContainerName is the task pod's single container (see BuildPod).
const taskContainerName = "task"

// classifyPod determines what the reconciler should do with a task pod. It trusts
// a durable outcome record on the terminated task container over pod phase (ADR
// 0052) — recovering a success whose report was lost — and falls back to
// phase-based classification when no decodable record is present.
func classifyPod(pod *corev1.Pod) verdict {
	// The durable outcome record is authoritative when present: the agent wrote it
	// before attempting the report, so it survives a pod killed mid-report.
	if rec, ok := outcomeRecord(pod); ok {
		switch rec.Outcome {
		case taskoutcome.Success:
			return verdict{terminal: true, settle: settleSucceeded, fromRecord: true}
		case taskoutcome.Failed:
			return verdict{terminal: true, settle: settleFailed, reason: recordFailureReason(rec), fromRecord: true}
		case taskoutcome.Reschedule:
			at, _ := rec.At() // Decode guaranteed a parseable time
			return verdict{terminal: true, settle: settleReschedule, at: at, fromRecord: true}
		}
	}

	// No record — fall back to pod phase (pre-0052 behavior). A record-less
	// succeeded pod has nothing to settle; its report already ran. A failed pod is
	// recorded as failed so retries and finalization proceed.
	switch pod.Status.Phase {
	case corev1.PodFailed:
		return verdict{terminal: true, settle: settleFailed, reason: podFailureReason(pod)}
	case corev1.PodSucceeded:
		return verdict{terminal: true, settle: settleNothing}
	case corev1.PodPending, corev1.PodRunning, corev1.PodUnknown:
		for _, cs := range pod.Status.ContainerStatuses {
			if w := cs.State.Waiting; w != nil && unrecoverableWaiting[w.Reason] {
				return verdict{terminal: true, settle: settleFailed, reason: w.Reason}
			}
		}
		return verdict{terminal: false}
	default:
		return verdict{terminal: false}
	}
}

// outcomeRecord decodes the durable outcome record from the terminated task
// container's termination message, if present and valid. A missing, running, or
// undecodable message returns false so the caller falls back to pod phase.
func outcomeRecord(pod *corev1.Pod) (taskoutcome.Record, bool) {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name != taskContainerName {
			continue
		}
		if t := cs.State.Terminated; t != nil && t.Message != "" {
			return taskoutcome.Decode(t.Message)
		}
	}
	return taskoutcome.Record{}, false
}

// recordFailureReason renders a failure reason from an outcome record, naming the
// exit code when the record carries one.
func recordFailureReason(rec taskoutcome.Record) string {
	if rec.ExitCode != nil {
		return fmt.Sprintf("task failed (exit %d)", *rec.ExitCode)
	}
	return "task failed"
}

func podFailureReason(pod *corev1.Pod) string {
	if pod.Status.Reason != "" {
		return pod.Status.Reason
	}
	return "pod failed"
}

// tryNumberOf reads the attempt the pod ran, from the leoflow.io/try-number label
// BuildPod sets. It guards the settle against clobbering a different attempt, so a
// pod whose label is missing or unparseable is not settled (ok is false).
func tryNumberOf(pod *corev1.Pod) (int, bool) {
	s := pod.Labels["leoflow.io/try-number"]
	if s == "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

// OutcomeReporter records a terminal task-instance outcome the reconciler
// recovered from a pod (its durable outcome record, or its phase). Every method is
// guarded by the attempt (try_number) so a stale reconciler acting on a previous
// attempt's pod never clobbers a live retry, and is idempotent: a settle on an
// already-terminal instance is a no-op, not an error.
type OutcomeReporter interface {
	FailTask(ctx context.Context, taskInstanceID string, tryNumber int, reason string) error
	SucceedTask(ctx context.Context, taskInstanceID string, tryNumber int) error
	RescheduleTask(ctx context.Context, taskInstanceID string, tryNumber int, at time.Time) error
}

// podGCGracePeriod is how long a finished pod is kept before garbage collection,
// leaving a window to inspect a failed pod with kubectl.
const podGCGracePeriod = 10 * time.Minute

// Reconciler detects task pods whose task instance was never settled by the agent
// (a pod killed before or during its report) and records the true outcome — from
// the pod's durable outcome record where present, else its phase — so retries and
// run finalization proceed instead of stranding the task. It also garbage-collects
// finished pods once they age out.
type Reconciler struct {
	clientset kubernetes.Interface
	namespace string
	reporter  OutcomeReporter
	now       func() time.Time
	ttl       time.Duration
}

// NewReconciler builds a Reconciler over the given cluster and outcome reporter.
func NewReconciler(clientset kubernetes.Interface, namespace string, reporter OutcomeReporter) *Reconciler {
	return &Reconciler{clientset: clientset, namespace: namespace, reporter: reporter, now: time.Now, ttl: podGCGracePeriod}
}

// Reconcile lists managed task pods, records each terminal one's outcome against
// its task instance, and garbage-collects finished pods older than the grace
// period.
func (r *Reconciler) Reconcile(ctx context.Context) error {
	pods, err := r.clientset.CoreV1().Pods(r.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "leoflow.io/run-id",
	})
	if err != nil {
		return fmt.Errorf("listing task pods: %w", err)
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		v := classifyPod(pod)
		if !v.terminal {
			continue
		}
		// A terminal pod must have its outcome durably recorded before it is
		// collected: the pod is the only signal that lets the next tick retry the
		// record, so deleting it after a failed settle strands the task instance
		// until the heartbeat reaper catches it. If the settle does not succeed,
		// leave the pod and try again next tick.
		if v.settle != settleNothing {
			if err := r.settlePod(ctx, pod, v); err != nil {
				continue
			}
		}
		if r.now().Sub(pod.CreationTimestamp.Time) > r.ttl {
			r.collect(ctx, pod)
		}
	}
	return nil
}

// settlePod records the pod's recovered outcome against its task instance,
// attempt-guarded. It returns nil when there is nothing to record (an orphan pod
// with no task-instance annotation) so the caller may collect it, and an error
// when the outcome could not be durably recorded, so the caller defers collection
// until a later tick succeeds.
func (r *Reconciler) settlePod(ctx context.Context, pod *corev1.Pod, v verdict) error {
	tiID := pod.Annotations["leoflow.io/task-instance-id"]
	if tiID == "" {
		return nil
	}
	tryNumber, ok := tryNumberOf(pod)
	if !ok {
		// Without the attempt we cannot guard the settle, and settling unguarded
		// could clobber a different attempt. Leave the pod for a later tick / the
		// heartbeat reaper rather than risk a wrong write.
		err := fmt.Errorf("pod %s has no parseable leoflow.io/try-number label", pod.Name)
		slog.Error("cannot settle task pod without a try-number", "pod", pod.Name, "task_instance", tiID)
		return err
	}
	if err := r.recordOutcome(ctx, tiID, tryNumber, v); err != nil {
		slog.Error("recording task pod outcome", "pod", pod.Name, "task_instance", tiID,
			"settle", v.settle, "from_record", v.fromRecord, "error", err)
		return err
	}
	return nil
}

func (r *Reconciler) recordOutcome(ctx context.Context, tiID string, tryNumber int, v verdict) error {
	switch v.settle {
	case settleSucceeded:
		return r.reporter.SucceedTask(ctx, tiID, tryNumber)
	case settleReschedule:
		return r.reporter.RescheduleTask(ctx, tiID, tryNumber, v.at)
	case settleFailed:
		return r.reporter.FailTask(ctx, tiID, tryNumber, v.reason)
	case settleNothing:
		return nil
	default:
		return nil
	}
}

func (r *Reconciler) collect(ctx context.Context, pod *corev1.Pod) {
	if err := r.clientset.CoreV1().Pods(r.namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{}); err != nil {
		slog.Error("garbage-collecting finished pod", "pod", pod.Name, "error", err)
	}
}
