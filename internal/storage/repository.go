package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/neochaotic/leoflow/internal/auth"
	"github.com/neochaotic/leoflow/internal/domain"
	"github.com/neochaotic/leoflow/internal/secrets"
	"github.com/neochaotic/leoflow/internal/storage/queries"
)

const defaultMaxActiveRuns = 16

// Repository implements the API resource and auth user-store interfaces over
// Postgres using the sqlc-generated query set.
type Repository struct {
	q      *queries.Queries
	cipher secrets.Cipher
}

// NewRepository builds a Repository backed by the given Postgres connection.
func NewRepository(pg *Postgres) *Repository {
	return &Repository{q: pg.Queries}
}

// SetCipher attaches the encryption cipher used for connection secrets (ADR
// 0019). Without it, connection writes fail rather than storing plaintext.
func (r *Repository) SetCipher(c secrets.Cipher) { r.cipher = c }

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
	// The trigger's audit entry is written by the API handler, where the acting
	// user is known (so the Audit Log shows the owner).
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

// SetDagRunState sets a DAG run's state directly, backing the UI's mark run
// success/failed actions. Terminal states stamp ended_at; re-opening to a
// non-terminal state clears it. started_at is preserved.
func (r *Repository) SetDagRunState(ctx context.Context, tenant, dagID, runID, state string) error {
	dag, err := r.resolveDag(ctx, tenant, dagID)
	if err != nil {
		return err
	}
	run, err := r.q.GetDagRun(ctx, queries.GetDagRunParams{DagID: dag.ID, RunID: runID})
	if err != nil {
		return mapNotFound(err)
	}
	ended := pgtype.Timestamptz{}
	if domain.DagRunState(state).IsTerminal() {
		ended = pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	}
	if _, err := r.q.UpdateDagRunState(ctx, queries.UpdateDagRunStateParams{
		ID: run.ID, State: queries.DagRunState(state), StartedAt: run.StartedAt, EndedAt: ended,
	}); err != nil {
		return fmt.Errorf("setting dag run state: %w", err)
	}
	return nil
}

// RecordTaskActionAudit logs a task-level action (clear, mark state) with the
// acting user and the run/task/try in metadata, so the Audit Log view shows the
// owner and the task columns. Scoped to the DAG (resource_id = dag_id) so it
// appears on the DAG's Audit Log tab.
func (r *Repository) RecordTaskActionAudit(ctx context.Context, tenant, userID, action, dagID, runID, taskID string, tryNumber int) error {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return err
	}
	var uid pgtype.UUID
	if u, perr := parseUUID(userID); perr == nil {
		uid = u
	}
	fields := map[string]any{"run_id": runID}
	if taskID != "" { // run-level events (e.g. trigger) carry no task columns
		fields["task_id"] = taskID
		fields["try_number"] = tryNumber
	}
	meta, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("encoding audit metadata: %w", err)
	}
	if err := r.q.CreateAuditLog(ctx, queries.CreateAuditLogParams{
		TenantID: tid, UserID: uid, Action: action,
		ResourceType: strPtr("dag"), ResourceID: strPtr(dagID), Metadata: meta,
	}); err != nil {
		return fmt.Errorf("writing task action audit: %w", err)
	}
	return nil
}

