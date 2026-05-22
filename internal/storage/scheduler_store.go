package storage

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/neochaotic/leoflow/internal/domain"
	"github.com/neochaotic/leoflow/internal/scheduler"
	"github.com/neochaotic/leoflow/internal/storage/queries"
)

// SchedulerStore is the sqlc-backed implementation of scheduler.Store.
type SchedulerStore struct {
	q *queries.Queries
}

// NewSchedulerStore builds a SchedulerStore over the given Postgres connection.
func NewSchedulerStore(pg *Postgres) *SchedulerStore {
	return &SchedulerStore{q: pg.Queries}
}

// ActiveRuns returns every queued/running run with its topology and task states.
func (s *SchedulerStore) ActiveRuns(ctx context.Context) ([]scheduler.RunState, error) {
	runs, err := s.q.ListActiveDagRuns(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing active runs: %w", err)
	}
	out := make([]scheduler.RunState, 0, len(runs))
	for _, run := range runs {
		version, err := s.q.GetDagVersionByID(ctx, run.DagVersionID)
		if err != nil {
			return nil, fmt.Errorf("loading dag version: %w", err)
		}
		var spec domain.DAGSpec
		if uerr := json.Unmarshal(version.Spec, &spec); uerr != nil {
			return nil, fmt.Errorf("decoding spec: %w", uerr)
		}
		tis, err := s.q.ListTaskInstancesByRun(ctx, run.ID)
		if err != nil {
			return nil, fmt.Errorf("listing task instances: %w", err)
		}
		states := make(map[string]domain.TaskState, len(tis))
		for _, ti := range tis {
			states[ti.TaskID] = domain.TaskState(ti.State)
		}
		out = append(out, scheduler.RunState{
			RunID:  uuidToString(run.ID),
			State:  domain.DagRunState(run.State),
			Tasks:  spec.Tasks,
			States: states,
		})
	}
	return out, nil
}

// MaterializeTasks creates a none-state task instance for each task in the run.
func (s *SchedulerStore) MaterializeTasks(ctx context.Context, runID string, tasks []domain.TaskSpec) error {
	rid, err := parseUUID(runID)
	if err != nil {
		return err
	}
	run, err := s.q.GetDagRunByID(ctx, rid)
	if err != nil {
		return fmt.Errorf("loading run: %w", err)
	}
	for _, t := range tasks {
		maxTries := int32(1)
		if _, err := s.q.CreateTaskInstance(ctx, queries.CreateTaskInstanceParams{
			TenantID: run.TenantID,
			DagRunID: rid,
			TaskID:   t.TaskID,
			Operator: string(t.Type),
			MaxTries: maxTries,
			State:    queries.TaskStateNone,
		}); err != nil {
			return fmt.Errorf("creating task instance %q: %w", t.TaskID, err)
		}
	}
	return nil
}

// ApplyTransition moves a task instance to a new state.
func (s *SchedulerStore) ApplyTransition(ctx context.Context, runID, taskID string, to domain.TaskState) error {
	rid, err := parseUUID(runID)
	if err != nil {
		return err
	}
	return s.q.UpdateTaskInstanceStateByRunTask(ctx, queries.UpdateTaskInstanceStateByRunTaskParams{
		DagRunID: rid,
		TaskID:   taskID,
		State:    queries.TaskState(to),
	})
}

// SetRunState updates a run's state.
func (s *SchedulerStore) SetRunState(ctx context.Context, runID string, state domain.DagRunState) error {
	rid, err := parseUUID(runID)
	if err != nil {
		return err
	}
	_, err = s.q.UpdateDagRunState(ctx, queries.UpdateDagRunStateParams{
		ID:        rid,
		State:     queries.DagRunState(state),
		StartedAt: pgtype.Timestamptz{},
		EndedAt:   pgtype.Timestamptz{},
	})
	return err
}

var _ scheduler.Store = (*SchedulerStore)(nil)
