package api

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/neochaotic/leoflow/internal/domain"
)

// DagVersionRepository registers compiled DAG versions.
type DagVersionRepository interface {
	// RegisterDagVersion upserts the DAG and inserts a version keyed by
	// specHash, reporting whether a new version was created (false if the hash
	// already existed — the push is idempotent).
	RegisterDagVersion(ctx context.Context, tenant string, spec domain.DAGSpec, specHash string) (bool, error)
}

type versionResponse struct {
	DagID    string `json:"dag_id"`
	Version  string `json:"version"`
	SpecHash string `json:"spec_hash"`
	Created  bool   `json:"created"`
}

func registerVersionHandler(repo DagVersionRepository, inlineMaxSeconds int) gin.HandlerFunc {
	if inlineMaxSeconds <= 0 {
		inlineMaxSeconds = domain.DefaultInlineMaxDurationSeconds
	}
	return func(c *gin.Context) {
		var spec domain.DAGSpec
		if err := c.ShouldBindJSON(&spec); err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", err.Error())
			return
		}
		if c.Param("dag_id") != spec.DagID {
			AbortProblem(c, http.StatusBadRequest, "bad request", "dag_id in path does not match spec")
			return
		}
		if err := spec.Validate(); err != nil {
			AbortProblem(c, http.StatusBadRequest, "invalid dag spec", err.Error())
			return
		}
		if err := spec.ValidateInlineExecution(inlineMaxSeconds); err != nil {
			AbortProblem(c, http.StatusBadRequest, "invalid dag spec", err.Error())
			return
		}
		hash, err := spec.CanonicalHash()
		if err != nil {
			AbortProblem(c, http.StatusInternalServerError, "internal error", err.Error())
			return
		}
		created, err := repo.RegisterDagVersion(c.Request.Context(), tenantOf(c), spec, hash)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		c.JSON(status, versionResponse{DagID: spec.DagID, Version: spec.DagVersion, SpecHash: hash, Created: created})
	}
}
