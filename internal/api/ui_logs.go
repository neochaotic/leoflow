package api

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/neochaotic/leoflow/internal/logs"
)

// structuredLogEvent is one item in Airflow 3.2.1's structured log content: a
// fold marker (::group::/::endgroup:: with sources) or a log line with a level.
type structuredLogEvent struct {
	Event   string   `json:"event"`
	Level   string   `json:"level,omitempty"`
	Sources []string `json:"sources,omitempty"`
}

// structuredLogResponse is the Accept: application/json log payload the UI's
// drill-down viewer renders (collapsible groups + per-level coloring).
type structuredLogResponse struct {
	Content           []structuredLogEvent `json:"content"`
	ContinuationToken *string              `json:"continuation_token"`
}

// wantsStructuredLogs reports whether the client asked for JSON logs (the UI's
// drill-down viewer) rather than the plain-text stream.
func wantsStructuredLogs(c *gin.Context) bool {
	return strings.Contains(c.GetHeader("Accept"), "application/json")
}

// serveStructuredLogs reads the stored log lines and emits Airflow's structured
// content: a leading collapsible source group, then one event per line with an
// inferred level. continuation_token is null (logs are served whole, not paged).
func serveStructuredLogs(c *gin.Context, rc io.Reader, try int) {
	source := fmt.Sprintf("dag_id=%s/run_id=%s/task_id=%s/attempt=%d.log",
		c.Param("dag_id"), c.Param("dag_run_id"), c.Param("task_id"), try)
	content := []structuredLogEvent{
		{Event: "::group::Log message source details", Sources: []string{source}},
		{Event: "::endgroup::"},
	}
	scanner := bufio.NewScanner(rc)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		// Logs are stored as JSONL with the real level the producer tagged;
		// DecodeLine infers a level for legacy plain lines.
		ev := logs.DecodeLine(scanner.Text())
		content = append(content, structuredLogEvent{Event: ev.Message, Level: ev.Level})
	}
	if err := scanner.Err(); err != nil {
		slog.Warn("reading logs for structured response", "error", err)
	}
	c.JSON(http.StatusOK, structuredLogResponse{Content: content, ContinuationToken: nil})
}