// SetTaskInstanceState sets a task instance's state directly, backing the UI's
// "mark success"/"mark failed" actions. It does not run the task.
func (r *Repository) SetTaskInstanceState(ctx context.Context, tenant, dagID, runID, taskID, state string) error {
	dag, err := r.resolveDag(ctx, tenant, dagID)
	if err != nil {
		return err
	}
	run, err := r.q.GetDagRun(ctx, queries.GetDagRunParams{DagID: dag.ID, RunID: runID})
	if err != nil {
		return mapNotFound(err)
	}
	if err := r.q.UpdateTaskInstanceStateByRunTask(ctx, queries.UpdateTaskInstanceStateByRunTaskParams{
		State: queries.TaskState(state), DagRunID: run.ID, TaskID: taskID,
	}); err != nil {
		return fmt.Errorf("setting task %q state: %w", taskID, err)
	}
	return nil
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
		Tags:             spec.Tags,
		Schedule:         spec.Schedule,
		ScheduleTimezone: strPtr(spec.ScheduleTZ),
		StartDate:        parseTimestamptz(spec.StartDate),
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

// AddFavorite marks a DAG as a favorite for the user (idempotent).
func (r *Repository) AddFavorite(ctx context.Context, tenant, userID, dagID string) error {
	if err := r.q.AddFavorite(ctx, queries.AddFavoriteParams{Tenant: tenant, UserID: userID, DagID: dagID}); err != nil {
		return fmt.Errorf("adding favorite: %w", err)
	}
	return nil
}

// RemoveFavorite clears a DAG's favorite mark for the user (idempotent).
func (r *Repository) RemoveFavorite(ctx context.Context, tenant, userID, dagID string) error {
	if err := r.q.RemoveFavorite(ctx, queries.RemoveFavoriteParams{Tenant: tenant, UserID: userID, DagID: dagID}); err != nil {
		return fmt.Errorf("removing favorite: %w", err)
	}
	return nil
}

// FavoriteDagIDs returns the set of DAG ids the user has favorited.
func (r *Repository) FavoriteDagIDs(ctx context.Context, tenant, userID string) (map[string]bool, error) {
	ids, err := r.q.ListFavoriteDagIDs(ctx, queries.ListFavoriteDagIDsParams{Tenant: tenant, UserID: userID})
	if err != nil {
		return nil, fmt.Errorf("listing favorites: %w", err)
	}
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set, nil
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

// encOrEmpty encrypts a non-empty value, returning a nil pointer for empty input.
func (r *Repository) encOrEmpty(plain string) (*string, error) {
	if plain == "" {
		return nil, nil //nolint:nilnil // empty secret maps to a NULL column; no value and no error is correct
	}
	enc, err := r.cipher.Encrypt(plain)
	if err != nil {
		return nil, err
	}
	return &enc, nil
}

// SetConnection creates or updates a connection, encrypting password and extra
// at rest. It fails if no encryption cipher is configured (never stores a
// credential in plaintext — ADR 0019).
func (r *Repository) SetConnection(ctx context.Context, tenant string, c domain.Connection) error {
	if r.cipher == nil {
		return secrets.ErrNoKey
	}
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return err
	}
	encPass, err := r.encOrEmpty(c.Password)
	if err != nil {
		return fmt.Errorf("encrypting password: %w", err)
	}
	encExtra, err := r.encOrEmpty(c.Extra)
	if err != nil {
		return fmt.Errorf("encrypting extra: %w", err)
	}
	var port *int32
	if c.Port != nil {
		p := toInt32(*c.Port)
		port = &p
	}
	return r.q.UpsertConnection(ctx, queries.UpsertConnectionParams{
		TenantID: tid, ConnID: c.ConnID, ConnType: c.ConnType,
		Host: strPtr(c.Host), ConnSchema: strPtr(c.Schema), Login: strPtr(c.Login),
		Password: encPass, Port: port, Extra: encExtra, Description: strPtr(c.Description),
	})
}

// GetConnection returns a connection with extra decrypted; the password is not
// returned (write-only). Returns ErrNotFound when absent.
func (r *Repository) GetConnection(ctx context.Context, tenant, connID string) (domain.Connection, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return domain.Connection{}, err
	}
	row, err := r.q.GetConnection(ctx, queries.GetConnectionParams{TenantID: tid, ConnID: connID})
	if err != nil {
		return domain.Connection{}, mapNotFound(err)
	}
	extra, err := r.decryptExtra(row.Extra)
	if err != nil {
		return domain.Connection{}, err
	}
	return domain.Connection{
		ConnID: row.ConnID, ConnType: row.ConnType, Host: strOrEmpty(row.Host),
		Schema: strOrEmpty(row.ConnSchema), Login: strOrEmpty(row.Login),
		Port: int32PtrToInt(row.Port), Extra: extra, Description: strOrEmpty(row.Description),
	}, nil
}

// ListConnections returns a page of connections (no passwords) and the total.
func (r *Repository) ListConnections(ctx context.Context, tenant string, limit, offset int) ([]domain.Connection, int, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.q.ListConnections(ctx, queries.ListConnectionsParams{TenantID: tid, Limit: toInt32(limit), Offset: toInt32(offset)})
	if err != nil {
		return nil, 0, fmt.Errorf("listing connections: %w", err)
	}
	total, err := r.q.CountConnections(ctx, tid)
	if err != nil {
		return nil, 0, fmt.Errorf("counting connections: %w", err)
	}
	out := make([]domain.Connection, 0, len(rows))
	for _, row := range rows {
		extra, derr := r.decryptExtra(row.Extra)
		if derr != nil {
			return nil, 0, derr
		}
		out = append(out, domain.Connection{
			ConnID: row.ConnID, ConnType: row.ConnType, Host: strOrEmpty(row.Host),
			Schema: strOrEmpty(row.ConnSchema), Login: strOrEmpty(row.Login),
			Port: int32PtrToInt(row.Port), Extra: extra, Description: strOrEmpty(row.Description),
		})
	}
	return out, int(total), nil
}

// decryptExtra decrypts a stored extra blob, tolerating a nil cipher (returns
// empty) and an empty value.
func (r *Repository) decryptExtra(enc *string) (string, error) {
	if enc == nil || *enc == "" || r.cipher == nil {
		return "", nil
	}
	plain, err := r.cipher.Decrypt(*enc)
	if err != nil {
		return "", fmt.Errorf("decrypting extra: %w", err)
	}
	return plain, nil
}

// DeleteConnection removes a connection, returning ErrNotFound when none matched.
func (r *Repository) DeleteConnection(ctx context.Context, tenant, connID string) error {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return err
	}
	rows, err := r.q.DeleteConnection(ctx, queries.DeleteConnectionParams{TenantID: tid, ConnID: connID})
	if err != nil {
		return fmt.Errorf("deleting connection: %w", err)
	}
	if rows == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// int32PtrToInt converts a nullable int32 column to a *int.
func int32PtrToInt(p *int32) *int {
	if p == nil {
		return nil
	}
	v := int(*p)
	return &v
}

// ClearDagHistory deletes a DAG's runs (cascading task instances and XCom index
// rows) while keeping the DAG and its versions registered — the safe "clear"
// the UI trash maps to (ADR 0020). Returns ErrNotFound when the DAG is absent.
func (r *Repository) ClearDagHistory(ctx context.Context, tenant, dagID string) error {
	dag, err := r.resolveDag(ctx, tenant, dagID)
	if err != nil {
		return err
	}
	if _, err := r.q.ClearDagRuns(ctx, dag.ID); err != nil {
		return fmt.Errorf("clearing dag history: %w", err)
	}
	return nil
}
