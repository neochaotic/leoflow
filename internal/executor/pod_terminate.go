package executor

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// This file gives the Kubernetes executor the ability to tear down a reaped
// task's pod (#474). Before this, the three scheduler reapers only wrote DB
// state — a reaped TI's pod kept running to completion, doing at-most-once work
// twice. The reapers now call these methods after the DB transition so the pod
// is actually stopped.
//
// Every selector below reuses the exact label scheme BuildPod stamps
// (leoflow.io/run-id, /task-id, /try-number). Deletion is by List-then-Delete,
// not DeleteCollection: the executor Role grants the `list` and `delete` verbs
// but NOT `deletecollection` (helm/leoflow/templates/rbac.yaml), so a
// DeleteCollection call would 403 in production. NotFound is always tolerated —
// a pod may have been garbage-collected between the list and the delete.

// DeleteTaskPod deletes the pod(s) for exactly one reaped task instance: the
// (run, task, try) tuple. Pinning try-number is the invariant guard — a retry
// bumps try_number in place and dispatches a new pod with a new try-number
// label, so a newer live attempt can never match this selector and is never
// deleted. Tolerates NotFound.
func (e *KubernetesExecutor) DeleteTaskPod(ctx context.Context, runID, taskID string, tryNumber int) error {
	selector := fmt.Sprintf("leoflow.io/run-id=%s,leoflow.io/task-id=%s,leoflow.io/try-number=%s",
		sanitizeLabel(runID), sanitizeLabel(taskID), strconv.Itoa(tryNumber))
	return e.deletePodsBySelector(ctx, selector)
}

// DeleteRunPods deletes every task pod belonging to a single reaped run. The
// orphan-run reaper abandons a whole run (failing all its still-active TIs), so
// every pod of that run must be torn down. The run-id is a unique per-run UUID,
// so this selector can only ever match pods of the one abandoned run — never a
// different run's live pod. Tolerates NotFound.
func (e *KubernetesExecutor) DeleteRunPods(ctx context.Context, runID string) error {
	selector := fmt.Sprintf("leoflow.io/run-id=%s", sanitizeLabel(runID))
	return e.deletePodsBySelector(ctx, selector)
}

// deletePodsBySelector lists the pods matching selector and deletes each by
// name. It uses only the `list` and `delete` verbs the executor Role grants;
// a NotFound on either the list target or an individual delete is treated as
// success (the pod is already gone). Per-pod delete errors are collected so one
// failure does not skip the rest.
func (e *KubernetesExecutor) deletePodsBySelector(ctx context.Context, selector string) error {
	pods, err := e.clientset.CoreV1().Pods(e.namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("listing pods to delete (%s): %w", selector, err)
	}
	var errs []error
	for i := range pods.Items {
		name := pods.Items[i].Name
		if derr := e.clientset.CoreV1().Pods(e.namespace).Delete(ctx, name, metav1.DeleteOptions{}); derr != nil && !apierrors.IsNotFound(derr) {
			errs = append(errs, fmt.Errorf("deleting pod %s: %w", name, derr))
		}
	}
	return errors.Join(errs...)
}

// TaskPodPresence reports what the apiserver holds for exactly the
// (run, task, try) attempt: a live pod (Pending/Running), a present-but-finished
// pod, or no pod at all. The dispatch-lost and pod-lost reapers consult this
// before failing a TI: a live pod means the dispatch actually landed and the node
// is merely slow to pull the image (#461), so the reaper must DEFER.
//
// A present-but-finished pod is reported apart from an absence on purpose. The
// pod object still carries the attempt's outcome (the termination log the
// reconciler recovers a durable result from), so it is the reconciler's to settle
// and no reaper may delete it; only a genuine absence means the attempt is lost
// with nothing left to recover. Any live pod for the attempt wins over a
// lingering finished sibling, since work may still be running.
//
// Try-number is pinned — the same invariant guard as DeleteTaskPod above. The
// retry rail resets up_for_retry -> none with try_number+1 and the planner
// re-queues the TI (storage/queries/runs.sql), so a `queued`/`running` TI can be
// on try 2 while try 1's pod still lingers Pending after a failed best-effort
// delete. Selecting on (run, task) alone would match that stale older pod and
// false-defer the reap of the current attempt forever (#723). Asking about the
// attempt the reaper is about to fail is the correct liveness question.
func (e *KubernetesExecutor) TaskPodPresence(ctx context.Context, runID, taskID string, tryNumber int) (PodPresence, error) {
	selector := fmt.Sprintf("leoflow.io/run-id=%s,leoflow.io/task-id=%s,leoflow.io/try-number=%s",
		sanitizeLabel(runID), sanitizeLabel(taskID), strconv.Itoa(tryNumber))
	pods, err := e.clientset.CoreV1().Pods(e.namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		// PodPresenceLive is the zero value on purpose: a caller that drops the
		// error still holds the presence that authorizes nothing.
		return PodPresenceLive, fmt.Errorf("listing pods for liveness (%s): %w", selector, err)
	}
	if len(pods.Items) == 0 {
		return PodPresenceAbsent, nil
	}
	for i := range pods.Items {
		if phase := pods.Items[i].Status.Phase; phase == corev1.PodPending || phase == corev1.PodRunning {
			return PodPresenceLive, nil
		}
	}
	return PodPresenceTerminal, nil
}
