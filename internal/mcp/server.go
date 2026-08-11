// Package mcp is the Leoflow Model Context Protocol server (ADR 0050). It exposes
// the control plane to an LLM agent as read tools over the official
// modelcontextprotocol/go-sdk, talking to /api/v2 only through pkg/client — it
// imports no other internal/ package and holds no privilege of its own (the
// caller's token is passed through by the client).
package mcp

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	apiclient "github.com/neochaotic/leoflow/pkg/client"
)

const (
	serverName    = "leoflow"
	defaultDagLim = 25
	maxDagLim     = 200
)

// handlers holds the dependencies the tool handlers share — currently just the
// typed /api/v2 client.
type handlers struct {
	api *apiclient.ClientWithResponses
}

// NewServer builds a read-only Leoflow MCP server over the given control-plane
// client. It registers the read tools and returns the server; the caller runs it
// over a transport (stdio for Lite dev, Streamable HTTP for Pro). Tools that
// mutate state are deliberately absent from this skeleton (ADR 0050 D7).
func NewServer(api *apiclient.ClientWithResponses, version string) *mcpsdk.Server {
	s := mcpsdk.NewServer(&mcpsdk.Implementation{Name: serverName, Version: version}, nil)
	h := &handlers{api: api}
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "list_dags",
		Description: "List DAGs registered in the Leoflow control plane, with their paused state.",
	}, h.listDags)
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "diagnose_run",
		Description: "Diagnose a DAG run: its state, which task instances failed, and a truncated tail of each failed task's log — one call instead of list-runs/get-run/list-tasks/get-logs.",
	}, h.diagnoseRun)
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "search_logs",
		Description: "Search one task attempt's log for a case-insensitive substring, returning the matching lines (with line numbers) instead of the whole log.",
	}, h.searchLogs)
	return s
}

type listDagsInput struct {
	Limit int    `json:"limit,omitempty" jsonschema:"maximum number of DAGs to return (default 25, max 200)"`
	Tag   string `json:"tag,omitempty" jsonschema:"return only DAGs carrying this tag"`
}

type dagSummary struct {
	DagID    string `json:"dag_id"`
	IsPaused bool   `json:"is_paused"`
}

type listDagsOutput struct {
	Dags         []dagSummary `json:"dags"`
	TotalEntries int          `json:"total_entries"`
}

// listDags returns a compact list of DAGs. It shapes the response rather than
// forwarding the verbose upstream payload (ADR 0050 R18), and surfaces a
// non-200 as an error so the agent never mistakes a failed call for "no DAGs".
func (h *handlers) listDags(ctx context.Context, _ *mcpsdk.CallToolRequest, in listDagsInput) (*mcpsdk.CallToolResult, listDagsOutput, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = defaultDagLim
	}
	if limit > maxDagLim {
		limit = maxDagLim
	}
	lim := limit
	params := &apiclient.ListDagsParams{Limit: &lim}
	if in.Tag != "" {
		params.Tags = &[]string{in.Tag}
	}

	resp, err := h.api.ListDagsWithResponse(ctx, params)
	if err != nil {
		return nil, listDagsOutput{}, fmt.Errorf("listing dags: %w", err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return nil, listDagsOutput{}, fmt.Errorf("control plane returned %d listing dags", resp.StatusCode())
	}

	out := listDagsOutput{}
	if resp.JSON200.TotalEntries != nil {
		out.TotalEntries = *resp.JSON200.TotalEntries
	}
	if resp.JSON200.Dags != nil {
		out.Dags = make([]dagSummary, 0, len(*resp.JSON200.Dags))
		for _, d := range *resp.JSON200.Dags {
			out.Dags = append(out.Dags, dagSummary{
				DagID:    deref(d.DagId),
				IsPaused: deref(d.IsPaused),
			})
		}
	}
	return nil, out, nil
}

// deref returns the pointed-to value or the zero value when nil — the generated
// client models every optional field as a pointer.
func deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

const (
	defaultLogTail = 40
	maxLogTail     = 200
)

type diagnoseRunInput struct {
	DagID        string `json:"dag_id" jsonschema:"the DAG id"`
	RunID        string `json:"run_id" jsonschema:"the DAG run id to diagnose"`
	LogTailLines int    `json:"log_tail_lines,omitempty" jsonschema:"log lines to include per failed task (default 40, max 200)"`
}

