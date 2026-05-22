# Phase 4 Prompt — XCom and Log Shipping

## Goal

Close the data flow loop: implement the Redis-backed XCom subsystem and the log shipping pipeline. After this phase, tasks can pass data to each other and operators can view full logs in the UI.

## Prerequisites

Phases 1-3 complete. End-to-end smoke test passes.

## Constraints

- Redis is now mandatory in all deployment modes. `docker-compose.yaml` and the Helm chart must include it.
- XCom payloads have a hard 256 KB limit. Larger values must be rejected with a clear error message.
- Log files must remain accessible via the API even after the source pod is gone.
- **ADRs 0011 (TDD), 0012 (A+), 0014 (Supply Chain) govern every change.** Coverage floor: **80% per package**. A+ maintained.

## TDD Notes for This Phase

Critical behaviors that must have tests written first:

- **Size limit enforcement.** Write a test that pushes 256KB+1 bytes and expects rejection. Then implement.
- **Schema validation.** Write tests for valid/invalid schemas. Then implement.
- **TTL behavior.** Use a fake clock to test TTL expiration without sleeping. Write the test, then implement.
- **Cross-tenant isolation.** A task in tenant A must NOT be able to fetch XCom from tenant B. Test this explicitly.
- **Log file rotation and read-back.** Test that logs persist past pod termination.

## Deliverables

### 1. XCom subsystem

In `internal/xcom/`:

- `RedisBackend` implementing `Backend` interface (`Push`, `Fetch`, `Delete`, `List`).
- Key format from ADR 0006: `xcom:{tenant_id}:{dag_id}:{run_id}:{task_id}:{key_name}`.
- TTL defaults to 7 days (configurable per DAG via `xcom_ttl_seconds`).
- Size validation BEFORE Redis write.
- On push, write the metadata to `xcom_index` (Postgres) for UI listing.

Wire the Agent gRPC methods:

- `PushXCom` calls `RedisBackend.Push`.
- `FetchXCom` calls `RedisBackend.Fetch` after validating the requester is allowed (same dag_run, declared dependency).

### 2. Schema validation

If a task declares `xcom_schema`, validate the payload against the schema on push. Reject with `rejection_reason="schema_mismatch"` and a detail message pointing to the failing field.

### 3. XCom read API

The OpenAPI endpoint `GET /api/v2/xcoms/{dag_id}/{dag_run_id}/{task_id}/{key}`:

- Reads from `xcom_index` to find the Redis key.
- Reads the value from Redis.
- Returns as the Airflow-compatible `XComEntry` shape.
- 404 if expired or not found.

### 4. Log shipping

In `internal/logs/`:

- `Sink` interface: `Write(taskInstanceID string, line LogLine)`, `Read(...) io.ReadCloser`.
- Implementations:
  - `DiskSink` — writes to `${LEOFLOW_LOG_DIR}/{tenant}/{dag_id}/{run_id}/{task_id}/{try}.log`.
  - `S3Sink` — buffers and uploads to S3 (key pattern matches the disk pattern).
  - `GCSSink` — same for GCS.

The Agent streams log lines via gRPC `StreamLogs`. The Control Plane writes them through the configured sink.

For long-running tasks, the sink buffers and flushes every 5 seconds OR every 1 MB, whichever comes first. On task termination, the sink does a final flush before responding to `ReportState`.

### 5. Log read API

`GET /api/v2/dags/{dag_id}/dagRuns/{dag_run_id}/taskInstances/{task_id}/logs/{try_number}`:

- For DiskSink: stream the file directly.
- For S3/GCS: stream from the bucket. Use range requests if the UI sends `Range` headers.
- Live tailing for running tasks: if the task is still running, the response sets `Transfer-Encoding: chunked` and streams new lines as they arrive (via Redis pubsub, see below).

### 6. Live log tailing (optional but recommended)

While a task is running, the Agent also publishes log lines to a Redis pub/sub channel:

```
log_tail:{task_instance_id}
```

The API handler for log reads subscribes to this channel when the task is in `running` state and streams lines to the HTTP client. The Airflow UI gets live updates without polling.

### 7. Cleanup job

A background goroutine (leader-only) runs every hour:

- Deletes `xcom_index` rows older than `expires_at`.
- For DiskSink, deletes log files older than the configured retention (default 30 days).
- For S3/GCS, relies on lifecycle policies (documented in the operator guide).

## Acceptance Criteria

- Two-task DAG: `extract` returns `{"rows": 100}`, `transform` reads it via `xcom_input` and prints the count. Runs to success.
- A task that tries to push a 1 MB XCom is rejected with a clear error and the task fails.
- A task that violates its declared `xcom_schema` is rejected.
- Logs are visible in the UI for completed tasks.
- Live tailing works for running tasks.

---

# Phase 5 Prompt — Airflow UI Integration

## Goal

Run the Airflow 3.2.x UI against the Leoflow API and verify the user experience end to end. No code changes to the Airflow UI itself.

