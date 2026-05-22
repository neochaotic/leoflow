package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/neochaotic/leoflow/internal/auth"
	"github.com/neochaotic/leoflow/internal/domain"
	"github.com/neochaotic/leoflow/internal/storage/queries"
)

const defaultMaxActiveRuns = 16

// Repository implements the API resource and auth user-store interfaces over
// Postgres using the sqlc-generated query set.
type Repository struct {
	q *queries.Queries
}

// NewRepository builds a Repository backed by the given Postgres connection.
func NewRepository(pg *Postgres) *Repository {
	return &Repository{q: pg.Queries}
}

func toInt32(n int) int32 {
	switch {
	case n < 0:
		return 0
	case n > math.MaxInt32:
		return math.MaxInt32
	default:
		return int32(n)
	}
}

func mapNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	return err
}

func (r *Repository) tenantID(ctx context.Context, name string) (pgtype.UUID, error) {
	t, err := r.q.GetTenantByName(ctx, name)
	if err != nil {
		return pgtype.UUID{}, mapNotFound(err)
	}
	return t.ID, nil
}

// FindUserByLogin loads a user and its bcrypt hash for authentication.
func (r *Repository) FindUserByLogin(ctx context.Context, tenant, username string) (*auth.User, string, error) {
	row, err := r.q.GetUserByEmail(ctx, queries.GetUserByEmailParams{Name: tenant, Email: username})
	if err != nil {
		return nil, "", mapNotFound(err)
	}
	if !row.IsActive {
		return nil, "", auth.ErrInvalidCredentials
	}
	roles, err := r.q.GetUserRoles(ctx, row.ID)
	if err != nil {
		return nil, "", fmt.Errorf("loading roles: %w", err)
	}
	perms, err := r.q.GetUserPermissions(ctx, row.ID)
	if err != nil {
		return nil, "", fmt.Errorf("loading permissions: %w", err)
	}
	user := &auth.User{ID: uuidToString(row.ID), TenantID: tenant, Email: row.Email, Roles: roles}
	for _, p := range perms {
		user.Permissions = append(user.Permissions, auth.Permission{Action: p.Action, Resource: p.Resource})
	}
	return user, strOrEmpty(row.PasswordHash), nil
}

// ListDags returns a page of DAGs for the tenant and the total count.
func (r *Repository) ListDags(ctx context.Context, tenant string, limit, offset int) ([]domain.DAG, int, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.q.ListDags(ctx, queries.ListDagsParams{TenantID: tid, Limit: toInt32(limit), Offset: toInt32(offset)})
	if err != nil {
		return nil, 0, fmt.Errorf("listing dags: %w", err)
	}
	total, err := r.q.CountDags(ctx, tid)
	if err != nil {
		return nil, 0, fmt.Errorf("counting dags: %w", err)
	}
	out := make([]domain.DAG, 0, len(rows))
	for _, d := range rows {
		out = append(out, mapDag(d))
	}
	return out, int(total), nil
}

// GetDag returns a single DAG by its user-facing id.
func (r *Repository) GetDag(ctx context.Context, tenant, dagID string) (domain.DAG, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return domain.DAG{}, err
	}
	d, err := r.q.GetDagByDagID(ctx, queries.GetDagByDagIDParams{TenantID: tid, DagID: dagID})
	if err != nil {
		return domain.DAG{}, mapNotFound(err)
	}
	return mapDag(d), nil
}

// SetPaused toggles the paused flag of a DAG.
func (r *Repository) SetPaused(ctx context.Context, tenant, dagID string, paused bool) (domain.DAG, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return domain.DAG{}, err
	}
	d, err := r.q.SetDagPaused(ctx, queries.SetDagPausedParams{TenantID: tid, DagID: dagID, IsPaused: paused})
	if err != nil {
		return domain.DAG{}, mapNotFound(err)
	}
	return mapDag(d), nil
}

func (r *Repository) resolveDag(ctx context.Context, tenant, dagID string) (queries.Dag, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return queries.Dag{}, err
	}
	d, err := r.q.GetDagByDagID(ctx, queries.GetDagByDagIDParams{TenantID: tid, DagID: dagID})
	if err != nil {
		return queries.Dag{}, mapNotFound(err)
	}
	return d, nil
}

// ListDagRuns returns a page of runs for a DAG and the total count.
func (r *Repository) ListDagRuns(ctx context.Context, tenant, dagID string, limit, offset int) ([]domain.DagRun, int, error) {
	dag, err := r.resolveDag(ctx, tenant, dagID)
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.q.ListDagRunsByDag(ctx, queries.ListDagRunsByDagParams{DagID: dag.ID, Limit: toInt32(limit), Offset: toInt32(offset)})
	if err != nil {
		return nil, 0, fmt.Errorf("listing dag runs: %w", err)
	}
	total, err := r.q.CountDagRunsByDag(ctx, dag.ID)
	if err != nil {
		return nil, 0, fmt.Errorf("counting dag runs: %w", err)
	}
	out := make([]domain.DagRun, 0, len(rows))
	for _, run := range rows {
		out = append(out, mapDagRun(run, dagID))
	}
	return out, int(total), nil
}

// GetDagRun returns a single run by its run id.
func (r *Repository) GetDagRun(ctx context.Context, tenant, dagID, runID string) (domain.DagRun, error) {
	dag, err := r.resolveDag(ctx, tenant, dagID)
	if err != nil {
		return domain.DagRun{}, err
	}
	run, err := r.q.GetDagRun(ctx, queries.GetDagRunParams{DagID: dag.ID, RunID: runID})
	if err != nil {
		return domain.DagRun{}, mapNotFound(err)
	}
	return mapDagRun(run, dagID), nil
}

