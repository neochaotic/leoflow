package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	apiclient "github.com/neochaotic/leoflow/pkg/client"
)

// resourceLogTail bounds a log resource read (R16); a task log an agent fetches
// wholesale still must not blow the context window.
const resourceLogTail = 200

// registerResources wires the read-only addressable resources (ADR 0050 D7/R12).
// The agent chooses the URI; the control plane authorizes each read via the
// pass-through token, so a resource can only surface what the caller already may
// see. Templates carry their parameters IN the URI (R05).
func (h *handlers) registerResources(s *mcpsdk.Server) {
	s.AddResource(&mcpsdk.Resource{
		Name: "dags", URI: "dag://list", MIMEType: "application/json",
		Description: "All DAGs registered in the control plane (compact).",
	}, h.readDagList)
	s.AddResourceTemplate(&mcpsdk.ResourceTemplate{
		Name: "run-detail", URITemplate: "run://detail/{dag_id}/{run_id}", MIMEType: "application/json",
		Description: "A DAG run's detail (state, type, timing).",
	}, h.readRunDetail)
	s.AddResourceTemplate(&mcpsdk.ResourceTemplate{
		Name: "task-instances", URITemplate: "task://instances/{dag_id}/{run_id}", MIMEType: "application/json",
		Description: "The task instances of a DAG run (state, try, duration).",
	}, h.readTaskInstances)
	s.AddResourceTemplate(&mcpsdk.ResourceTemplate{
		Name: "task-log", URITemplate: "log://task/{dag_id}/{run_id}/{task_id}/{try_number}", MIMEType: "text/plain",
		Description: "A task attempt's log, truncated to the last lines and sanitized.",
	}, h.readTaskLog)
}

func (h *handlers) readDagList(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
	out, err := h.fetchDagList(ctx, listDagsInput{})
	if err != nil {
		return nil, err
	}
	return jsonResource(req.Params.URI, out)
}

func (h *handlers) readRunDetail(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
	p, err := uriParams(req.Params.URI, "run://detail/", 2)
	if err != nil {
		return nil, err
	}
	resp, err := h.api.GetDagRunWithResponse(ctx, p[0], p[1])
	if err != nil {
		return nil, fmt.Errorf("fetching run: %w", err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return nil, fmt.Errorf("control plane returned %d for run %s/%s", resp.StatusCode(), p[0], p[1])
	}
	r := resp.JSON200
	detail := map[string]any{
		"dag_id":   deref(r.DagId),
		"run_id":   deref(r.DagRunId),
		"state":    stateString(r.State),
		"run_type": runTypeString(r.RunType),
	}
	if r.StartDate != nil {
		detail["start_date"] = r.StartDate
	}
	if r.EndDate != nil {
		detail["end_date"] = r.EndDate
	}
	return jsonResource(req.Params.URI, detail)
}

type taskInstanceSummary struct {
	TaskID          string  `json:"task_id"`
	State           string  `json:"state"`
	TryNumber       int     `json:"try_number"`
	DurationSeconds float32 `json:"duration_seconds,omitempty"`
}

func (h *handlers) readTaskInstances(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
	p, err := uriParams(req.Params.URI, "task://instances/", 2)
	if err != nil {
		return nil, err
	}
	resp, err := h.api.ListTaskInstancesWithResponse(ctx, p[0], p[1])
	if err != nil {
		return nil, fmt.Errorf("listing task instances: %w", err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return nil, fmt.Errorf("control plane returned %d listing task instances", resp.StatusCode())
	}
	var tis []apiclient.TaskInstance
	if resp.JSON200.TaskInstances != nil {
		tis = *resp.JSON200.TaskInstances
	}
	out := make([]taskInstanceSummary, 0, len(tis))
	for _, ti := range tis {
		out = append(out, taskInstanceSummary{
			TaskID:          deref(ti.TaskId),
			State:           stateString(ti.State),
			TryNumber:       deref(ti.TryNumber),
			DurationSeconds: deref(ti.Duration),
		})
	}
	return jsonResource(req.Params.URI, map[string]any{"task_instances": out, "total_entries": len(out)})
}

func (h *handlers) readTaskLog(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
	p, err := uriParams(req.Params.URI, "log://task/", 4)
	if err != nil {
		return nil, err
	}
	try, err := strconv.Atoi(p[3])
	if err != nil || try < 1 {
		return nil, fmt.Errorf("uri %q: try_number must be a positive integer", req.Params.URI)
	}
	resp, err := h.api.GetTaskLogsWithResponse(ctx, p[0], p[1], p[2], try)
	if err != nil {
		return nil, fmt.Errorf("fetching task log: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("control plane returned %d fetching task log", resp.StatusCode())
	}
	return &mcpsdk.ReadResourceResult{Contents: []*mcpsdk.ResourceContents{{
		URI: req.Params.URI, MIMEType: "text/plain", Text: sanitizeLogTail(string(resp.Body), resourceLogTail),
	}}}, nil
}

// uriParams strips scheme+prefix from a resource URI and returns exactly n
// non-empty, URL-decoded path segments. A path segment supplied by the agent is
// data, never a raw selector (R05): it is decoded here and passed to the typed
// client, which URL-encodes it back into the request path — so a "../" or a stray
// slash cannot escape the intended endpoint.
func uriParams(uri, prefix string, n int) ([]string, error) {
	rest, ok := strings.CutPrefix(uri, prefix)
	if !ok {
		return nil, fmt.Errorf("uri %q does not match template %q", uri, prefix)
	}
	segs := strings.Split(rest, "/")
	if len(segs) != n {
		return nil, fmt.Errorf("uri %q: want %d segments after %q, got %d", uri, n, prefix, len(segs))
	}
	out := make([]string, n)
	for i, s := range segs {
		d, derr := url.PathUnescape(s)
		if derr != nil {
			return nil, fmt.Errorf("uri %q: bad segment %q: %w", uri, s, derr)
		}
		if d == "" {
			return nil, fmt.Errorf("uri %q: empty path segment", uri)
		}
		out[i] = d
	}
	return out, nil
}

func jsonResource(uri string, v any) (*mcpsdk.ReadResourceResult, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encoding resource %q: %w", uri, err)
	}
	return &mcpsdk.ReadResourceResult{Contents: []*mcpsdk.ResourceContents{{
		URI: uri, MIMEType: "application/json", Text: string(b),
	}}}, nil
}

func stateString[T ~string](p *T) string {
	if p == nil {
		return ""
	}
	return string(*p)
}

func runTypeString(p *apiclient.DAGRunRunType) string {
	if p == nil {
		return ""
	}
	return string(*p)
}
