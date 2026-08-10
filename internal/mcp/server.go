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
