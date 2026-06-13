package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/neochaotic/leoflow/internal/agentrpc"
	"github.com/neochaotic/leoflow/internal/auth"
	"github.com/neochaotic/leoflow/internal/dispatch"
	"github.com/neochaotic/leoflow/internal/domain"
	"github.com/neochaotic/leoflow/internal/storage/queries"
)

// ExecutionStore resolves task execution context from Postgres. It implements
// both agentrpc.Store (serving the in-pod agent) and dispatch.Resolver (feeding
// the pod-path dispatcher) over the same dag_version spec.
type ExecutionStore struct {
	q *queries.Queries
}

// NewExecutionStore builds an ExecutionStore over the given Postgres connection.
func NewExecutionStore(pg *Postgres) *ExecutionStore {
	return &ExecutionStore{q: pg.Queries}
}

// TaskSpec returns the agent-facing execution spec for a task instance.
func (s *ExecutionStore) TaskSpec(ctx context.Context, id auth.AgentIdentity) (agentrpc.TaskSpec, error) {
	task, spec, _, run, err := s.resolve(ctx, id.RunID, id.TaskID)
	if err != nil {
		return agentrpc.TaskSpec{}, err
	}
	// The DagRun's logical date, RFC3339, for the runtime's ds/ts (ADR 0040). Empty
	// when unset so the agent leaves LEOFLOW_TS/DS unset rather than stamping a zero.
	var logicalDate string
	if run.LogicalDate.Valid {
		logicalDate = run.LogicalDate.Time.UTC().Format(time.RFC3339)
	}
	var timeout int
	if task.ExecutionTimeoutSeconds != nil {
		timeout = *task.ExecutionTimeoutSeconds
	}
	// Marshal TaskFlow literals (#115) into a single string field on the gRPC
	// TaskSpec; the agent forwards it as LEOFLOW_CALL_ARGS_JSON. Marshaling
	// failures are surfaced — a non-serialisable map in dag.json is a
	// compile-time bug we want to see, not paper over.
	var callArgsJSON string
	if len(task.CallArgs) > 0 {
		b, mErr := json.Marshal(task.CallArgs)
		if mErr != nil {
			return agentrpc.TaskSpec{}, fmt.Errorf("marshaling task call_args: %w", mErr)
		}
		callArgsJSON = string(b)
	}
	// Marshal the captured operator constructor kwargs (ADR 0040) the same way;
	// the agent forwards it as LEOFLOW_OPERATOR_ARGS. A non-serialisable arg in
	// dag.json is a compile-time bug we surface rather than paper over.
	var operatorArgsJSON string
	if len(task.OperatorArgs) > 0 {
		b, mErr := json.Marshal(task.OperatorArgs)
		if mErr != nil {
			return agentrpc.TaskSpec{}, fmt.Errorf("marshaling operator args: %w", mErr)
		}
		operatorArgsJSON = string(b)
	}
	return agentrpc.TaskSpec{
		Operator:         string(task.Type),
		Entrypoint:       task.Entrypoint,
		DagVersion:       spec.DagVersion,
		Environment:      task.Env,
		XComInputMapping: task.XComInput,
		XComSchema:       task.XComSchema,
		TimeoutSeconds:   timeout,
		CallArgsJSON:     callArgsJSON,
		OperatorClass:    task.OperatorClass,
		OperatorArgsJSON: operatorArgsJSON,
		LogicalDate:      logicalDate,
		DependsOn:        task.DependsOn,
	}, nil
}

// ReportState records a state transition reported by the agent, persisting the
// exit code and error message and stamping started/ended/duration timestamps.
func (s *ExecutionStore) ReportState(ctx context.Context, id auth.AgentIdentity, state domain.TaskState, exitCode int, errMsg string) error {
	rid, err := parseUUID(id.RunID)
	if err != nil {
		return err
	}
	code := toInt32(exitCode)
	params := queries.ReportTaskResultParams{
		DagRunID: rid,
		TaskID:   id.TaskID,
		Column3:  queries.TaskState(state), // sqlc names the $3::task_state cast param Column3.
		ExitCode: &code,
	}
	if errMsg != "" {
		params.ErrorMessage = &errMsg
	}
	return s.q.ReportTaskResult(ctx, params)
}