type failedTask struct {
	TaskID          string  `json:"task_id"`
	State           string  `json:"state"`
	TryNumber       int     `json:"try_number"`
	DurationSeconds float32 `json:"duration_seconds,omitempty"`
	LogTail         string  `json:"log_tail,omitempty"`
}

type diagnoseRunOutput struct {
	DagID       string       `json:"dag_id"`
	RunID       string       `json:"run_id"`
	RunState    string       `json:"run_state"`
	TotalTasks  int          `json:"total_tasks"`
	FailedTasks []failedTask `json:"failed_tasks"`
	Summary     string       `json:"summary"`
}

// diagnoseRun composes the run, its task instances, and each failed task's log
// tail into a single diagnosis (ADR 0050 R19), so an agent gets one answer
// instead of chaining four calls it would likely mis-sequence. It is
// deliberately client-side composition for the MVP; a server-side aggregate
// endpoint is the later optimization for chattiness. Log tails are truncated and
// sanitized because log content is untrusted (D10).
func (h *handlers) diagnoseRun(ctx context.Context, _ *mcpsdk.CallToolRequest, in diagnoseRunInput) (*mcpsdk.CallToolResult, diagnoseRunOutput, error) {
	if in.DagID == "" || in.RunID == "" {
		return nil, diagnoseRunOutput{}, fmt.Errorf("dag_id and run_id are required")
	}
	tail := in.LogTailLines
	if tail <= 0 {
		tail = defaultLogTail
	}
	if tail > maxLogTail {
		tail = maxLogTail
	}
	runResp, err := h.api.GetDagRunWithResponse(ctx, in.DagID, in.RunID)
	if err != nil {
		return nil, diagnoseRunOutput{}, fmt.Errorf("fetching run: %w", err)
	}
	if runResp.StatusCode() != http.StatusOK || runResp.JSON200 == nil {
		return nil, diagnoseRunOutput{}, fmt.Errorf("control plane returned %d for run %s/%s", runResp.StatusCode(), in.DagID, in.RunID)
	}

	tiResp, err := h.api.ListTaskInstancesWithResponse(ctx, in.DagID, in.RunID)
	if err != nil {
		return nil, diagnoseRunOutput{}, fmt.Errorf("listing task instances: %w", err)
	}
	if tiResp.StatusCode() != http.StatusOK || tiResp.JSON200 == nil {
		return nil, diagnoseRunOutput{}, fmt.Errorf("control plane returned %d listing task instances", tiResp.StatusCode())
	}

	out := diagnoseRunOutput{DagID: in.DagID, RunID: in.RunID}
	if runResp.JSON200.State != nil {
		out.RunState = string(*runResp.JSON200.State)
	}
	var tis []apiclient.TaskInstance
	if tiResp.JSON200.TaskInstances != nil {
		tis = *tiResp.JSON200.TaskInstances
	}
	out.TotalTasks = len(tis)
	out.FailedTasks = h.collectFailedTasks(ctx, in.DagID, in.RunID, tis, tail)
	out.Summary = fmt.Sprintf("%d of %d task(s) failed", len(out.FailedTasks), out.TotalTasks)
	return nil, out, nil
}

// collectFailedTasks returns the failed/upstream-failed task instances, each with
// a truncated+sanitized log tail (only for tasks that actually ran — an
// upstream_failed task never executed, so it has no log of its own).
func (h *handlers) collectFailedTasks(ctx context.Context, dagID, runID string, tis []apiclient.TaskInstance, tail int) []failedTask {
	var failed []failedTask
	for _, ti := range tis {
		if ti.State == nil {
			continue
		}
		switch *ti.State {
		case apiclient.TaskInstanceStateFailed, apiclient.TaskInstanceStateUpstreamFailed:
		default:
			continue
		}
		ft := failedTask{
			TaskID:          deref(ti.TaskId),
			State:           string(*ti.State),
			TryNumber:       deref(ti.TryNumber),
			DurationSeconds: deref(ti.Duration),
		}
		if *ti.State == apiclient.TaskInstanceStateFailed && ft.TaskID != "" && ft.TryNumber >= 1 {
			logResp, lerr := h.api.GetTaskLogsWithResponse(ctx, dagID, runID, ft.TaskID, ft.TryNumber)
			if lerr == nil && logResp.StatusCode() == http.StatusOK {
				ft.LogTail = sanitizeLogTail(string(logResp.Body), tail)
			}
		}
		failed = append(failed, ft)
	}
	return failed
}

