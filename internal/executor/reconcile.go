package executor

import (
	"context"
	"fmt"
	"log/slog"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// podOutcome is the reconciler's classification of a task pod's status.
type podOutcome int

const (
	// podPending means the pod is still starting or running; no action.
	podPending podOutcome = iota
	// podFailed means the pod failed in a way the agent will never report
	// (terminal phase or an unrecoverable container start error).
	podFailed
	// podSucceeded means the pod completed successfully.
	podSucceeded
)

// unrecoverableWaiting lists container "waiting" reasons that never self-resolve
// and mean the agent never started, so no state will be reported.
var unrecoverableWaiting = map[string]bool{
	"ImagePullBackOff":     true,
	"ErrImagePull":         true,
	"InvalidImageName":     true,
	"CreateContainerError": true,
	"CrashLoopBackOff":     true,
}

// classifyPod determines whether a task pod has failed, succeeded, or is still
// in progress, returning a human-readable reason for failures.
func classifyPod(pod *corev1.Pod) (outcome podOutcome, reason string) {
	switch pod.Status.Phase {
	case corev1.PodFailed:
		return podFailed, podFailureReason(pod)
	case corev1.PodSucceeded:
		return podSucceeded, ""
	case corev1.PodPending, corev1.PodRunning, corev1.PodUnknown:
		for _, cs := range pod.Status.ContainerStatuses {
			if w := cs.State.Waiting; w != nil && unrecoverableWaiting[w.Reason] {
				return podFailed, w.Reason
			}
		}
		return podPending, ""
	default:
		return podPending, ""
	}
}

func podFailureReason(pod *corev1.Pod) string {
	if pod.Status.Reason != "" {
		return pod.Status.Reason
	}
	return "pod failed"
}

// FailureReporter marks a task instance failed when its pod failed without the
// agent reporting. The implementation must be idempotent and only act on a
// non-terminal task instance.
type FailureReporter interface {
	FailTask(ctx context.Context, taskInstanceID, reason string) error
}

// Reconciler detects task pods that failed without the agent reporting state and
// marks the corresponding task instance failed, so retries and run finalization
// can proceed instead of stranding the task.
type Reconciler struct {
	clientset kubernetes.Interface
	namespace string
	reporter  FailureReporter
}

// NewReconciler builds a Reconciler over the given cluster and failure reporter.
func NewReconciler(clientset kubernetes.Interface, namespace string, reporter FailureReporter) *Reconciler {
	return &Reconciler{clientset: clientset, namespace: namespace, reporter: reporter}
}

// Reconcile lists managed task pods and reports each failed one's task instance.
func (r *Reconciler) Reconcile(ctx context.Context) error {
	pods, err := r.clientset.CoreV1().Pods(r.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "leoflow.io/run-id",
	})
	if err != nil {
		return fmt.Errorf("listing task pods: %w", err)
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		outcome, reason := classifyPod(pod)
		if outcome != podFailed {
			continue
		}
		tiID := pod.Annotations["leoflow.io/task-instance-id"]
		if tiID == "" {
			continue
		}
		if rerr := r.reporter.FailTask(ctx, tiID, reason); rerr != nil {
			slog.Error("reporting failed pod", "pod", pod.Name, "task_instance", tiID, "error", rerr)
		}
	}
	return nil
}
