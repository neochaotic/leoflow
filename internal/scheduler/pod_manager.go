package scheduler

import "context"

// PodManager is the slice of the Kubernetes executor the reapers use to (1)
// tear down a reaped task's pod and (2) check whether a queued TI's pod is
// actually live before declaring its dispatch lost (#474, #461).
//
// Before #474 the reapers only wrote DB state, so a reaped TI's pod kept
// running to completion — breaking at-most-once execution. The reapers now
// call these AFTER the durable DB transition so the pod is actually stopped.
//
// It is nil in Lite/subprocess, where tasks are host processes with no pods.
// Every call site guards the nil: a nil PodManager means "no pods to manage",
// and the dispatch-lost reaper falls back to its pure time-threshold behavior.
//
// The Kubernetes executor implements this; the interface lives here so the
// scheduler depends on a capability, not on the executor package.
type PodManager interface {
	// DeleteTaskPod deletes the pod for exactly one reaped task instance —
	// the (run, task, try) tuple. Pinning try-number guarantees a newer live
	// attempt (dispatched with a new try-number) is never deleted. Tolerates
	// a missing pod.
	DeleteTaskPod(ctx context.Context, runID, taskID string, tryNumber int) error
	// DeleteRunPods deletes every task pod of one reaped run. Used by the
	// orphan-run reaper, which abandons the whole run. The run-id is unique
	// per run, so no other run's pod can match. Tolerates missing pods.
	DeleteRunPods(ctx context.Context, runID string) error
	// TaskPodActive reports whether a pod for (run, task) exists and is
	// Pending or Running. The dispatch-lost reaper defers when it is true —
	// the dispatch landed, the node is merely slow (#461).
	TaskPodActive(ctx context.Context, runID, taskID string) (bool, error)
}
