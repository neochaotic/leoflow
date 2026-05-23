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

// ClearTaskInstances resets tasks to none for re-run, optionally resetting the
// parent run to queued. When onlyFailed is true, only tasks currently in a
// failed-ish state (failed, upstream_failed, up_for_retry) are reset; with an
// empty taskIDs and onlyFailed, every failed task in the run is cleared. It
// returns the number of task instances actually reset.
func (r *Repository) ClearTaskInstances(ctx context.Context, tenant, dagID, runID string, taskIDs []string, onlyFailed, resetDagRun bool) (int, error) {
	dag, err := r.resolveDag(ctx, tenant, dagID)
	if err != nil {
		return 0, err
	}
	run, err := r.q.GetDagRun(ctx, queries.GetDagRunParams{DagID: dag.ID, RunID: runID})
	if err != nil {
		return 0, mapNotFound(err)
	}
	cleared, err := r.resetTaskInstances(ctx, run.ID, taskIDs, onlyFailed)
	if err != nil {
		return cleared, err
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

// resetTaskInstances applies the clear semantics: a specific task list, or (with
// an empty list and onlyFailed) every failed task in the run.
func (r *Repository) resetTaskInstances(ctx context.Context, runID pgtype.UUID, taskIDs []string, onlyFailed bool) (int, error) {
	if len(taskIDs) == 0 {
		if !onlyFailed {
			return 0, nil
		}
		n, err := r.q.ResetAllFailedTaskInstances(ctx, runID)
		if err != nil {
			return 0, fmt.Errorf("clearing failed tasks: %w", err)
		}
		return int(n), nil
	}
	cleared := 0
	for _, taskID := range taskIDs {
		if onlyFailed {
			n, err := r.q.ResetFailedTaskInstance(ctx, queries.ResetFailedTaskInstanceParams{DagRunID: runID, TaskID: taskID})
			if err != nil {
				return cleared, fmt.Errorf("clearing failed task %q: %w", taskID, err)
			}
			cleared += int(n)
			continue
		}
		if err := r.q.ResetTaskInstanceToNone(ctx, queries.ResetTaskInstanceToNoneParams{DagRunID: runID, TaskID: taskID}); err != nil {
			return cleared, fmt.Errorf("clearing task %q: %w", taskID, err)
		}
		cleared++
	}
	return cleared, nil
}

// LatestRunsForDags returns up to perDag most-recent runs for each named DAG,
// keyed by dag_id, in a single windowed query (no per-DAG round trips).
func (r *Repository) LatestRunsForDags(ctx context.Context, tenant string, dagIDs []string, perDag int) (map[string][]domain.DagRun, error) {
	if len(dagIDs) == 0 {
		return map[string][]domain.DagRun{}, nil
	}
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return nil, err
	}
	rows, err := r.q.LatestRunsForDags(ctx, queries.LatestRunsForDagsParams{
		TenantID: tid, Column2: dagIDs, Limit: toInt32(perDag),
	})
	if err != nil {
		return nil, fmt.Errorf("latest runs for dags: %w", err)
	}
	out := make(map[string][]domain.DagRun, len(dagIDs))
	for _, row := range rows {
		out[row.DagIDText] = append(out[row.DagIDText], domain.DagRun{
			DagID:       row.DagIDText,
			RunID:       row.RunID,
			LogicalDate: timeVal(row.LogicalDate),
			State:       domain.DagRunState(row.State),
			RunType:     string(row.Trigger),
			QueuedAt:    timeVal(row.QueuedAt),
			StartedAt:   timePtr(row.StartedAt),
			EndedAt:     timePtr(row.EndedAt),
		})
	}
	return out, nil
}

// TaskInstancesForRuns returns the task instances of the given runs of a DAG in
// one query, ordered by run_id, task_id, try_number, for the grid summaries.
func (r *Repository) TaskInstancesForRuns(ctx context.Context, tenant, dagID string, runIDs []string) ([]domain.TaskInstance, error) {
	if len(runIDs) == 0 {
		return nil, nil
	}
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return nil, err
	}
	rows, err := r.q.TaskInstancesForDagRuns(ctx, queries.TaskInstancesForDagRunsParams{
		TenantID: tid, DagID: dagID, Column3: runIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("task instances for runs: %w", err)
	}
	out := make([]domain.TaskInstance, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.TaskInstance{
			DagID:     dagID,
			RunID:     row.RunID,
			TaskID:    row.TaskID,
			TryNumber: int(row.TryNumber),
			State:     domain.TaskState(row.State),
			StartedAt: timePtr(row.StartedAt),
			EndedAt:   timePtr(row.EndedAt),
		})
	}
	return out, nil
}