const (
	defaultLogMatches = 20
	maxLogMatches     = 100
)

type searchLogsInput struct {
	DagID      string `json:"dag_id" jsonschema:"the DAG id"`
	RunID      string `json:"run_id" jsonschema:"the DAG run id"`
	TaskID     string `json:"task_id" jsonschema:"the task id whose log to search"`
	TryNumber  int    `json:"try_number,omitempty" jsonschema:"attempt to search (default 1)"`
	Query      string `json:"query" jsonschema:"case-insensitive substring to match"`
	MaxMatches int    `json:"max_matches,omitempty" jsonschema:"maximum matching lines to return (default 20, max 100)"`
}

type logMatch struct {
	LineNumber int    `json:"line_number"`
	Line       string `json:"line"`
}

type searchLogsOutput struct {
	DagID        string     `json:"dag_id"`
	RunID        string     `json:"run_id"`
	TaskID       string     `json:"task_id"`
	TryNumber    int        `json:"try_number"`
	Query        string     `json:"query"`
	Matches      []logMatch `json:"matches"`
	TotalMatches int        `json:"total_matches"`
	Truncated    bool       `json:"truncated"`
}

// searchLogs fetches one task attempt's log and returns the lines matching a
// case-insensitive substring, with 1-based line numbers — so an agent can find
// the relevant lines of a long log without pulling the whole thing into context
// (ADR 0050 R16). The match is a plain substring, not a regex, to avoid handing
// the model a ReDoS lever. Matched lines are sanitized (untrusted content, D10)
// and the returned set is capped, but the true total is always reported.
func (h *handlers) searchLogs(ctx context.Context, _ *mcpsdk.CallToolRequest, in searchLogsInput) (*mcpsdk.CallToolResult, searchLogsOutput, error) {
	if in.DagID == "" || in.RunID == "" || in.TaskID == "" || in.Query == "" {
		return nil, searchLogsOutput{}, fmt.Errorf("dag_id, run_id, task_id, and query are required")
	}
	try := in.TryNumber
	if try < 1 {
		try = 1
	}
	maxN := in.MaxMatches
	if maxN <= 0 {
		maxN = defaultLogMatches
	}
	if maxN > maxLogMatches {
		maxN = maxLogMatches
	}

	resp, err := h.api.GetTaskLogsWithResponse(ctx, in.DagID, in.RunID, in.TaskID, try)
	if err != nil {
		return nil, searchLogsOutput{}, fmt.Errorf("fetching task log: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, searchLogsOutput{}, fmt.Errorf("control plane returned %d fetching task log", resp.StatusCode())
	}

	out := searchLogsOutput{DagID: in.DagID, RunID: in.RunID, TaskID: in.TaskID, TryNumber: try, Query: in.Query}
	needle := strings.ToLower(in.Query)
	lines := strings.Split(strings.ReplaceAll(string(resp.Body), "\r\n", "\n"), "\n")
	for i, ln := range lines {
		if !strings.Contains(strings.ToLower(ln), needle) {
			continue
		}
		out.TotalMatches++
		if len(out.Matches) < maxN {
			out.Matches = append(out.Matches, logMatch{LineNumber: i + 1, Line: stripControl(ln)})
		}
	}
	out.Truncated = out.TotalMatches > len(out.Matches)
	return nil, out, nil
}

// sanitizeLogTail returns at most the last n lines of raw log output, stripped of
// control and ANSI bytes (keeping \n and \t). Log content is untrusted (D10): an
// ANSI/control payload in a task's stderr must not reach the agent's context
// intact, and an unbounded tail would blow the context window (R16).
func sanitizeLogTail(raw string, n int) string {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	// Drop a trailing empty line from a final newline so "n lines" is intuitive.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	var b strings.Builder
	for i, ln := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(stripControl(ln))
	}
	return b.String()
}

// stripControl removes ASCII control bytes (< 0x20) except tab; this also breaks
// ANSI escape sequences by removing their leading ESC (0x1b).
func stripControl(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}
