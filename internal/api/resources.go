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

// DagRepository reads and updates registered DAGs.
type DagRepository interface {
	ListDags(ctx context.Context, tenant string, limit, offset int) ([]domain.DAG, int, error)
	GetDag(ctx context.Context, tenant, dagID string) (domain.DAG, error)
	SetPaused(ctx context.Context, tenant, dagID string, paused bool) (domain.DAG, error)
}

// DagRunRepository reads and creates DAG runs.
type DagRunRepository interface {
	ListDagRuns(ctx context.Context, tenant, dagID string, limit, offset int) ([]domain.DagRun, int, error)
	GetDagRun(ctx context.Context, tenant, dagID, runID string) (domain.DagRun, error)
	CreateDagRun(ctx context.Context, tenant, dagID string, run domain.DagRun) (domain.DagRun, error)
}

// TaskInstanceRepository reads task instances and clears them for re-run.
type TaskInstanceRepository interface {
	ListTaskInstances(ctx context.Context, tenant, dagID, runID string, limit, offset int) ([]domain.TaskInstance, int, error)
	ClearTaskInstances(ctx context.Context, tenant, dagID, runID string, taskIDs []string, resetDagRun bool) (int, error)
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

func listDagRunsHandler(repo DagRunRepository) gin.HandlerFunc {
	return func(c *gin.Context) {
		limit, offset := pagination(c)
		runs, total, err := repo.ListDagRuns(c.Request.Context(), tenantOf(c), c.Param("dag_id"), limit, offset)
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

func listTaskInstancesHandler(repo TaskInstanceRepository) gin.HandlerFunc {
	return func(c *gin.Context) {
		limit, offset := pagination(c)
		tis, total, err := repo.ListTaskInstances(c.Request.Context(), tenantOf(c), c.Param("dag_id"), c.Param("dag_run_id"), limit, offset)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		out := taskInstanceCollectionDTO{TaskInstances: make([]taskInstanceDTO, 0, len(tis)), TotalEntries: total}
		for _, ti := range tis {
			out.TaskInstances = append(out.TaskInstances, toTaskInstanceDTO(ti))
		}
		setPaginationLinks(c, total, limit, offset)
		c.JSON(http.StatusOK, out)
	}
}

func clearTaskInstancesHandler(repo TaskInstanceRepository) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body struct {
			TaskIDs      []string `json:"task_ids"`
			DagRunID     string   `json:"dag_run_id"`
			ResetDagRuns *bool    `json:"reset_dag_runs"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", err.Error())
			return
		}
		reset := true
		if body.ResetDagRuns != nil {
			reset = *body.ResetDagRuns
		}
		n, err := repo.ClearTaskInstances(c.Request.Context(), tenantOf(c), c.Param("dag_id"), body.DagRunID, body.TaskIDs, reset)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"cleared": n})
	}
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
		g.PATCH("/:dag_id", RequirePermission("write", "dag"), patchDagHandler(deps.Dags))
	}
	if deps.DagRuns != nil {
		g := r.Group("/api/v2/dags/:dag_id/dagRuns")
		g.GET("", RequirePermission("read", "dag_run"), listDagRunsHandler(deps.DagRuns))
		g.POST("", RequirePermission("execute", "dag"), createDagRunHandler(deps.DagRuns))
		g.GET("/:dag_run_id", RequirePermission("read", "dag_run"), getDagRunHandler(deps.DagRuns))
	}
	if deps.Tasks != nil {
		r.GET("/api/v2/dags/:dag_id/dagRuns/:dag_run_id/taskInstances",
			RequirePermission("read", "task_instance"), listTaskInstancesHandler(deps.Tasks))
		if deps.Logs != nil {
			r.GET("/api/v2/dags/:dag_id/dagRuns/:dag_run_id/taskInstances/:task_id/logs/:try_number",
				RequirePermission("read", "task_instance"), logsHandler(deps.Logs))
		}
		r.POST("/api/v2/dags/:dag_id/clearTaskInstances",
			RequirePermission("write", "task_instance"), clearTaskInstancesHandler(deps.Tasks))
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
