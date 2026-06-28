# Changelog

All notable changes to Leoflow are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project aims to
adhere to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2026-06-28

> **First stable release.** Leoflow runs standard Apache Airflow 3.2 DAGs on a Go
> control plane — no GIL, no Airflow in the scheduling path — one pod per task. This
> promotes `v0.1.0-rc.4` verbatim: the same artifacts, soaked through the rc series.

### The 0.1.0 line, in brief

- **Standard Airflow 3.2 DAGs in Python.** A dependency-free structural shim (ADR
  0024) parses `airflow.sdk` DAGs without importing Airflow; the real provider
  operator runs in the task pod (ADR 0040).
- **Provider operators & sensors.** A native fast path for `bash`/`python`/`http`,
  generic capture for the long tail, and reschedule-mode sensors.
- **86 connection types** generated from real Airflow (ADR 0038 / 0039), with the
  `connectors:` one-liner that bakes providers into each DAG's image.
- **Lite** — a Docker-free, Kubernetes-free local edition: hot-reload, the embedded
  Airflow 3.2 UI, and resilience (Docker-wedged fallback, boot self-heal, per-DAG
  venv reclaim, watcher-token refresh).

Per-rc detail is in the `0.1.0-rc.1` … `0.1.0-rc.4` sections below.

## [0.1.0-rc.4] - 2026-06-28

> Fourth release candidate of the **0.1.0** line — a single Lite fix found while
> testing rc.3.

### Fixed