// RecordHeartbeat stamps last_heartbeat_at on the agent's TI so the
// scheduler's heartbeat reaper (#128) can tell a live task from one whose
// agent has gone silent. The SQL guard skips terminal rows — a late
// heartbeat after a terminal report is a no-op, not a regression.
func (s *ExecutionStore) RecordHeartbeat(ctx context.Context, id auth.AgentIdentity) error {
	rid, err := parseUUID(id.RunID)
	if err != nil {
		return err
	}
	return s.q.RecordTaskHeartbeat(ctx, queries.RecordTaskHeartbeatParams{
		DagRunID:  rid,
		TaskID:    id.TaskID,
		TryNumber: toInt32(id.TryNumber),
	})
}

// FailTask marks a task instance failed by its ID, but only while it is still
// active (scheduled/queued/running), so it never clobbers a terminal state. It
// implements executor.FailureReporter for the pod reconciler.
func (s *ExecutionStore) FailTask(ctx context.Context, taskInstanceID, reason string) error {
	tid, err := parseUUID(taskInstanceID)
	if err != nil {
		return err
	}
	msg := reason
	return s.q.FailTaskInstanceIfActive(ctx, queries.FailTaskInstanceIfActiveParams{
		ID:           tid,
		ErrorMessage: &msg,
	})
}

// ResolveTask returns the dispatcher's execution context for a run's task.
func (s *ExecutionStore) ResolveTask(ctx context.Context, runID, taskID string) (dispatch.Resolved, error) {
	task, spec, ver, _, err := s.resolve(ctx, runID, taskID)
	if err != nil {
		return dispatch.Resolved{}, err
	}
	rid, err := parseUUID(runID)
	if err != nil {
		return dispatch.Resolved{}, err
	}
	tis, err := s.q.ListTaskInstancesByRun(ctx, rid)
	if err != nil {
		return dispatch.Resolved{}, fmt.Errorf("listing task instances: %w", err)
	}
	ti, ok := latestTry(tis, taskID)
	if !ok {
		return dispatch.Resolved{}, fmt.Errorf("no task instance for task %q in run %q", taskID, runID)
	}
	image := ver.ImageReference
	if image == "" {
		image = spec.Image
	}
	pullPolicy := "IfNotPresent"
	if task.Execution != nil && task.Execution.ImagePullPolicy != "" {
		pullPolicy = task.Execution.ImagePullPolicy
	}
	return dispatch.Resolved{
		TaskInstanceID:  uuidToString(ti.ID),
		TenantID:        uuidToString(ti.TenantID),
		Image:           image,
		ImagePullPolicy: pullPolicy,
		TryNumber:       int(ti.TryNumber),
		Staging:         spec.Staging,
		// Materialize source on the executor side (Lite only); Pro ignores it.
		Source: spec.Source,
	}, nil
}

// resolve loads the dag version and run for a run id and returns the named task's
// spec. The run is returned too so callers can read run-scoped fields (e.g. its
// logical date) without a second query.
func (s *ExecutionStore) resolve(ctx context.Context, runID, taskID string) (domain.TaskSpec, domain.DAGSpec, queries.DagVersion, queries.DagRun, error) {
	rid, err := parseUUID(runID)
	if err != nil {
		return domain.TaskSpec{}, domain.DAGSpec{}, queries.DagVersion{}, queries.DagRun{}, err
	}
	run, err := s.q.GetDagRunByID(ctx, rid)
	if err != nil {
		return domain.TaskSpec{}, domain.DAGSpec{}, queries.DagVersion{}, queries.DagRun{}, fmt.Errorf("loading run: %w", err)
	}
	ver, err := s.q.GetDagVersionByID(ctx, run.DagVersionID)
	if err != nil {
		return domain.TaskSpec{}, domain.DAGSpec{}, queries.DagVersion{}, queries.DagRun{}, fmt.Errorf("loading dag version: %w", err)
	}
	var spec domain.DAGSpec
	if jerr := json.Unmarshal(ver.Spec, &spec); jerr != nil {
		return domain.TaskSpec{}, domain.DAGSpec{}, queries.DagVersion{}, queries.DagRun{}, fmt.Errorf("decoding spec: %w", jerr)
	}
	for _, t := range spec.Tasks {
		if t.TaskID == taskID {
			return t, spec, ver, run, nil
		}
	}
	return domain.TaskSpec{}, domain.DAGSpec{}, queries.DagVersion{}, queries.DagRun{}, fmt.Errorf("task %q not found in run %q", taskID, runID)
}

// latestTry returns the highest try_number task instance for the given task.
func latestTry(tis []queries.TaskInstance, taskID string) (queries.TaskInstance, bool) {
	var best queries.TaskInstance
	found := false
	for _, ti := range tis {
		if ti.TaskID == taskID && (!found || ti.TryNumber > best.TryNumber) {
			best, found = ti, true
		}
	}
	return best, found
}
