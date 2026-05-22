package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// LogReader streams a task attempt's logs for the read API.
type LogReader interface {
	ReadLogs(ctx context.Context, tenant, dagID, runID, taskID string, tryNumber int) (io.ReadCloser, error)
}

func logsHandler(reader LogReader) gin.HandlerFunc {
	return func(c *gin.Context) {
		try, err := strconv.Atoi(c.Param("try_number"))
		if err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", "try_number must be an integer")
			return
		}
		rc, err := reader.ReadLogs(c.Request.Context(), tenantOf(c),
			c.Param("dag_id"), c.Param("dag_run_id"), c.Param("task_id"), try)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		defer func() {
			if cerr := rc.Close(); cerr != nil {
				slog.Warn("closing log stream", "error", cerr)
			}
		}()
		c.Header("Content-Type", "text/plain; charset=utf-8")
		c.Status(http.StatusOK)
		if _, cerr := io.Copy(c.Writer, rc); cerr != nil {
			slog.Warn("streaming logs to client", "error", cerr)
		}
	}
}
