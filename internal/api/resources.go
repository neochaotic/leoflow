package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/neochaotic/leoflow/internal/domain"
)

// ErrNotFound is returned by repositories when a resource does not exist.
var ErrNotFound = domain.ErrNotFound

// DagRepository reads, updates, and deletes registered DAGs.
type DagRepository interface {
	ListDags(ctx context.Context, tenant string, limit, offset int) ([]domain.DAG, int, error)
	GetDag(ctx context.Context, tenant, dagID string) (domain.DAG, error)
	SetPaused(ctx context.Context, tenant, dagID string, paused bool) (domain.DAG, error)
	DeleteDag(ctx context.Context, tenant, dagID string) error
	ClearDagHistory(ctx context.Context, tenant, dagID string) error
	ListDagsFiltered(ctx context.Context, tenant, runState string, paused *bool, limit, offset int) ([]domain.DAG, int, error)
}

// DagRunRepository reads and creates DAG runs.
type DagRunRepository interface {
	ListDagRuns(ctx context.Context, tenant, dagID string, limit, offset int) ([]domain.DagRun, int, error)
	GetDagRun(ctx context.Context, tenant, dagID, runID string) (domain.DagRun, error)
	CreateDagRun(ctx context.Context, tenant, dagID string, run domain.DagRun) (domain.DagRun, error)
}

// TaskInstanceRepository reads task instances, clears them for re-run, and sets
// their state directly (the UI's mark-success/failed actions).
type TaskInstanceRepository interface {
	ListTaskInstances(ctx context.Context, tenant, dagID, runID string, limit, offset int) ([]domain.TaskInstance, int, error)
	ClearTaskInstances(ctx context.Context, tenant, dagID, runID string, taskIDs []string, onlyFailed, resetDagRun bool) (int, error)
	SetTaskInstanceState(ctx context.Context, tenant, dagID, runID, taskID, state string) error
}

func pagination(c *gin.Context) (limit, offset int) {
	limit, offset = 100, 0
	if n, err := strconv.Atoi(c.Query("limit")); err == nil && n > 0 {
		limit = n
	}
	if n, err := strconv.Atoi(c.Query("offset")); err == nil && n >= 0 {
		offset = n
	}
	return limit, offset
}

// setPaginationLinks sets an RFC 5988 Link header with next/prev relations.
func setPaginationLinks(c *gin.Context, total, limit, offset int) {
	links := make([]string, 0, 2)
	path := c.Request.URL.Path
	if offset+limit < total {
		links = append(links, fmt.Sprintf(`<%s?limit=%d&offset=%d>; rel="next"`, path, limit, offset+limit))
	}
	if offset > 0 {
		prev := offset - limit
		if prev < 0 {
			prev = 0
		}
		links = append(links, fmt.Sprintf(`<%s?limit=%d&offset=%d>; rel="prev"`, path, limit, prev))
	}
	if len(links) > 0 {
		c.Header("Link", strings.Join(links, ", "))
	}
}

func tenantOf(c *gin.Context) string {
	if u, ok := UserFromContext(c); ok && u.TenantID != "" {
		return u.TenantID
	}
	return "default"
}

func handleRepoError(c *gin.Context, err error) {
	if errors.Is(err, ErrNotFound) {
		AbortProblem(c, http.StatusNotFound, "not found", err.Error())
		return
	}
	AbortProblem(c, http.StatusInternalServerError, "internal error", err.Error())
}

func listDagsHandler(repo DagRepository) gin.HandlerFunc {
	return func(c *gin.Context) {
		limit, offset := pagination(c)
		dags, total, err := repo.ListDags(c.Request.Context(), tenantOf(c), limit, offset)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		out := dagCollectionDTO{Dags: make([]dagDTO, 0, len(dags)), TotalEntries: total}
		for _, d := range dags {
			out.Dags = append(out.Dags, toDagDTO(d))
		}
		setPaginationLinks(c, total, limit, offset)
		c.JSON(http.StatusOK, out)
	}
}

func getDagHandler(repo DagRepository) gin.HandlerFunc {
	return func(c *gin.Context) {
		d, err := repo.GetDag(c.Request.Context(), tenantOf(c), c.Param("dag_id"))
		if err != nil {
			handleRepoError(c, err)
			return
		}
		c.JSON(http.StatusOK, toDagDTO(d))
	}
}

