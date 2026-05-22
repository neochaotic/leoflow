package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/neochaotic/leoflow/internal/xcom"
)

// XComReader reads a stored XCom value for the read API.
type XComReader interface {
	GetXCom(ctx context.Context, tenant, dagID, runID, taskID, key string) (xcom.Entry, error)
}

// xComEntryDTO is the Airflow-compatible XComEntry response shape.
type xComEntryDTO struct {
	Key       string          `json:"key"`
	Value     json.RawMessage `json:"value"`
	DagID     string          `json:"dag_id"`
	DagRunID  string          `json:"dag_run_id"`
	TaskID    string          `json:"task_id"`
	Timestamp time.Time       `json:"timestamp"`
	MapIndex  int             `json:"map_index"`
}

func xcomHandler(reader XComReader) gin.HandlerFunc {
	return func(c *gin.Context) {
		entry, err := reader.GetXCom(c.Request.Context(), tenantOf(c),
			c.Param("dag_id"), c.Param("dag_run_id"), c.Param("task_id"), c.Param("key"))
		if err != nil {
			handleRepoError(c, err)
			return
		}
		c.JSON(http.StatusOK, xComEntryDTO{
			Key:       c.Param("key"),
			Value:     json.RawMessage(entry.Value),
			DagID:     c.Param("dag_id"),
			DagRunID:  c.Param("dag_run_id"),
			TaskID:    c.Param("task_id"),
			Timestamp: entry.CreatedAt,
			MapIndex:  -1,
		})
	}
}