- **Lite hot-reload no longer stops registering after an hour (#407).** `leoflow
  lite` minted one admin token at startup (one-hour expiry) and reused it for every
  hot-reload registration, deregister, and the boot reconcile; after an hour the
  token expired and every save silently failed to register — only a `✗ … invalid
  token` in the log, with the UI just not updating. The watcher now re-mints a fresh
  token per operation, so a Lite left running for the day keeps reloading.

## [0.1.0-rc.3] - 2026-06-28

> Third release candidate of the **0.1.0** line. On top of rc.2 it hardens the
> **install path** and makes the local **Lite** edition **resilient** — every item
> here surfaced during hands-on testing of rc.2. The boot self-heal is end-to-end
> gated in CI.

### Added

- **Lite self-heals stale state on boot (#404).** A reused metadata DB no longer
  leaves "ghost" DAGs and orphan import errors that the UI showed but could not
  remove: on startup Lite reconciles the registered DAGs against the workspace —
  deregistering what is gone on disk and clearing stale import errors — fail-safe
  (if the control plane can't be listed, it wipes nothing). A new end-to-end gate
  (`lite-selfheal`) keeps it from regressing.
- A DAG's **per-DAG venv is reclaimed when the DAG is deregistered** (and logged),
  instead of lingering on disk with the Airflow SDK; a later reload re-creates it
  if the DAG returns. The sweep of venvs orphaned while Lite was stopped is tracked
  for a scheduled GC (#406).

### Fixed

- **Install:** the pinned-version command placed `LEOFLOW_VERSION` on `curl`
  instead of `sh`, so `install.sh` resolved latest-stable and installed the
  previous release rather than the pinned rc. The variable now sits on the `sh`
  side of the pipe (#402).
- **Lite falls back to a Docker-free Postgres when Docker is wedged (#403).** The
  auto-resolvers now ping the Docker daemon; a present-but-unresponsive Docker
  (e.g. a hung Docker Desktop returning 500s) falls back to the managed Postgres
  and the subprocess executor instead of aborting on `docker compose up`.

## [0.1.0-rc.2] - 2026-06-24

> Second release candidate of the **0.1.0** line. On top of rc.1 it ships
> **reschedule-mode sensors** — the first ADR 0040 Phase B capability, which rc.1
> still rejected at compile — plus release- and CI-hardening fixes. E2E-gated on a
> real k3d cluster.

### Added

- **Reschedule-mode sensors (ADR 0040, Phase B).** A sensor declared
  `mode='reschedule'` now releases its pod between pokes instead of holding it:
  on a not-ready poke the task transitions to `up_for_reschedule` and the
  scheduler re-dispatches it once `reschedule_at` arrives — no retry budget
  consumed (mirrors the `up_for_retry` rail). The agent reports
  `up_for_reschedule` only when the task exits 75 **and** writes a parseable
  reschedule file, so a bare exit 75 stays an ordinary failure. Validated on a
  real k3d cluster via a `DateTimeSensor(mode='reschedule')` e2e guard that
  visibly passes through `up_for_reschedule` and is re-dispatched to success
  (#380, #389).

### Fixed

- Deterministic reschedule-sensor e2e guard: the wait loop asserts the sensor
  passed through `up_for_reschedule` without timing flakiness (#390).
- Release notes now install the exact release tag (`LEOFLOW_VERSION` + the
  tag's `install.sh`) instead of resolving latest-stable, and drop the stale
  `(pre-alpha)` wording (ADR 0037) (#391).

### Changed

- CI secret-scan runs a pinned `gitleaks` binary and drops the flaky Docker Hub
  pull (#392).

## [0.1.0-rc.1] - 2026-06-16

> First release candidate of the **0.1.0** line — a Go control plane that runs
> DAGs end-to-end and serves the embedded Apache Airflow 3.2.1 UI. Published as a
> pre-release after hands-on maintainer validation of the UI and the connector
> flow in a real browser.

### Added

- **Embedded Airflow 3.2.1 UI (Phase 5).** The control plane embeds the pinned
  Airflow 3.2.1 React SPA (`go:embed`, ADR 0017) and serves it at `/`, alongside
  the implemented internal UI API:
  - Auth/identity: `GET /ui/config`, `GET /ui/auth/me`, `GET /ui/auth/menus`
    (curated to the screens Leoflow backs), `POST /ui/auth/token`.
  - Read views: `GET /ui/dags` (latest runs embedded, no N+1), `/ui/dags/{id}/latest_run`,
    `/ui/grid/runs/{id}`, `/ui/grid/structure/{id}`, `/ui/structure/structure_data`,
    `/ui/grid/ti_summaries/{id}` (NDJSON stream with a conditional-GET ETag),
    `GET /api/v2/dags/{id}/details` (cron→English), `GET /api/v2/version`.
  - Graceful degradation: unimplemented `/ui` screens return schema-valid empty
    responses; writes degrade to `501`.
  - Static assets are gzipped; the SPA shell and assets load without auth so the
    login screen is reachable, while `/api/v2` and `/ui` data stay gated.
- **One-command demo.** `docker compose --profile demo up --build` brings up
  Postgres, Redis, and the control plane with the UI; bootstraps an admin user.
  `deploy/Dockerfile.server` builds the single image.
- `make fetch-airflow-ui` extracts the pinned UI bundle from `apache/airflow:3.2.1`.
- **Connectors — provider hooks/operators without `apache-airflow` in the control
  plane (ADR 0038/0039).** `connectors:` / `dependencies:` in `leoflow.yaml` install a
  provider into the DAG image; a **generated connection catalog** (86 connection
  types, derived from real Airflow) drives the UI's Add-Connection form
  (`/ui/connections/hook_meta`) and the structural connection-test probe
  (`POST /api/v2/connections/test`). Admin Variables and Connections are delivered to
  each task as `AIRFLOW_VAR_*` / `AIRFLOW_CONN_*` (encrypted at rest, ADR 0019) and
  resolved by Airflow's native env-secrets backend — in pods (via the agent over gRPC)
  and in Lite/subprocess.
- **Airflow operators & poke sensors (ADR 0040, Phase A).** Any provider operator or
  sensor runs in its own pod through a generic executor
  (`import_string(class)(**args) → render_template_fields → execute(context)`) — no
  per-operator code. Includes `ti.xcom_pull` chaining between operators; the
  standalone run context (`ds`, `ts`, `data_interval_*`, `params`, `var`, `conn`);
  operator extra-links (the UI "open in …" buttons); multi-key XCom; and native
  `@task` / `bash` parity (including Jinja-templated bash commands). Reschedule-mode
  sensors, deferrable operators, dynamic task mapping and branching are loudly
  rejected with actionable errors (tracked for later phases).

### Changed

- ADR 0007 (Airflow UI Compatibility) premise refined from Airflow 2.x-style
  `/api/v2` parity to the pinned Airflow 3.x `/ui/*` approach (see ADR 0017,
  ADR 0018, `docs/ui-compatibility.md`).

### Fixed

- The static SPA (shell + assets) is now public, so an unauthenticated first
  visit can load the app and reach the login screen.
- `/ui/auth/me` returns the authenticated user's username (the JWT now carries
  the email claim).

### Notes

- The pinned Airflow UI is a tactical MVP choice; a purpose-built Leoflow UI on
  the stable `/api/v2` is the long-term direction (ADR 0018).
- Browser end-to-end verification (rendering, write-flow paths, screenshots) is
  the remaining Phase 5 acceptance step; see `docs/ui-compatibility.md`.

[Unreleased]: https://github.com/neochaotic/leoflow/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/neochaotic/leoflow/compare/v0.1.0-rc.4...v0.1.0
[0.1.0-rc.4]: https://github.com/neochaotic/leoflow/compare/v0.1.0-rc.3...v0.1.0-rc.4
[0.1.0-rc.3]: https://github.com/neochaotic/leoflow/compare/v0.1.0-rc.2...v0.1.0-rc.3
[0.1.0-rc.2]: https://github.com/neochaotic/leoflow/compare/v0.1.0-rc.1...v0.1.0-rc.2
[0.1.0-rc.1]: https://github.com/neochaotic/leoflow/releases/tag/v0.1.0-rc.1