// ListDagVersions returns the DAG's versions, newest first, with a 1-based
// version_number the UI uses to query version-scoped structure.
func (r *Repository) ListDagVersions(ctx context.Context, tenant, dagID string) ([]domain.DagVersion, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return nil, err
	}
	rows, err := r.q.ListDagVersions(ctx, queries.ListDagVersionsParams{TenantID: tid, DagID: dagID})
	if err != nil {
		return nil, fmt.Errorf("listing dag versions: %w", err)
	}
	out := make([]domain.DagVersion, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.DagVersion{
			ID:            uuidToString(row.ID),
			VersionNumber: int(row.VersionNumber),
			CreatedAt:     timeVal(row.CreatedAt),
		})
	}
	return out, nil
}

// GetCurrentSpec returns the parsed spec of the DAG's current version, or
// domain.ErrNotFound if the DAG or its current version does not exist.
func (r *Repository) GetCurrentSpec(ctx context.Context, tenant, dagID string) (domain.DAGSpec, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return domain.DAGSpec{}, err
	}
	raw, err := r.q.GetCurrentDagSpec(ctx, queries.GetCurrentDagSpecParams{TenantID: tid, DagID: dagID})
	if err != nil {
		return domain.DAGSpec{}, mapNotFound(err)
	}
	var spec domain.DAGSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return domain.DAGSpec{}, fmt.Errorf("decoding current spec: %w", err)
	}
	return spec, nil
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
	if err := r.q.CreateAuditLog(ctx, queries.CreateAuditLogParams{
		TenantID:     tid,
		Action:       "dag.version.register",
		ResourceType: strPtr("dag"),
		ResourceID:   strPtr(spec.DagID),
	}); err != nil {
		return false, fmt.Errorf("writing audit log: %w", err)
	}
	return true, nil
}

// BootstrapAdmin creates a default admin user with the given password when the
// tenant has no users yet, assigning the seeded admin role. It returns whether
// a user was created (false when users already exist).
func (r *Repository) BootstrapAdmin(ctx context.Context, tenant, email, password string) (bool, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return false, err
	}
	n, err := r.q.CountUsers(ctx, tid)
	if err != nil {
		return false, fmt.Errorf("counting users: %w", err)
	}
	if n > 0 {
		return false, nil
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return false, err
	}
	uid, err := r.q.CreateUser(ctx, queries.CreateUserParams{TenantID: tid, Email: email, PasswordHash: strPtr(hash)})
	if err != nil {
		return false, fmt.Errorf("creating admin user: %w", err)
	}
	roleID, err := r.q.GetRoleByName(ctx, queries.GetRoleByNameParams{TenantID: tid, Name: "admin"})
	if err != nil {
		return false, fmt.Errorf("loading admin role: %w", err)
	}
	if err := r.q.AssignUserRole(ctx, queries.AssignUserRoleParams{UserID: uid, RoleID: roleID}); err != nil {
		return false, fmt.Errorf("assigning admin role: %w", err)
	}
	return true, nil
}

// compile-time assurance that Repository satisfies the auth user store.
var _ auth.UserStore = (*Repository)(nil)

// ListAuditLogs returns a page of audit-log entries for the tenant, newest
// first, optionally filtered to a single DAG (dagID == "" means no filter).
func (r *Repository) ListAuditLogs(ctx context.Context, tenant, dagID string, limit, offset int) ([]domain.AuditLogEntry, int, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return nil, 0, err
	}
	var dagFilter *string
	if dagID != "" {
		dagFilter = &dagID
	}
	rows, err := r.q.ListAuditLogs(ctx, queries.ListAuditLogsParams{
		TenantID: tid, Limit: toInt32(limit), Offset: toInt32(offset), DagID: dagFilter,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("listing audit logs: %w", err)
	}
	total, err := r.q.CountAuditLogs(ctx, queries.CountAuditLogsParams{TenantID: tid, DagID: dagFilter})
	if err != nil {
		return nil, 0, fmt.Errorf("counting audit logs: %w", err)
	}
	out := make([]domain.AuditLogEntry, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.AuditLogEntry{
			ID:           row.ID,
			When:         timeVal(row.OccurredAt),
			Action:       row.Action,
			ResourceType: strOrEmpty(row.ResourceType),
			ResourceID:   strOrEmpty(row.ResourceID),
			Owner:        row.Owner,
			Extra:        string(row.Metadata),
		})
	}
	return out, int(total), nil
}