func patchDagHandler(repo DagRepository) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body struct {
			IsPaused bool `json:"is_paused"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", err.Error())
			return
		}
		d, err := repo.SetPaused(c.Request.Context(), tenantOf(c), c.Param("dag_id"), body.IsPaused)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		c.JSON(http.StatusOK, toDagDTO(d))
	}
}

// deleteDagHandler implements DELETE /api/v2/dags/{dag_id}. By default (the UI
// trash) it CLEARS the DAG's run history — deletes its runs, task instances, and
// XCom while keeping the DAG registered — because a GitOps DAG does not reload on
// its own, so destroying it would be surprising and lossy (ADR 0020). With
// ?deregister=true it removes the DAG artifact entirely (cascade). Returns 204.
func deleteDagHandler(repo DagRepository) gin.HandlerFunc {
	return func(c *gin.Context) {
		dagID := c.Param("dag_id")
		var err error
		if c.Query("deregister") == "true" {
			err = repo.DeleteDag(c.Request.Context(), tenantOf(c), dagID)
		} else {
			err = repo.ClearDagHistory(c.Request.Context(), tenantOf(c), dagID)
		}
		if err != nil {
			handleRepoError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// listRunsFiltered returns a page of a DAG's runs, optionally restricted to the
// given states (the UI's "failed runs" widget filters with ?state=failed). With
// no state filter it pages directly; with one, it filters the full set first so
// the page and total reflect the filter (run counts are small — a SQL-level
// filter is a follow-up for scale).
func listRunsFiltered(c *gin.Context, repo DagRunRepository, states []string, limit, offset int) ([]domain.DagRun, int, error) {
	if len(states) == 0 {
		return repo.ListDagRuns(c.Request.Context(), tenantOf(c), c.Param("dag_id"), limit, offset)
	}
	want := make(map[string]bool, len(states))
	for _, s := range states {
		want[s] = true
	}
	all, _, err := repo.ListDagRuns(c.Request.Context(), tenantOf(c), c.Param("dag_id"), maxRunScan, 0)
	if err != nil {
		return nil, 0, err
	}
	filtered := make([]domain.DagRun, 0, len(all))
	for _, r := range all {
		if want[string(r.State)] {
			filtered = append(filtered, r)
		}
	}
	total := len(filtered)
	if offset >= total {
		return []domain.DagRun{}, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return filtered[offset:end], total, nil
}

// maxRunScan caps the in-memory state-filter scan of a DAG's runs.
const maxRunScan = 10000

func listDagRunsHandler(repo DagRunRepository) gin.HandlerFunc {
	return func(c *gin.Context) {
		// "~" is Airflow's wildcard for "all DAGs"; the UI home polls
		// GET /api/v2/dags/~/dagRuns for a global run view. Leoflow has no
		// cross-DAG run query yet, so degrade to an empty collection (200) rather
		// than 404 (which would resolve "~" as a missing DAG). Real cross-DAG
		// aggregation is a follow-up.
		if c.Param("dag_id") == "~" {
			c.JSON(http.StatusOK, dagRunCollectionDTO{DagRuns: []dagRunDTO{}, TotalEntries: 0})
			return
		}
		limit, offset := pagination(c)
		states := c.QueryArray("state")
		runs, total, err := listRunsFiltered(c, repo, states, limit, offset)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		out := dagRunCollectionDTO{DagRuns: make([]dagRunDTO, 0, len(runs)), TotalEntries: total}
		for _, r := range runs {
			out.DagRuns = append(out.DagRuns, toDagRunDTO(r))
		}
		setPaginationLinks(c, total, limit, offset)
		c.JSON(http.StatusOK, out)
	}
}

func getDagRunHandler(repo DagRunRepository) gin.HandlerFunc {
	return func(c *gin.Context) {
		r, err := repo.GetDagRun(c.Request.Context(), tenantOf(c), c.Param("dag_id"), c.Param("dag_run_id"))
		if err != nil {
			handleRepoError(c, err)
			return
		}
		c.JSON(http.StatusOK, toDagRunDTO(r))
	}
}

func createDagRunHandler(repo DagRunRepository) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body struct {
			DagRunID    string     `json:"dag_run_id"`
			LogicalDate *time.Time `json:"logical_date"`
			Note        string     `json:"note"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", err.Error())
			return
		}
		logical := time.Now().UTC()
		if body.LogicalDate != nil {
			logical = *body.LogicalDate
		}
		runID := body.DagRunID
		if runID == "" {
			// Airflow-style identifier; also avoids an empty/duplicate run_id,
			// which dag_runs forbids via UNIQUE (dag_id, run_id).
			runID = "manual__" + logical.Format(time.RFC3339)
		}
		run := domain.DagRun{
			RunID:       runID,
			LogicalDate: logical,
			State:       domain.DagRunStateQueued,
			RunType:     "manual",
			QueuedAt:    time.Now().UTC(),
			Note:        body.Note,
		}
		created, err := repo.CreateDagRun(c.Request.Context(), tenantOf(c), c.Param("dag_id"), run)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		c.JSON(http.StatusCreated, toDagRunDTO(created))
	}
}