## Prerequisites

Phases 1-4 complete.

## Deliverables

### 1. docker-compose for full standalone

`docker-compose.yaml` at the repo root:

```yaml
services:
  postgres: ...
  redis: ...
  leoflow-server: ...                  # the Go control plane
  airflow-webserver:                   # apache/airflow:3.2.1
    environment:
      AIRFLOW__API__AUTH_BACKENDS: ...
      # configure to point at leoflow-server
```

Verify these UI flows work:

- Logging in with `admin` / `<bootstrap-password>`.
- DAGs list displays.
- Clicking a DAG shows the graph view.
- Triggering a DAG run from the UI works.
- Grid view shows task states updating live.
- Clicking a task instance shows the logs (live tail while running).
- Clearing a task instance re-runs it.
- Pausing / unpausing a DAG works.

### 2. Mismatch remediation

Wherever the UI breaks, document the cause in `docs/ui-compatibility.md` and fix the API. Common areas to check:

- Field naming: `logical_date` vs `execution_date`.
- Pagination shape: `total_entries` vs `Link` headers.
- DAG details endpoint may require fields we have not yet populated (e.g., `timetable_description`).

### 3. Authentication wiring

The Airflow webserver needs to use a JWT obtained from Leoflow. Configure via `AIRFLOW__API_AUTH__JWT_SECRET` (must match `LEOFLOW_JWT_SECRET`) or via the OIDC-compatible bridge if simpler. Document the chosen approach.

### 4. UI screenshots

In `docs/screenshots/` add screenshots of:

- DAGs list
- Graph view
- Grid view
- Task log view
- Triggered run history

These go in the README and the public docs.

## Acceptance Criteria

- All flows above work without modifying the Airflow UI image.
- A new user can clone the repo, run `docker compose up`, and have a working DAG run with logs viewable in their browser in under 5 minutes.

---

# Phase 6 Prompt — Hardening, Helm Chart, and Documentation

## Goal

Make the MVP releasable: load tests, Helm chart, operator guide, and final polish.

## Prerequisites

Phases 1-5 complete.

## Deliverables

### 1. Helm chart

`helm/leoflow/`:

- Deployment for `leoflow-server` (2 replicas by default for HA).
- StatefulSet or Deployment for Postgres + Redis (with PVCs).
- ConfigMap for configuration.
- Secret for JWT secret and DB credentials.
- ServiceAccount + Role + RoleBinding granting permission to manage Pods.
- Service + Ingress (configurable).
- Optional: include the Airflow webserver as a sub-chart.
- ServiceMonitor for Prometheus Operator users.

Values file documented exhaustively.

### 2. Load test suite

`tests/load/`:

- A k6 or vegeta script that drives 10k task instances across 100 DAGs and measures:
  - Scheduling latency (decision time per task)
  - End-to-end task duration (queue-to-success)
  - Memory and CPU of the control plane
  - Postgres connection pool saturation
  - Redis memory consumption

Generate a report comparing against documented targets (sub-200ms scheduling, sub-5s cold start).

### 3. Operator documentation

`docs/operator-guide.md`:

- Production deployment recipe (K8s + managed Postgres + managed Redis)
- Backup and restore procedures (Postgres + Redis AOF)
- Monitoring setup (Prometheus rules, Grafana dashboards)
- Common operational scenarios (rotating JWT secret, upgrading Leoflow, scaling)
- Troubleshooting playbook (pod stuck pending, scheduler not advancing, etc.)

### 4. Developer documentation

`docs/developer-guide.md`:

- Writing your first DAG (`leoflow.yaml` + Python)
- Migration from Airflow (what works, what doesn't, what to change)
- XCom best practices and the 256 KB rule
- Choosing resources for tasks
- CI/CD integration

### 5. Polish

- All TODO/FIXME comments resolved or documented as known limitations.
- All metrics from ADR 0010 verified to emit.
- License headers on every Go file.
- `CHANGELOG.md` with the v0.1.0 release notes.
- A release script that tags, builds binaries, builds and pushes Docker images, and publishes Helm chart.

## Acceptance Criteria

- The Helm chart installs cleanly on a fresh K8s cluster.
- Load test achieves: 10k task instances over 100 DAGs, scheduling latency p99 under 500ms.
- A new operator can read the operator guide and have a production deployment running in under 1 hour.
- A new developer can read the developer guide and submit their first DAG in under 30 minutes.
- **Go Report Card still A+** across the full codebase.
- **OpenSSF Best Practices passing badge obtained** at `bestpractices.dev` (self-certified, ADR 0014).
- **OpenSSF Scorecard score ≥ 7.0/10** on weekly workflow.
- **All security workflows green** on `main`: govulncheck, gosec, Trivy, CodeQL.
- **Signed release artifacts:** binaries and container images signed with `cosign`.
- Total test coverage ≥ **85% per package**.

## Release Decision

If all acceptance criteria pass and load tests meet targets, tag `v0.1.0` and publish.
