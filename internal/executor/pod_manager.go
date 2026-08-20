package executor

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

// PodPresenceCache is an optional read-through cache of pod presence — backed by
// a shared informer (PR-10) — that the pod-lost and dispatch-lost reapers consult
// ONLY to DEFER a reap, never to authorize one. Its trust is asymmetric (#461):
//
//   - CachedPodActive == true  => a pod is present and Pending/Running; the reaper
//     may skip the live LIST and defer, because presence is monotonic-safe against
//     cache lag (a pod the cache still shows as live cannot have been gone longer
//     than the lag, and deferring one tick is harmless).
//   - CachedPodActive == false => NOT authoritative. The reaper MUST fall through
//     to the live TaskPodActive (quorum) read before any destructive action, so a
//     lagged/cold cache can only ever delay a reap by a tick, never cause a
//     false-positive one.
//
// It exists to remove the O(running-TIs)/sec apiserver LIST storm from the read
// path while keeping the kill decision on the live read. Nil in Lite/subprocess
// and before the informer warms: every candidate then uses the live path.
type PodPresenceCache interface {
	// CachedPodActive reports whether the cache holds a Pending/Running pod for
	// (run, task). Only a true return is trusted (to defer); false is a "no
	// speedup" signal that must not drive a reap.
	CachedPodActive(runID, taskID string) bool
}