func listTaskInstancesHandler(repo TaskInstanceRepository, runs DagRunRepository, versions DagVersionLister) gin.HandlerFunc {
	return func(c *gin.Context) {
		// "~" wildcards "all runs"; the UI overview polls
		// dagRuns/~/taskInstances?state=failed for its Failed-Tasks widget. We
		// have no cross-run task-instance query, so degrade to empty (200) rather
		// than 404 (which resolves "~" as a missing run). Follow-up: real query.
		if c.Param("dag_run_id") == "~" {
			c.JSON(http.StatusOK, taskInstanceCollectionDTO{TaskInstances: []taskInstanceDTO{}, TotalEntries: 0})
			return
		}
		limit, offset := pagination(c)
		tis, total, err := repo.ListTaskInstances(c.Request.Context(), tenantOf(c), c.Param("dag_id"), c.Param("dag_run_id"), limit, offset)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		// All instances in a run share the run-derived fields and DAG version, so
		// resolve them once rather than per task instance.
		logical, version := resolveRunContext(c, runs, versions)
		out := taskInstanceCollectionDTO{TaskInstances: make([]taskInstanceDTO, 0, len(tis)), TotalEntries: total}
		for _, ti := range tis {
			dto := toTaskInstanceDTO(ti)
			dto.LogicalDate, dto.RunAfter, dto.DagVersion = logical, logical, version
			out.TaskInstances = append(out.TaskInstances, dto)
		}
		setPaginationLinks(c, total, limit, offset)
		c.JSON(http.StatusOK, out)
	}
}

