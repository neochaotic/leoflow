package api

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/neochaotic/leoflow/internal/logs"
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
	if errors.Is(err, ErrNotFound) {
		// No stored logs for this attempt (e.g. it never ran, or the logs aged
		// out / predate a backend change). Serve a graceful "no logs" rather than
		// a 404 the UI renders as a broken page.
		serveNoLogs(c)
		return
	}
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
	// Logs are stored as JSONL; the plain-text view emits just each message
	// (DecodeLine tolerates legacy plain lines too).
	scanner := bufio.NewScanner(rc)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		if _, werr := c.Writer.WriteString(logs.DecodeLine(scanner.Text()).Message + "\n"); werr != nil {
			slog.Warn("streaming logs to client", "error", werr)
			break
		}
	}
	if c.Query("follow") == "true" {
		tailLogs(c, reader, try)
	}
}

// serveNoLogs renders an empty-but-valid log response (200) when no logs exist
// for an attempt, so the UI shows "no logs" instead of erroring.
func serveNoLogs(c *gin.Context) {
	const msg = "No logs available for this attempt."
	if wantsStructuredLogs(c) {
		c.JSON(http.StatusOK, structuredLogResponse{
			Content:           []structuredLogEvent{{Event: msg, Level: "info"}},
			ContinuationToken: nil,
		})
		return
	}
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.String(http.StatusOK, msg+"\n")
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
