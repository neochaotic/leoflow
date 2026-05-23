package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
)

// LogReader streams a task attempt's stored logs and, for running tasks, tails
// new lines live.
type LogReader interface {
	ReadLogs(ctx context.Context, tenant, dagID, runID, taskID string, tryNumber int) (io.ReadCloser, error)
	Tail(ctx context.Context, tenant, dagID, runID, taskID string, tryNumber int) (<-chan string, func(), error)
}

// serveLogs streams the stored logs for a task attempt and, when follow=true,
// tails live lines. The caller has already parsed the try number (the route is a
// catch-all shared with the single task-instance endpoint).
func serveLogs(c *gin.Context, reader LogReader, try int) {
	if reader == nil {
		AbortProblem(c, http.StatusNotFound, "not found", "logs are not available")
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
	if wantsStructuredLogs(c) {
		serveStructuredLogs(c, rc, try)
		return
	}
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.Status(http.StatusOK)
	if _, cerr := io.Copy(c.Writer, rc); cerr != nil {
		slog.Warn("streaming logs to client", "error", cerr)
	}
	if c.Query("follow") == "true" {
		tailLogs(c, reader, try)
	}
}

// tailLogs streams live log lines to the client until the task stops producing
// them or the client disconnects. It is best-effort: if tailing is unavailable
// the already-sent stored logs stand on their own.
func tailLogs(c *gin.Context, reader LogReader, try int) {
	ctx := c.Request.Context()
	lines, cancel, err := reader.Tail(ctx, tenantOf(c),
		c.Param("dag_id"), c.Param("dag_run_id"), c.Param("task_id"), try)
	if err != nil {
		return
	}
	defer cancel()
	flusher, canFlush := c.Writer.(http.Flusher)
	for {
		select {
		case <-ctx.Done():
			return
		case line, open := <-lines:
			if !open {
				return
			}
			if _, werr := c.Writer.WriteString(line + "\n"); werr != nil {
				return
			}
			if canFlush {
				flusher.Flush()
			}
		}
	}
}