// CreateDagRun inserts a new run for a DAG at its current version.
func (r *Repository) CreateDagRun(ctx context.Context, tenant, dagID string, run domain.DagRun) (domain.DagRun, error) {
	dag, err := r.resolveDag(ctx, tenant, dagID)
	if err != nil {
		return domain.DagRun{}, err
	}
	created, err := r.q.CreateDagRun(ctx, queries.CreateDagRunParams{
		TenantID:     dag.TenantID,
		DagID:        dag.ID,
		DagVersionID: dag.CurrentVersionID,
		RunID:        run.RunID,
		LogicalDate:  pgtype.Timestamptz{Time: run.LogicalDate, Valid: true},
		State:        queries.DagRunState(run.State),
		Trigger:      queries.DagRunTrigger(run.RunType),
		Note:         strPtr(run.Note),
	})
	if err != nil {
		return domain.DagRun{}, fmt.Errorf("creating dag run: %w", err)
	}
	return mapDagRun(created, dagID), nil
}

// ListTaskInstances returns the task instances of a run.
func (r *Repository) ListTaskInstances(ctx context.Context, tenant, dagID, runID string, _, _ int) ([]domain.TaskInstance, int, error) {
	dag, err := r.resolveDag(ctx, tenant, dagID)
	if err != nil {
		return nil, 0, err
	}
	run, err := r.q.GetDagRun(ctx, queries.GetDagRunParams{DagID: dag.ID, RunID: runID})
	if err != nil {
		return nil, 0, mapNotFound(err)
	}
	rows, err := r.q.ListTaskInstancesByRun(ctx, run.ID)
	if err != nil {
		return nil, 0, fmt.Errorf("listing task instances: %w", err)
	}
	out := make([]domain.TaskInstance, 0, len(rows))
	for _, ti := range rows {
		out = append(out, mapTaskInstance(ti, dagID, runID))
	}
	return out, len(out), nil
}

// ClearTaskInstances resets the given tasks to none for re-run, optionally
// resetting the parent run to queued.
func (r *Repository) ClearTaskInstances(ctx context.Context, tenant, dagID, runID string, taskIDs []string, resetDagRun bool) (int, error) {
	dag, err := r.resolveDag(ctx, tenant, dagID)
	if err != nil {
		return 0, err
	}
	run, err := r.q.GetDagRun(ctx, queries.GetDagRunParams{DagID: dag.ID, RunID: runID})
	if err != nil {
		return 0, mapNotFound(err)
	}
	cleared := 0
	for _, taskID := range taskIDs {
		if err := r.q.ResetTaskInstanceToNone(ctx, queries.ResetTaskInstanceToNoneParams{DagRunID: run.ID, TaskID: taskID}); err != nil {
			return cleared, fmt.Errorf("clearing task %q: %w", taskID, err)
		}
		cleared++
	}
	if resetDagRun {
		if _, err := r.q.UpdateDagRunState(ctx, queries.UpdateDagRunStateParams{
			ID:        run.ID,
			State:     queries.DagRunStateQueued,
			StartedAt: pgtype.Timestamptz{},
			EndedAt:   pgtype.Timestamptz{},
		}); err != nil {
			return cleared, fmt.Errorf("resetting dag run: %w", err)
		}
	}
	return cleared, nil
}

// RegisterDagVersion upserts the DAG and inserts a version keyed by specHash,
// setting it as current. It is idempotent: an existing hash yields created=false.
func (r *Repository) RegisterDagVersion(ctx context.Context, tenant string, spec domain.DAGSpec, specHash string) (bool, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return false, err
	}
	maxRuns := spec.MaxActiveRuns
	if maxRuns == 0 {
		maxRuns = defaultMaxActiveRuns
	}
	dag, err := r.q.UpsertDag(ctx, queries.UpsertDagParams{
		TenantID:         tid,
		DagID:            spec.DagID,
		Description:      strPtr(spec.Description),
		Owner:            strPtr(spec.Owner),
		Schedule:         spec.Schedule,
		ScheduleTimezone: strPtr(spec.ScheduleTZ),
		MaxActiveRuns:    toInt32(maxRuns),
		Catchup:          spec.Catchup,
	})
	if err != nil {
		return false, fmt.Errorf("upserting dag: %w", err)
	}
	if _, verr := r.q.GetDagVersionByHash(ctx, queries.GetDagVersionByHashParams{DagID: dag.ID, SpecHash: specHash}); verr == nil {
		return false, nil
	} else if !errors.Is(verr, pgx.ErrNoRows) {
		return false, fmt.Errorf("checking existing version: %w", verr)
	}
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return false, fmt.Errorf("encoding spec: %w", err)
	}
	version, err := r.q.InsertDagVersion(ctx, queries.InsertDagVersionParams{
		DagID:          dag.ID,
		Version:        spec.DagVersion,
		ImageReference: spec.Image,
		Spec:           specJSON,
		SpecHash:       specHash,
		CreatedBy:      pgtype.UUID{},
	})
	if err != nil {
		return false, fmt.Errorf("inserting version: %w", err)
	}
	if err := r.q.SetCurrentDagVersion(ctx, queries.SetCurrentDagVersionParams{ID: dag.ID, CurrentVersionID: version.ID}); err != nil {
		return false, fmt.Errorf("setting current version: %w", err)
	}
	return true, nil
}

// compile-time assurance that Repository satisfies the auth user store.
var _ auth.UserStore = (*Repository)(nil)
