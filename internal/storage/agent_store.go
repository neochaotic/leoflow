package storage

import (
	"context"
	"encoding/json"
	"fmt"

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
	task, spec, _, err := s.resolve(ctx, id.RunID, id.TaskID)
	if err != nil {
		return agentrpc.TaskSpec{}, err
	}
	var timeout int
	if task.ExecutionTimeoutSeconds != nil {
		timeout = *task.ExecutionTimeoutSeconds
	}
	return agentrpc.TaskSpec{
		Operator:         string(task.Type),
		Entrypoint:       task.Entrypoint,
		DagVersion:       spec.DagVersion,
		Environment:      task.Env,
		XComInputMapping: task.XComInput,
		TimeoutSeconds:   timeout,
	}, nil
}

// ReportState records a state transition reported by the agent. Exit code and
// error message are not yet persisted (see Phase 4).
func (s *ExecutionStore) ReportState(ctx context.Context, id auth.AgentIdentity, state domain.TaskState, _ int, _ string) error {
	rid, err := parseUUID(id.RunID)
	if err != nil {
		return err
	}
	return s.q.UpdateTaskInstanceStateByRunTask(ctx, queries.UpdateTaskInstanceStateByRunTaskParams{
		DagRunID: rid,
		TaskID:   id.TaskID,
		State:    queries.TaskState(state),
	})
}

// ResolveTask returns the dispatcher's execution context for a run's task.
func (s *ExecutionStore) ResolveTask(ctx context.Context, runID, taskID string) (dispatch.Resolved, error) {
	task, spec, ver, err := s.resolve(ctx, runID, taskID)
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
	}, nil
}

// resolve loads the dag version for a run and returns the named task's spec.
func (s *ExecutionStore) resolve(ctx context.Context, runID, taskID string) (domain.TaskSpec, domain.DAGSpec, queries.DagVersion, error) {
	rid, err := parseUUID(runID)
	if err != nil {
		return domain.TaskSpec{}, domain.DAGSpec{}, queries.DagVersion{}, err
	}
	run, err := s.q.GetDagRunByID(ctx, rid)
	if err != nil {
		return domain.TaskSpec{}, domain.DAGSpec{}, queries.DagVersion{}, fmt.Errorf("loading run: %w", err)
	}
	ver, err := s.q.GetDagVersionByID(ctx, run.DagVersionID)
	if err != nil {
		return domain.TaskSpec{}, domain.DAGSpec{}, queries.DagVersion{}, fmt.Errorf("loading dag version: %w", err)
	}
	var spec domain.DAGSpec
	if jerr := json.Unmarshal(ver.Spec, &spec); jerr != nil {
		return domain.TaskSpec{}, domain.DAGSpec{}, queries.DagVersion{}, fmt.Errorf("decoding spec: %w", jerr)
	}
	for _, t := range spec.Tasks {
		if t.TaskID == taskID {
			return t, spec, ver, nil
		}
	}
	return domain.TaskSpec{}, domain.DAGSpec{}, queries.DagVersion{}, fmt.Errorf("task %q not found in run %q", taskID, runID)
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
