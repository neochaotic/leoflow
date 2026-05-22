package domain

import "time"

// DAG is a registered DAG with its scheduling metadata (distinct from DAGSpec,
// which is the compiled artifact).
type DAG struct {
	DagID         string
	Description   string
	Owner         string
	Tags          []string
	Schedule      *string
	IsPaused      bool
	IsActive      bool
	MaxActiveRuns int
	Catchup       bool
}

// DagRun is an execution of a DAG, identified by dag_id + run_id.
type DagRun struct {
	DagID       string
	RunID       string
	LogicalDate time.Time
	State       DagRunState
	RunType     string
	QueuedAt    time.Time
	StartedAt   *time.Time
	EndedAt     *time.Time
	Note        string
}

// TaskInstance is an execution of a task within a DagRun.
type TaskInstance struct {
	DagID     string
	RunID     string
	TaskID    string
	MapIndex  int
	TryNumber int
	MaxTries  int
	State     TaskState
	Operator  string
	StartedAt *time.Time
	EndedAt   *time.Time
	Duration  *float64
	Hostname  string
}
