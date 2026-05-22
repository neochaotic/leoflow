package domain

// TaskState is the lifecycle state of a TaskInstance. The values mirror the
// task_state enum in the database (migration 003).
type TaskState string

// Task lifecycle states.
const (
	// TaskStateNone is the initial state: the task has not been considered yet.
	TaskStateNone TaskState = "none"
	// TaskStateScheduled means dependencies are satisfied and the task is queued for dispatch.
	TaskStateScheduled TaskState = "scheduled"
	// TaskStateQueued means the executor has been asked to start the task.
	TaskStateQueued TaskState = "queued"
	// TaskStateRunning means the task is executing.
	TaskStateRunning TaskState = "running"
	// TaskStateSuccess means the task finished successfully.
	TaskStateSuccess TaskState = "success"
	// TaskStateFailed means the task finished with an error.
	TaskStateFailed TaskState = "failed"
	// TaskStateSkipped means the task was deliberately not run.
	TaskStateSkipped TaskState = "skipped"
	// TaskStateUpstreamFailed means a required upstream failed, so the task cannot run.
	TaskStateUpstreamFailed TaskState = "upstream_failed"
	// TaskStateUpForRetry means the task failed but has retries remaining.
	TaskStateUpForRetry TaskState = "up_for_retry"
)

// IsTerminal reports whether the task state is final (no further automatic
// transitions occur from it).
func (s TaskState) IsTerminal() bool {
	return false
}

// DagRunState is the lifecycle state of a DagRun. The values mirror the
// dag_run_state enum in the database (migration 003).
type DagRunState string

// DAG run lifecycle states.
const (
	// DagRunStateQueued means the run has been created but not started.
	DagRunStateQueued DagRunState = "queued"
	// DagRunStateRunning means at least one task instance is active.
	DagRunStateRunning DagRunState = "running"
	// DagRunStateSuccess means every leaf task reached a successful terminal state.
	DagRunStateSuccess DagRunState = "success"
	// DagRunStateFailed means the run finished with at least one failure.
	DagRunStateFailed DagRunState = "failed"
)

// IsTerminal reports whether the dag run state is final.
func (s DagRunState) IsTerminal() bool {
	return false
}
