package domain

import "time"

// DAG is a registered DAG with its scheduling metadata (distinct from DAGSpec,
// which is the compiled artifact).
type DAG struct {
	DagID          string
	Description    string
	Owner          string
	Tags           []string
	Schedule       *string
	ScheduleTZ     string
	StartDate      *time.Time
	IsPaused       bool
	IsActive       bool
	MaxActiveRuns  int
	Catchup        bool
	LastParsedTime *time.Time
}

// DagVersion is a registered version of a DAG. VersionNumber is the 1-based
// ordinal the UI uses (the stored version label is free-form).
type DagVersion struct {
	ID            string
	VersionNumber int
	CreatedAt     time.Time
	// Version is the deployment label that produced this snapshot: a git describe
	// (tag/SHA) in production, or "dev-<timestamp>" in dev. It is the stable
	// per-deployment identifier under a stable dag_id.
	Version string
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
	// ScheduledAt and QueuedAt record when the instance first entered the
	// scheduled and queued states (Airflow's scheduled_when / queued_when).
	ScheduledAt *time.Time
	QueuedAt    *time.Time
	StartedAt   *time.Time
	EndedAt     *time.Time
	Duration    *float64
	Hostname    string
	// Note is operational context shown in the UI's task panel — e.g. why a task
	// is queued but not running (no executor available).
	Note string
}
