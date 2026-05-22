# Phase 2 Prompt — Control Plane Core

## Goal

Bring up the Leoflow Control Plane: the HTTP API server (Airflow-compatible), the scheduler state machine, JWT auth, leader election, and full observability.

By the end of this phase:

1. `leoflow server` boots, connects to Postgres + Redis, and serves `/api/v2/...`.
2. `POST /auth/token` issues JWTs that work against all protected endpoints.
3. `POST /api/v2/dags/{dag_id}/dagRuns` creates a queued `dag_run` and the scheduler transitions task instances through the state machine — but tasks are not yet executed (Phase 3).
4. `/metrics` exposes Prometheus metrics; OTel traces are exported.
5. Leader election works: starting two replicas, only one runs the scheduler loop.
6. `leoflow push dag.json` registers a DAG and version in the database.

## Prerequisites

- Phase 1 must be complete (including A+ grade and clean lint).
- Read all ADRs again, especially 0007, 0008, 0009, 0010, 0012, 0013.

## Constraints

- Gin for HTTP, sqlc for SQL, slog for logs, OTel for traces, prometheus/client_golang for metrics.
- Every protected endpoint validates JWT and loads `User` and tenant into context.
- Every endpoint emits a structured log line with `trace_id`, `dag_id` (if applicable), and the user.
- The scheduler is **deterministic**: re-running it on identical state yields identical decisions.
- No Python anywhere in this phase.
- **ADR 0011 (TDD), 0012 (A+), 0013 (Scalar docs), and 0014 (Supply Chain) all govern every change.** See TDD and Quality workflows below.
- **A+ floor maintained.** Every exported identifier in new code has GoDoc; complexity ≤ 15; `make lint` clean.

## TDD Workflow (Non-Negotiable)

Same workflow as Phase 1: failing test first, separate commit (or clearly test-first sequence within one commit), then implementation. Coverage floor for Phase 2: **75% per package**.

**Special focus for this phase: the scheduler state machine.** ADR 0011 mandates exhaustive coverage of:

- Every `from_state × to_state` transition (allowed and rejected).
- Every trigger rule × upstream state combination.
- `upstream_failed` propagation.
- Leader/follower race conditions.

The state machine tests must exist **before** the state machine implementation. Concretely:

1. Write `internal/scheduler/state_machine_test.go` enumerating every transition.
2. Confirm all tests fail (the implementation doesn't exist yet).
3. Implement `internal/scheduler/state_machine.go` until all tests pass.

## Deliverables

### 1. HTTP server

`cmd/leoflow-server/main.go` boots:

- HTTP server on `:8080` (configurable)
- Prometheus metrics on `:9090/metrics` (separate port for safety)
- pprof on `:6060` in debug mode

Middlewares (in this order):

1. Recovery
2. Request ID generation
3. OTel HTTP middleware
4. Structured logger
5. CORS (configurable, default permissive for dev, lock-down preset for prod)
6. JWT auth (skipped on `/auth/*`, `/healthz`, `/readyz`, `/metrics`, `/docs/*`)
7. Tenant resolution
8. RBAC check

### 1.1. Embedded API documentation (Scalar — ADR 0013)

The server embeds both the OpenAPI spec and the Scalar UI assets at build time and serves them:

- `GET /docs` → renders the Scalar API reference, themed for Leoflow.
- `GET /openapi.yaml` → serves the raw OpenAPI document.
- `GET /openapi.json` → serves the same document as JSON for tooling.

Implementation notes:

- Use `//go:embed docs/api/openapi.yaml` to embed the spec.
- Use `//go:embed internal/docs/assets/*` for the Scalar HTML wrapper (a single HTML file referencing the Scalar CDN-hosted JS, OR fully self-hosted Scalar assets — prefer self-hosted to avoid CDN dependency).
- The wrapper HTML configures Scalar with `_integration: "go"`, dark mode default, and `theme: "purple"` (matching the Leoflow brand).
- The `/docs` route is **public** (no JWT required) so operators can read docs without first logging in. The "try it out" feature inside Scalar will require the user to paste their JWT manually — document this in the UI.

### 2. Endpoints

Implement the OpenAPI spec at `docs/api/openapi.yaml`. All endpoints in this file are in scope.

For each handler:

- Translate the internal `domain.*` type to the Airflow-compatible DTO at the edge.
- Return RFC 7807 problem details on errors.
- Paginate where applicable using `limit` + `offset`.

The clearTaskInstances endpoint must:

1. Find matching task instances.
2. Transition them to `none` state.
3. If `reset_dag_runs=true`, also reset the parent DAG run state to `queued`.
4. Increment `try_number` on the next execution.

### 3. JWT auth

In `internal/auth/`:

- `JWTAuthenticator` implementing the `Authenticator` interface.
- HS256 signing, secret from `LEOFLOW_JWT_SECRET`.
- Tokens valid for 1 hour by default.
- `/auth/token` validates credentials against `users` (bcrypt) and issues JWT.
- Failed login rate limit: 5 per minute per IP (in-memory token bucket is fine for MVP).

### 4. RBAC enforcement

A middleware checks the user's roles against the required permission for each route. Mapping:

| Endpoint | Required permission |
|---|---|
| GET /api/v2/dags* | read:dag |
| PATCH /api/v2/dags/{id} | write:dag |
| POST /api/v2/dags/{id}/dagRuns | execute:dag |
| POST /api/v2/dags/{id}/clearTaskInstances | write:task_instance |
| GET .../logs/... | read:task_instance |
| GET /api/v2/xcoms/... | read:xcom |

The `admin` role bypasses all checks via the `admin:*` permission.

### 5. Scheduler loop

In `internal/scheduler/`:

```go
type Scheduler struct {
    db       *Postgres
    redis    *Redis
    logger   *slog.Logger
    metrics  *Metrics
    interval time.Duration
}

func (s *Scheduler) Run(ctx context.Context) error {
    // Run the loop until ctx is cancelled.
    // Each iteration:
    //   1. Examine paused DAGs - skip.
    //   2. For each active DAG, decide if a new dag_run should be created (cron + catchup logic).
    //   3. For each running dag_run, evaluate task instances:
    //      - Move 'none' -> 'scheduled' if dependencies satisfied per trigger_rule.
    //      - Move 'scheduled' -> 'queued' (executor will handle from here in Phase 3).
    //      - Detect upstream_failed propagation.
    //      - Mark dag_run as success/failed when all leaf tasks are terminal.
    //   4. Emit metrics.
}
```

Trigger rule semantics (verbatim, do not improvise):

- `all_success`: all upstream tasks in state `success`.
- `all_failed`: all upstream tasks in state `failed`.
- `all_done`: all upstream tasks in any terminal state (`success`, `failed`, `skipped`).
- `one_success`: at least one upstream in `success`, none still running.
- `one_failed`: at least one upstream in `failed`, none still running.

If an upstream is `upstream_failed`, propagate `upstream_failed` downstream (unless trigger_rule is `all_done`).

### 6. Leader election

In `internal/scheduler/leader.go`:

- On startup, call `SELECT pg_try_advisory_lock(<scheduler_lock_id>)`.
- Lock ID is a fixed `int64` constant: `0x4C656F466C6F77` ("LeoFlow" in hex).
- The connection holding the lock is dedicated (separate `*sql.DB` with `SetMaxOpenConns(1)`).
- If acquired, become leader. Start scheduler loop. Heartbeat every 30s.
- If not acquired, become follower. Poll every 5s.
- On graceful shutdown, release the lock explicitly.
- Update `replicas` table accordingly for observability.

### 7. DAG registration

`leoflow push <dag.json>`:

1. Validate JSON against `dag-schema.json`.
2. POST to `/api/v2/dags/{dag_id}/versions` (new internal endpoint, not in the Airflow API).
3. Server upserts the `dags` row, inserts a new `dag_versions` row, and sets `current_version_id`.
4. Server emits an audit log entry.

The push endpoint is **idempotent**: pushing the same `spec_hash` returns the existing version.

### 8. Observability

Follow ADR 0010 strictly:

- All metrics listed in the ADR are wired and emit real values.
- Every handler creates an OTel span.
- Span attributes use the `leoflow.*` namespace.
- Logs use slog JSON handler. No human-readable handler in production builds.

Add a `pkg/observability/setup.go` that does:

```go
func Setup(ctx context.Context, cfg Config) (*Telemetry, func(), error) { ... }
```

Returns a shutdown function the caller defers.

### 9. Configuration

`internal/config/config.go` defines the full config struct (Viper-backed). Top-level keys:

```yaml
server:
  http_addr: "0.0.0.0:8080"
  metrics_addr: "0.0.0.0:9090"
  cors:
    allowed_origins: ["http://localhost:8080"]
database:
  url: "postgres://leoflow:leoflow@localhost:5432/leoflow?sslmode=disable"
  max_open_conns: 25
  max_idle_conns: 5
redis:
  url: "redis://localhost:6379/0"
auth:
  provider: jwt
  jwt:
    secret: ""                              # required, no default
    token_ttl_seconds: 3600
scheduler:
  loop_interval_ms: 1000
  enabled: true                             # set false on followers (auto-set via leader election)
observability:
  otel:
    enabled: true
    endpoint: "localhost:4317"
  log_level: "info"
  log_format: "json"
```

### 10. Tests

- Unit tests per package, mocking with gomock.
- Integration test (`//go:build integration`):
  - Spin up Postgres + Redis via testcontainers-go.
  - Boot the server.
  - Push a fixture `dag.json`.
  - Trigger a DAG run via API.
  - Assert the scheduler transitions tasks through states.
  - Without an executor, tasks should stop at `queued`.

## Acceptance Criteria

- `leoflow server` boots in under 2 seconds.
- `curl -X POST /auth/token` returns a working JWT.
- `curl /api/v2/dags` returns the pushed DAGs.
- Triggering a DAG run results in task instances in `queued` state within 5 seconds.
- Killing the leader and starting another replica causes failover within 10 seconds.
- `/metrics` shows all metrics from ADR 0010.
- Integration tests pass.
- Test coverage ≥ **75% per package**.
- **State machine has exhaustive transition coverage** (every legal/illegal `from_state × to_state` pair, every trigger rule combination tested).
- Commit history shows tests preceding implementation for the scheduler and auth packages.
- **`make lint` clean** with zero warnings; `goreportcard-cli` still A+.
- **`GET /docs` renders the Scalar API reference** with all `/api/v2/` endpoints visible and code samples in Go and curl.
- **`GET /openapi.yaml` and `/openapi.json` return the embedded spec.**
- **`govulncheck` reports zero affecting vulnerabilities.**

## Hints

- Do not introduce GORM or any ORM. Use sqlc-generated code only.
- Wrap every error: `fmt.Errorf("creating dag run: %w", err)`.
- The scheduler should sleep with `time.NewTicker`, not `time.Sleep`, so cancellation works.
- For pagination, return `Link` headers in addition to `total_entries` (the Airflow UI prefers `total_entries`).

## Out of Scope (Do Not Implement)

- Pod / container creation (Phase 3)
- Agent communication (Phase 3)
- XCom push / pull (Phase 4)
- Log retrieval implementation (Phase 4 — return a stub for now)