func clearTaskInstancesHandler(repo TaskInstanceRepository, runs DagRunRepository, versions DagVersionLister) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body struct {
			TaskIDs      []string `json:"task_ids"`
			DagRunID     string   `json:"dag_run_id"`
			OnlyFailed   *bool    `json:"only_failed"`
			ResetDagRuns *bool    `json:"reset_dag_runs"`
			DryRun       *bool    `json:"dry_run"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", err.Error())
			return
		}
		onlyFailed := body.OnlyFailed != nil && *body.OnlyFailed
		// The UI previews with dry_run=true before confirming; it expects the set of
		// affected task instances back (TaskInstanceCollectionResponse), not a count.
		affected := affectedTaskInstances(c, repo, runs, versions, body.DagRunID, body.TaskIDs, onlyFailed)
		if body.DryRun != nil && *body.DryRun {
			c.JSON(http.StatusOK, taskInstanceCollectionDTO{TaskInstances: affected, TotalEntries: len(affected)})
			return
		}
		reset := true
		if body.ResetDagRuns != nil {
			reset = *body.ResetDagRuns
		}
		if _, err := repo.ClearTaskInstances(c.Request.Context(), tenantOf(c), c.Param("dag_id"), body.DagRunID, body.TaskIDs, onlyFailed, reset); err != nil {
			handleRepoError(c, err)
			return
		}
		c.JSON(http.StatusOK, taskInstanceCollectionDTO{TaskInstances: affected, TotalEntries: len(affected)})
	}
}

// affectedTaskInstances returns the task instances a clear would touch: the named
// tasks (or all when none named), restricted to failed ones when onlyFailed.
func affectedTaskInstances(c *gin.Context, repo TaskInstanceRepository, runs DagRunRepository, versions DagVersionLister,
	runID string, taskIDs []string, onlyFailed bool) []taskInstanceDTO {
	tis, _, err := repo.ListTaskInstances(c.Request.Context(), tenantOf(c), c.Param("dag_id"), runID, 1000, 0)
	if err != nil {
		return []taskInstanceDTO{}
	}
	want := make(map[string]bool, len(taskIDs))
	for _, id := range taskIDs {
		want[id] = true
	}
	logical, version := resolveRunContextFor(c, runs, versions, runID)
	out := make([]taskInstanceDTO, 0, len(tis))
	for _, ti := range tis {
		if len(want) > 0 && !want[ti.TaskID] {
			continue
		}
		if onlyFailed && ti.State != domain.TaskStateFailed && ti.State != domain.TaskStateUpstreamFailed {
			continue
		}
		dto := toTaskInstanceDTO(ti)
		dto.LogicalDate, dto.RunAfter, dto.DagVersion = logical, logical, version
		out = append(out, dto)
	}
	return out
}

// markStateRequest is the body of the mark-success/failed PATCH.
type markStateRequest struct {
	NewState string `json:"new_state"`
}

// patchTaskInstanceHandler implements the UI's mark-success/failed PATCH on
// .../taskInstances/{task_id}[/{map_index}][/dry_run]. The catch-all action is
// the trailing path after the task_id; a "dry_run" segment previews without
// changing state. It returns the (would-be) updated task instance.
func patchTaskInstanceHandler(repo TaskInstanceRepository, runs DagRunRepository, versions DagVersionLister) gin.HandlerFunc {
	return func(c *gin.Context) {
		action := strings.Trim(c.Param("action"), "/")
		dryRun := action == "dry_run" || strings.HasSuffix(action, "/dry_run")
		var body markStateRequest
		if err := c.ShouldBindJSON(&body); err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", err.Error())
			return
		}
		if !validMarkState(body.NewState) {
			AbortProblem(c, http.StatusBadRequest, "bad request", "new_state must be success, failed, or skipped")
			return
		}
		dagID, runID, taskID := c.Param("dag_id"), c.Param("dag_run_id"), c.Param("task_id")
		if !dryRun {
			if err := repo.SetTaskInstanceState(c.Request.Context(), tenantOf(c), dagID, runID, taskID, body.NewState); err != nil {
				handleRepoError(c, err)
				return
			}
		}
		dto, ok := findTaskInstanceDTO(c, repo, runs, versions, runID, taskID)
		if !ok {
			AbortProblem(c, http.StatusNotFound, "not found", "task instance not found")
			return
		}
		if dryRun {
			s := body.NewState
			dto.State = &s // preview the state the confirm would apply
		}
		c.JSON(http.StatusOK, dto)
	}
}

// validMarkState reports whether s is a state the UI may set a task to directly.
func validMarkState(s string) bool {
	return s == string(domain.TaskStateSuccess) || s == string(domain.TaskStateFailed) || s == string(domain.TaskStateSkipped)
}

// findTaskInstanceDTO loads one task instance (map_index -1) for the run, enriched.
func findTaskInstanceDTO(c *gin.Context, repo TaskInstanceRepository, runs DagRunRepository, versions DagVersionLister, runID, taskID string) (taskInstanceDTO, bool) {
	tis, _, err := repo.ListTaskInstances(c.Request.Context(), tenantOf(c), c.Param("dag_id"), runID, 1000, 0)
	if err != nil {
		return taskInstanceDTO{}, false
	}
	for _, ti := range tis {
		if ti.TaskID == taskID {
			dto := toTaskInstanceDTO(ti)
			enrichTaskInstance(c, &dto, runs, versions)
			return dto, true
		}
	}
	return taskInstanceDTO{}, false
}

// resolveRunContextFor is resolveRunContext for an explicit run id (clear posts
// the run in its body rather than the path).
func resolveRunContextFor(c *gin.Context, runs DagRunRepository, versions DagVersionLister, runID string) (*time.Time, *dagVersionDTO) {
	dagID := c.Param("dag_id")
	var logical *time.Time
	if runs != nil && runID != "" {
		if run, err := runs.GetDagRun(c.Request.Context(), tenantOf(c), dagID, runID); err == nil {
			logical = &run.LogicalDate
		}
	}
	var version *dagVersionDTO
	if versions != nil {
		if vs, err := versions.ListDagVersions(c.Request.Context(), tenantOf(c), dagID); err == nil && len(vs) > 0 {
			version = &dagVersionDTO{
				ID: vs[0].ID, VersionNumber: vs[0].VersionNumber, DagID: dagID,
				BundleName: "leoflow", CreatedAt: vs[0].CreatedAt, DagDisplayName: dagID,
			}
		}
	}
	return logical, version
}

// taskInstanceActionHandler dispatches the catch-all under
// /taskInstances/{task_id}/* : "logs/{try}" streams the attempt's logs, while a
// bare "{map_index}" returns the single task instance (TaskInstanceResponse).
func taskInstanceActionHandler(tasks TaskInstanceRepository, logs LogReader, xcoms XComReader, runs DagRunRepository, versions DagVersionLister) gin.HandlerFunc {
	return func(c *gin.Context) {
		action := strings.Trim(c.Param("action"), "/")
		if rest, ok := strings.CutPrefix(action, "logs/"); ok {
			try, err := strconv.Atoi(rest)
			if err != nil {
				AbortProblem(c, http.StatusBadRequest, "bad request", "try_number must be an integer")
				return
			}
			serveLogs(c, logs, try)
			return
		}
		if xcoms != nil {
			if action == "xcomEntries" {
				serveXComEntries(c, xcoms)
				return
			}
			if key, ok := strings.CutPrefix(action, "xcomEntries/"); ok {
				serveXComValue(c, xcoms, key)
				return
			}
		}
		if action == "links" {
			// The task Details view reads g.extra_links and calls Object.keys on it,
			// so the response must carry an extra_links object (a bare {} or a 400
			// crashes the view). We expose no operator links, so it is empty.
			c.JSON(http.StatusOK, gin.H{"extra_links": gin.H{}})
			return
		}
		if action == "tries" || strings.HasPrefix(action, "tries/") {
			serveTaskTries(c, tasks, runs, versions, strings.TrimPrefix(action, "tries"))
			return
		}
		mapIndex, err := strconv.Atoi(action)
		if err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", "map_index must be an integer")
			return
		}
		tis, _, err := tasks.ListTaskInstances(c.Request.Context(), tenantOf(c),
			c.Param("dag_id"), c.Param("dag_run_id"), 1000, 0)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		for _, ti := range tis {
			if ti.TaskID == c.Param("task_id") && ti.MapIndex == mapIndex {
				dto := toTaskInstanceDTO(ti)
				enrichTaskInstance(c, &dto, runs, versions)
				c.JSON(http.StatusOK, dto)
				return
			}
		}
		AbortProblem(c, http.StatusNotFound, "not found", "task instance not found")
	}
}

// enrichTaskInstance fills the run-derived fields (logical_date, run_after) and
// the DAG version object, so the task panel shows them rather than null.
func enrichTaskInstance(c *gin.Context, dto *taskInstanceDTO, runs DagRunRepository, versions DagVersionLister) {
	logical, version := resolveRunContext(c, runs, versions)
	dto.LogicalDate, dto.RunAfter, dto.DagVersion = logical, logical, version
}

// resolveRunContext looks up a run's logical date and the DAG's latest version
// once, for sharing across the task instances of a single run. Either result is
// nil when the source repository is absent or the lookup fails.
func resolveRunContext(c *gin.Context, runs DagRunRepository, versions DagVersionLister) (*time.Time, *dagVersionDTO) {
	dagID, runID := c.Param("dag_id"), c.Param("dag_run_id")
	var logical *time.Time
	if runs != nil {
		if run, err := runs.GetDagRun(c.Request.Context(), tenantOf(c), dagID, runID); err == nil {
			logical = &run.LogicalDate
		}
	}
	var version *dagVersionDTO
	if versions != nil {
		if vs, err := versions.ListDagVersions(c.Request.Context(), tenantOf(c), dagID); err == nil && len(vs) > 0 {
			version = &dagVersionDTO{
				ID: vs[0].ID, VersionNumber: vs[0].VersionNumber, DagID: dagID,
				BundleName: "leoflow", CreatedAt: vs[0].CreatedAt, DagDisplayName: dagID,
			}
		}
	}
	return logical, version
}

// serveTaskTries answers the task try-history endpoints the UI's task Details
// tab reads: "tries" returns the collection of attempts for the task, and
// "tries/{n}" (suffix "/{n}") returns the single attempt. Leoflow keeps one row
// per task (try_number advances in place), so the collection holds the current
// attempt and "/{n}" returns it regardless of n.
func serveTaskTries(c *gin.Context, tasks TaskInstanceRepository, runs DagRunRepository, versions DagVersionLister, suffix string) {
	tis, _, err := tasks.ListTaskInstances(c.Request.Context(), tenantOf(c),
		c.Param("dag_id"), c.Param("dag_run_id"), 1000, 0)
	if err != nil {
		handleRepoError(c, err)
		return
	}
	matches := make([]taskInstanceDTO, 0, 1)
	for _, ti := range tis {
		if ti.TaskID == c.Param("task_id") {
			dto := toTaskInstanceDTO(ti)
			enrichTaskInstance(c, &dto, runs, versions)
			matches = append(matches, dto)
		}
	}
	if single := strings.TrimPrefix(suffix, "/"); single != "" {
		if len(matches) == 0 {
			AbortProblem(c, http.StatusNotFound, "not found", "task instance not found")
			return
		}
		c.JSON(http.StatusOK, matches[len(matches)-1])
		return
	}
	c.JSON(http.StatusOK, taskInstanceCollectionDTO{TaskInstances: matches, TotalEntries: len(matches)})
}

// stubHandler reports a feature that arrives in a later phase.
func stubHandler(feature string) gin.HandlerFunc {
	return func(c *gin.Context) {
		AbortProblem(c, http.StatusNotImplemented, "not implemented", feature+" arrives in Phase 4")
	}
}

// registerResources mounts the /api/v2 resource routes whose repositories are
// configured. Routes for nil repositories are omitted.
func registerResources(r gin.IRouter, deps Dependencies) {
	if deps.Dags != nil {
		g := r.Group("/api/v2/dags")
		g.GET("", RequirePermission("read", "dag"), listDagsHandler(deps.Dags))
		g.GET("/:dag_id", RequirePermission("read", "dag"), getDagHandler(deps.Dags))
		g.GET("/:dag_id/details", RequirePermission("read", "dag"), dagDetailsHandler(deps.Dags, deps.DagVersions))
		g.PATCH("/:dag_id", RequirePermission("write", "dag"), patchDagHandler(deps.Dags))
		g.DELETE("/:dag_id", RequirePermission("write", "dag"), deleteDagHandler(deps.Dags))
	}
	if deps.DagRuns != nil {
		g := r.Group("/api/v2/dags/:dag_id/dagRuns")
		g.GET("", RequirePermission("read", "dag_run"), listDagRunsHandler(deps.DagRuns))
		g.POST("", RequirePermission("execute", "dag"), createDagRunHandler(deps.DagRuns))
		g.GET("/:dag_run_id", RequirePermission("read", "dag_run"), getDagRunHandler(deps.DagRuns))
	}
	if deps.Tasks != nil {
		r.GET("/api/v2/dags/:dag_id/dagRuns/:dag_run_id/taskInstances",
			RequirePermission("read", "task_instance"), listTaskInstancesHandler(deps.Tasks, deps.DagRuns, deps.DagVersions))
		// The "logs/:try_number" and ":map_index" routes share the :task_id
		// parent; gin cannot mix a static and a wildcard child there, so one
		// catch-all dispatches both (single task instance vs its logs).
		r.GET("/api/v2/dags/:dag_id/dagRuns/:dag_run_id/taskInstances/:task_id/*action",
			RequirePermission("read", "task_instance"), taskInstanceActionHandler(deps.Tasks, deps.Logs, deps.Xcoms, deps.DagRuns, deps.DagVersions))
		// Mark-success/failed: PATCH the task instance. The UI hits both the bare
		// path and one carrying optional /{map_index} and /dry_run segments.
		patchTI := patchTaskInstanceHandler(deps.Tasks, deps.DagRuns, deps.DagVersions)
		r.PATCH("/api/v2/dags/:dag_id/dagRuns/:dag_run_id/taskInstances/:task_id", RequirePermission("write", "task_instance"), patchTI)
		r.PATCH("/api/v2/dags/:dag_id/dagRuns/:dag_run_id/taskInstances/:task_id/*action", RequirePermission("write", "task_instance"), patchTI)
		r.POST("/api/v2/dags/:dag_id/clearTaskInstances",
			RequirePermission("write", "task_instance"), clearTaskInstancesHandler(deps.Tasks, deps.DagRuns, deps.DagVersions))
	}
	if deps.Versions != nil {
		r.POST("/api/v2/dags/:dag_id/versions", RequirePermission("write", "dag"), registerVersionHandler(deps.Versions, deps.InlineHTTPMaxDurationSeconds))
	}
	if deps.Xcoms != nil {
		r.GET("/api/v2/xcoms/:dag_id/:dag_run_id/:task_id/:key", RequirePermission("read", "xcom"), xcomHandler(deps.Xcoms))
	} else {
		r.GET("/api/v2/xcoms/:dag_id/:dag_run_id/:task_id/:key", RequirePermission("read", "xcom"), stubHandler("XCom retrieval"))
	}
}
