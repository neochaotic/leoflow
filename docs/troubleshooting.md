# Troubleshooting & observability

## Common issues

| Symptom | Cause / fix |
|---|---|
| `command not found: leoflow` | Not on PATH — `go install …/cmd/leoflow@latest` and add `$(go env GOPATH)/bin` to PATH. |
| `docker compose up … no such file` | Run `leoflow dev` from the leoflow source tree, or use `--no-up` with deps already running. |
| Task pod `ErrImagePull` | The DAG's image isn't in the cluster — rebuild + import (cluster-mode rebuilds on save; for a manual push, `leoflow compile --build --push`). |
| Run stuck at `queued` (subprocess) | The agent must reach the control plane — dev uses `127.0.0.1:<grpc>`; the executor launches async and the agent reports state over gRPC. |
| `Invalid credentials` in the UI | Type the password manually (autofill may add a trailing space; usernames are trimmed, passwords are not). |
| No `DEV` marker on `localhost:8080` | That's the **Demo** (production-like) — the DEV marker is on `leoflow dev` (`:8088`). See [operating modes](operating-modes.md). |
| `setup incomplete: dev database` | Postgres isn't up — start deps (`leoflow dev setup`) or `--no-up` against a running Postgres. |

## Logs
Task logs stream from the agent over gRPC to the control plane's log sink and are
served at `/api/v2/dags/<dag>/dagRuns/<run>/taskInstances/<task>/logs/<try>`
(the UI's drill-down). The sink directory is `LEOFLOW_LOGS_DIR` (must be writable;
`leoflow dev` points it at a temp dir).

## Observability
- **Metrics:** Prometheus at `:9090/metrics` (scheduler, dispatch, inline runner, undispatchable counters).
- **Tracing:** OpenTelemetry — set `LEOFLOW_OBSERVABILITY_OTEL_ENABLED=true` and `…_OTEL_ENDPOINT`.
- **Logs:** structured `slog` (JSON by default), one line per HTTP request with a request id.

Observability ships from the first commit (it is not optional).
