// Package alerts sends native on-failure notifications (#424). The scheduler
// resolves each leoflow.yaml alert rule's connection to an endpoint URL and calls
// a Notifier — Slack incoming webhooks and generic HTTP webhooks — entirely in
// Go, with no task pod and no Python in the hot path. Connection lookup lives in
// the caller; this package only renders the message and performs the HTTP POST,
// so it stays trivially testable and free of storage dependencies.
package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Event carries the DagRun context exposed to an alert message template and to
// the structured webhook payload.
type Event struct {
	// DagID is the failed run's DAG id.
	DagID string
	// RunID is the failed DagRun id.
	RunID string
	// LogicalDate is the run's logical date (may be empty for unscheduled runs).
	LogicalDate string
	// FailedTasks lists the task_ids that reached the failed state.
	FailedTasks []string
}

// Render substitutes the run context into a message template, replacing
// {{dag}}, {{run_id}}, {{logical_date}}, {{task}} (the first failed task) and
// {{tasks}} (all failed tasks, comma-separated). An empty template yields a
// default one-line summary so an operator never receives a blank alert.
func Render(tmpl string, ev Event) string {
	if strings.TrimSpace(tmpl) == "" {
		tmpl = "DAG {{dag}} run {{run_id}} failed"
		if len(ev.FailedTasks) > 0 {
			tmpl += ": {{tasks}}"
		}
	}
	firstTask := ""
	if len(ev.FailedTasks) > 0 {
		firstTask = ev.FailedTasks[0]
	}
	r := strings.NewReplacer(
		"{{dag}}", ev.DagID,
		"{{run_id}}", ev.RunID,
		"{{logical_date}}", ev.LogicalDate,
		"{{task}}", firstTask,
		"{{tasks}}", strings.Join(ev.FailedTasks, ", "),
	)
	return r.Replace(tmpl)
}

// Notifier posts resolved alerts over HTTP. It is safe for concurrent use.
type Notifier struct {
	client *http.Client
}

// NewNotifier returns a Notifier that sends with the given HTTP client. The
// client SHOULD carry a timeout so a slow endpoint cannot wedge the caller.
func NewNotifier(client *http.Client) *Notifier {
	return &Notifier{client: client}
}

// Send posts message to url for the given channel type: "slack" sends Slack's
// {"text": …} incoming-webhook shape; "webhook" sends a structured JSON payload
// carrying the run context. Any headers are applied to the request (e.g. an
// Authorization header for an endpoint whose token is not in the URL). A non-2xx
// response or an unknown channel type is an error; the caller decides whether
// that is fatal (it should not be — an alert failure must not fail the run).
func (n *Notifier) Send(ctx context.Context, channelType, url string, headers map[string]string, message string, ev Event) error {
	var payload any
	switch channelType {
	case "slack":
		payload = map[string]string{"text": message}
	case "webhook":
		payload = map[string]any{
			"dag_id":       ev.DagID,
			"run_id":       ev.RunID,
			"logical_date": ev.LogicalDate,
			"failed_tasks": ev.FailedTasks,
			"message":      message,
		}
	default:
		return fmt.Errorf("unknown alert channel type %q", channelType)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encoding %s alert payload: %w", channelType, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building %s alert request: %w", channelType, err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("sending %s alert: %w", channelType, err)
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // best-effort close of the alert response body
	//nolint:errcheck // draining the (capped) body for connection reuse; count/err are irrelevant
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s alert endpoint returned %s", channelType, resp.Status)
	}
	return nil
}
