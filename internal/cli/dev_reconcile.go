package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// reconcileStartup deregisters, at Lite boot, every DAG the control plane still
// has registered that is absent from the current workspace (#404) — the stale
// ghosts left by a reused metadata DB or a previous workspace. It reuses the
// watcher's set-diff primitive (removeMissingDags), so a deregister failure
// retries on later ticks. The fetch's failure is non-fatal and fail-safe: with
// no reliable registered set it keeps the workspace IDs and deregisters nothing,
// so an unreachable or flaky control plane never wipes a DAG by mistake.
func reconcileStartup(
	registered map[string]struct{},
	fetchErr error,
	workspace map[string]struct{},
	deleteDag func(dagID string) error,
	logf func(format string, args ...any),
) map[string]struct{} {
	if fetchErr != nil {
		logf("⚠ could not list registered DAGs to reconcile (%v) — skipping stale-DAG cleanup this boot", fetchErr)
		return workspace
	}
	return removeMissingDags(registered, workspace, deleteDag, logf)
}

// registeredDagsResponse is the slice of GET /api/v2/dags reconcile reads: just
// the IDs (the endpoint returns much more, ignored here).
type registeredDagsResponse struct {
	Dags []struct {
		DagID string `json:"dag_id"`
	} `json:"dags"`
}

// fetchRegisteredDagIDs returns the set of dag_ids the control plane currently
// has registered, via GET /api/v2/dags. A non-200 is an error so reconcile stays
// fail-safe — an error page is never read as "nothing is registered" (which would
// deregister the whole workspace).
func fetchRegisteredDagIDs(serverURL, token string) (map[string]struct{}, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	reqURL := strings.TrimRight(serverURL, "/") + "/api/v2/dags?limit=10000"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("listing registered DAGs: %w", err)
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // best-effort close of the response body
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512)) //nolint:errcheck // best-effort read of an error body for the message

		return nil, fmt.Errorf("listing registered DAGs: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var parsed registeredDagsResponse
	if derr := json.NewDecoder(resp.Body).Decode(&parsed); derr != nil {
		return nil, fmt.Errorf("decoding registered DAGs: %w", derr)
	}
	ids := make(map[string]struct{}, len(parsed.Dags))
	for _, d := range parsed.Dags {
		if d.DagID != "" {
			ids[d.DagID] = struct{}{}
		}
	}
	return ids, nil
}

// makeFetchRegistered binds fetchRegisteredDagIDs to the running control plane so
// devWatchLoop can seed its boot-time reconcile from the real registration state.
func makeFetchRegistered(token, uiURL string) func() (map[string]struct{}, error) {
	return func() (map[string]struct{}, error) { return fetchRegisteredDagIDs(uiURL, token) }
}
