package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/neochaotic/leoflow/internal/domain"
	"github.com/neochaotic/leoflow/internal/scheduler"
	"github.com/neochaotic/leoflow/internal/storage/queries"
)

// SchedulerStore is the sqlc-backed implementation of scheduler.Store.
type SchedulerStore struct {
	q    *queries.Queries
	pool poolBeginner
}

// poolBeginner is the slice of pgxpool.Pool the store uses to start the orphan
// reap transaction. Kept as a tiny interface so tests can fake the pool without
// pulling pgxpool into scheduler_store_test.go.
type poolBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// NewSchedulerStore builds a SchedulerStore over the given Postgres connection.
func NewSchedulerStore(pg *Postgres) *SchedulerStore {
	return &SchedulerStore{q: pg.Queries, pool: pg.Pool}
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
		applyDefaultRetries(&spec)
		tis, err := s.q.ListTaskInstancesByRun(ctx, run.ID)
		if err != nil {
			return nil, fmt.Errorf("listing task instances: %w", err)
		}
		states := make(map[string]domain.TaskState, len(tis))
		tries := make(map[string]int, len(tis))
		maxTries := make(map[string]int, len(tis))
		for _, ti := range tis {
			states[ti.TaskID] = domain.TaskState(ti.State)
			tries[ti.TaskID] = int(ti.TryNumber)
			maxTries[ti.TaskID] = int(ti.MaxTries)
		}
		out = append(out, scheduler.RunState{
			RunID:    uuidToString(run.ID),
			DagID:    spec.DagID,
			TenantID: uuidToString(run.TenantID),
			State:    domain.DagRunState(run.State),
			Tasks:    spec.Tasks,
			States:   states,
			Tries:    tries,
			MaxTries: maxTries,
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
		if t.Retries != nil {
			maxTries = toInt32(*t.Retries + 1)
		}
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

// SetTaskNote attaches operational context to a task instance, shown in the UI.
func (s *SchedulerStore) SetTaskNote(ctx context.Context, runID, taskID, note string) error {
	rid, err := parseUUID(runID)
	if err != nil {
		return err
	}
	return s.q.SetTaskInstanceNote(ctx, queries.SetTaskInstanceNoteParams{
		DagRunID: rid,
		TaskID:   taskID,
		Note:     strPtr(note),
	})
}

// ResetForRetry returns a task instance to 'none', clears its timestamps, and
// increments its try number so the scheduler re-evaluates and re-runs it.
func (s *SchedulerStore) ResetForRetry(ctx context.Context, runID, taskID string) error {
	rid, err := parseUUID(runID)
	if err != nil {
		return err
	}
	return s.q.ResetTaskInstanceToNone(ctx, queries.ResetTaskInstanceToNoneParams{
		DagRunID: rid,
		TaskID:   taskID,
	})
}

// SetRunState updates a run's state.
func (s *SchedulerStore) SetRunState(ctx context.Context, runID string, state domain.DagRunState) error {
	rid, err := parseUUID(runID)
	if err != nil {
		return err
	}
	return s.q.StampDagRunState(ctx, queries.StampDagRunStateParams{
		ID:    rid,
		State: queries.DagRunState(state),
	})
}

// ScheduledDAGs returns active, unpaused, cron-scheduled DAGs with the logical
// date of their most recent run.
func (s *SchedulerStore) ScheduledDAGs(ctx context.Context) ([]scheduler.ScheduledDAG, error) {
	rows, err := s.q.ListScheduledDags(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing scheduled dags: %w", err)
	}
	out := make([]scheduler.ScheduledDAG, 0, len(rows))
	for _, r := range rows {
		out = append(out, scheduler.ScheduledDAG{
			DagID:       r.DagID,
			Schedule:    strOrEmpty(r.Schedule),
			LastLogical: timeFromAny(r.LastLogical),
		})
	}
	return out, nil
}

// CreateScheduledRun inserts a scheduled run for a DAG (idempotent on run_id).
func (s *SchedulerStore) CreateScheduledRun(ctx context.Context, dagID string, logical time.Time) error {
	runID := "scheduled__" + logical.UTC().Format(time.RFC3339)
	return s.q.CreateScheduledRunByDagID(ctx, queries.CreateScheduledRunByDagIDParams{
		RunID:       runID,
		LogicalDate: pgtype.Timestamptz{Time: logical, Valid: true},
		Tenant:      "default",
		DagID:       dagID,
	})
}

// applyDefaultRetries fills each task's retries from the DAG default_args when
// the task has no explicit value, so materialization can derive max_tries.
func applyDefaultRetries(spec *domain.DAGSpec) {
	if spec.DefaultArgs == nil {
		return
	}
	for i := range spec.Tasks {
		if spec.Tasks[i].Retries == nil {
			r := spec.DefaultArgs.Retries
			spec.Tasks[i].Retries = &r
		}
	}
}

var _ scheduler.Store = (*SchedulerStore)(nil)

// RecordStagingVolume records a per-run staging volume as active, keyed by PVC
// name (idempotent — called per task as the PVC is ensured). ADR 0022.
func (s *SchedulerStore) RecordStagingVolume(ctx context.Context, tenantID, dagID, runID, pvcName, size string) error {
	tid, err := parseUUID(tenantID)
	if err != nil {
		return fmt.Errorf("staging volume tenant id %q: %w", tenantID, err)
	}
	if err := s.q.RecordStagingVolume(ctx, queries.RecordStagingVolumeParams{
		TenantID: tid, DagID: dagID, RunID: runID, PvcName: pvcName, Size: size,
	}); err != nil {
		return fmt.Errorf("recording staging volume: %w", err)
	}
	return nil
}

// MarkStagingDeleted records that a staging volume's PVC was deleted and why
// (run_succeeded | ttl_expired | orphaned).
func (s *SchedulerStore) MarkStagingDeleted(ctx context.Context, pvcName, reason string) error {
	var rp *string
	if reason != "" {
		rp = &reason
	}
	if err := s.q.MarkStagingDeleted(ctx, queries.MarkStagingDeletedParams{PvcName: pvcName, Reason: rp}); err != nil {
		return fmt.Errorf("marking staging volume deleted: %w", err)
	}
	return nil
}

// ListReapCandidates returns every dag_run currently in 'running' state with
// the timestamp of its most recent activity, for the scheduler's orphan reaper.
// The query (sqlc.runs.ListOrphanCandidates) is the authority on how to compute
// the timestamp; the reaper only decides whether each one is past its threshold.
func (s *SchedulerStore) ListReapCandidates(ctx context.Context) ([]scheduler.ReapCandidate, error) {
	rows, err := s.q.ListOrphanCandidates(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing orphan candidates: %w", err)
	}
	out := make([]scheduler.ReapCandidate, 0, len(rows))
	for _, r := range rows {
		var last time.Time
		if r.LastActivity.Valid {
			last = r.LastActivity.Time.UTC()
		}
		out = append(out, scheduler.ReapCandidate{
			RunID:        uuidToString(r.ID),
			DagID:        r.DagIDText,
			LastActivity: last,
		})
	}
	return out, nil
}

// ReapRun fails an orphaned dag run, then any of its still-active task
// instances, inside a single transaction. The run UPDATE comes first and is
// guarded by `state = 'running'`: if zero rows are touched, the run was no
// longer running (a competing finalizer beat us) and we abort with a clean
// rollback — the TI table is never touched. This guarantees we cannot leave a
// run as `success`/`failed` while flipping its TIs to `failed (orphaned)`.
// Idempotent: a second call on an already-failed run no-ops.
func (s *SchedulerStore) ReapRun(ctx context.Context, runID string) error {
	rid, err := parseUUID(runID)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning reap tx: %w", err)
	}
	defer func() {
		// Rollback after a successful commit is a no-op (tx is closed) and after
		// a returned error there is no recovery to do — pgx logs it via the pool
		// already. Silencing it keeps the lint happy without hiding a real bug.
		_ = tx.Rollback(ctx) //nolint:errcheck // best-effort cleanup; commit path returns the meaningful error
	}()
	q := s.q.WithTx(tx)
	rows, err := q.MarkRunOrphanedRun(ctx, rid)
	if err != nil {
		return fmt.Errorf("failing orphaned run: %w", err)
	}
	if rows == 0 {
		// Not running any longer — the normal scheduler path finalized it between
		// our list and our reap. Abort without touching task instances; the
		// caller treats a no-op reap as success (the run is no longer an orphan
		// either way).
		return nil
	}
	if err := q.MarkRunOrphanedTaskInstances(ctx, rid); err != nil {
		return fmt.Errorf("failing orphaned task instances: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing reap tx: %w", err)
	}
	return nil
}

// ListAgentLostCandidates returns every `running` TI with a non-null
// last_heartbeat_at, for the scheduler's TI heartbeat reaper (#128). The
// reaper applies the threshold per row so the SQL stays simple.
func (s *SchedulerStore) ListAgentLostCandidates(ctx context.Context) ([]scheduler.AgentLostCandidate, error) {
	rows, err := s.q.ListAgentLostCandidates(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing agent-lost candidates: %w", err)
	}
	out := make([]scheduler.AgentLostCandidate, 0, len(rows))
	for _, r := range rows {
		var last time.Time
		if r.LastHeartbeatAt.Valid {
			last = r.LastHeartbeatAt.Time.UTC()
		}
		out = append(out, scheduler.AgentLostCandidate{
			TaskInstanceID: uuidToString(r.TaskInstanceID),
			DagRunID:       uuidToString(r.DagRunID),
			DagID:          r.DagIDText,
			TaskID:         r.TaskID,
			LastHeartbeat:  last,
		})
	}
	return out, nil
}

// MarkTaskDispatchFailed transitions a TI to `failed` after its asynchronous
// dispatch failed inside the BufferedDispatcher worker (#127). The SQL guard
// only targets scheduled/queued rows, so a TI that already moved to running
// or terminal between the worker accepting the request and the dispatch
// failing is left alone (defense in depth — the agent's late progress report
// wins over the dispatcher's "I failed" claim).
func (s *SchedulerStore) MarkTaskDispatchFailed(ctx context.Context, runID, taskID, reason string) error {
	rid, err := parseUUID(runID)
	if err != nil {
		return err
	}
	return s.q.MarkTaskDispatchFailed(ctx, queries.MarkTaskDispatchFailedParams{
		DagRunID:     rid,
		TaskID:       taskID,
		ErrorMessage: &reason,
	})
}

// MarkTaskAgentLost transitions one TI to `failed` with the agent_lost
// reason. The WHERE state='running' guard makes this idempotent and prevents
// a late terminal report being overwritten — if the row already moved, we
// touch zero rows and return nil.
func (s *SchedulerStore) MarkTaskAgentLost(ctx context.Context, taskInstanceID string) error {
	tid, err := parseUUID(taskInstanceID)
	if err != nil {
		return err
	}
	if _, err := s.q.MarkTaskAgentLost(ctx, tid); err != nil {
		return fmt.Errorf("marking task agent-lost: %w", err)
	}
	return nil
}

// ListActiveStagingVolumes returns active staging volumes joined with their DAG
// run's state (empty when the run row is gone), for the GC (ADR 0022).
func (s *SchedulerStore) ListActiveStagingVolumes(ctx context.Context) ([]domain.StagingVolumeState, error) {
	rows, err := s.q.ListActiveStagingVolumes(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing staging volumes: %w", err)
	}
	out := make([]domain.StagingVolumeState, 0, len(rows))
	for _, row := range rows {
		st := ""
		if row.RunState != nil {
			st = string(*row.RunState)
		}
		out = append(out, domain.StagingVolumeState{
			PVCName: row.PvcName, RunState: st, RunEndedAt: timePtr(row.RunEndedAt), CreatedAt: timeVal(row.CreatedAt),
		})
	}
	return out, nil
}