// DeleteDag removes a DAG and (via ON DELETE CASCADE) its versions, runs, task
// instances, and XCom index rows. It returns ErrNotFound when no DAG matched.
func (r *Repository) DeleteDag(ctx context.Context, tenant, dagID string) error {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return err
	}
	rows, err := r.q.DeleteDag(ctx, queries.DeleteDagParams{TenantID: tid, DagID: dagID})
	if err != nil {
		return fmt.Errorf("deleting dag: %w", err)
	}
	if rows == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// ListDagsFiltered returns a page of active DAGs for the tenant, optionally
// filtered by paused state and/or latest-run state, with the matching total.
// An empty runState or nil paused disables that filter.
func (r *Repository) ListDagsFiltered(ctx context.Context, tenant, runState string, paused *bool, limit, offset int) ([]domain.DAG, int, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return nil, 0, err
	}
	var rs *queries.DagRunState
	if runState != "" {
		s := queries.DagRunState(runState)
		rs = &s
	}
	rows, err := r.q.ListDagsFiltered(ctx, queries.ListDagsFilteredParams{
		TenantID: tid, Limit: toInt32(limit), Offset: toInt32(offset), Paused: paused, RunState: rs,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("listing filtered dags: %w", err)
	}
	total, err := r.q.CountDagsFiltered(ctx, queries.CountDagsFilteredParams{TenantID: tid, Paused: paused, RunState: rs})
	if err != nil {
		return nil, 0, fmt.Errorf("counting filtered dags: %w", err)
	}
	out := make([]domain.DAG, 0, len(rows))
	for _, d := range rows {
		out = append(out, mapDag(d))
	}
	return out, int(total), nil
}

// ListVariables returns a page of variables for the tenant and the total count.
func (r *Repository) ListVariables(ctx context.Context, tenant string, limit, offset int) ([]domain.Variable, int, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.q.ListVariables(ctx, queries.ListVariablesParams{TenantID: tid, Limit: toInt32(limit), Offset: toInt32(offset)})
	if err != nil {
		return nil, 0, fmt.Errorf("listing variables: %w", err)
	}
	total, err := r.q.CountVariables(ctx, tid)
	if err != nil {
		return nil, 0, fmt.Errorf("counting variables: %w", err)
	}
	out := make([]domain.Variable, 0, len(rows))
	for _, v := range rows {
		out = append(out, domain.Variable{Key: v.Key, Value: v.Value, Description: strOrEmpty(v.Description)})
	}
	return out, int(total), nil
}

// GetVariable returns one variable by key, or ErrNotFound.
func (r *Repository) GetVariable(ctx context.Context, tenant, key string) (domain.Variable, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return domain.Variable{}, err
	}
	v, err := r.q.GetVariable(ctx, queries.GetVariableParams{TenantID: tid, Key: key})
	if err != nil {
		return domain.Variable{}, mapNotFound(err)
	}
	return domain.Variable{Key: v.Key, Value: v.Value, Description: strOrEmpty(v.Description)}, nil
}

// SetVariable creates or updates a variable.
func (r *Repository) SetVariable(ctx context.Context, tenant string, v domain.Variable) error {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return err
	}
	if err := r.q.UpsertVariable(ctx, queries.UpsertVariableParams{
		TenantID: tid, Key: v.Key, Value: v.Value, Description: strPtr(v.Description),
	}); err != nil {
		return fmt.Errorf("upserting variable: %w", err)
	}
	return nil
}

// DeleteVariable removes a variable, returning ErrNotFound when none matched.
func (r *Repository) DeleteVariable(ctx context.Context, tenant, key string) error {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return err
	}
	rows, err := r.q.DeleteVariable(ctx, queries.DeleteVariableParams{TenantID: tid, Key: key})
	if err != nil {
		return fmt.Errorf("deleting variable: %w", err)
	}
	if rows == 0 {
		return domain.ErrNotFound
	}
	return nil
}
