# Changelog

All notable changes to Leoflow are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project aims to
adhere to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **A task-pod egress NetworkPolicy** (`taskNetworkPolicy`, off by default). Task
  pods run untrusted author code, so this is where the "a pod can reach cloud
  metadata / the apiserver / another service" risk is contained — at the network
  layer, for every task type — not in the control plane (ADR 0048). When enabled
  it denies ingress, allows DNS and the control-plane gRPC (the agent dials back),
  then all other egress **except the cloud metadata endpoint** (`169.254.0.0/16`),
  which is always blocked — the one unambiguous SSRF target. `blockPrivateNetworks`
  additionally denies RFC1918 + the apiserver; it is opt-in because a DAG calling
  an internal service is legitimate and the policy cannot tell that from the
  apiserver by IP (ADR 0047). This is the network-layer control ADR 0047/0048 point
  to, and the same one Argo and Kubeflow recommend.

### Added

- **A synchronous dispatch failure now backs off and eventually gives up, instead
  of retrying every tick forever** (ADR 0031 Amendment A). When `Dispatch` failed
  synchronously — kube-apiserver unreachable, RBAC denied, quota, an admission
  webhook reject — the task stayed `scheduled` and the planner re-attempted it on
  every tick, with no backoff, surfaced by no reaper (the dispatch-lost reaper
  only sees `queued`). A permanent misconfiguration became a silent tight loop.

  The task instance now records `dispatch_attempts` and `next_dispatch_at` (two new
  columns, mirroring `reschedule_at`); the planner does not re-dispatch until the
  exponential, capped backoff elapses, and after the attempt budget is spent the
  task fails as `dispatch_failed` so the run finalizes. A dispatch failure does
  **not** consume the task's `try_number` — it is infrastructure, not a task
  failure, so a `retries: 0` task is not killed by a transient blip.
  `dispatch_failed` is distinct from `dispatch_lost` (dispatched then vanished) and
  from a task's own `failed` (the code ran and failed).

- **`leoflow compile` now rejects a task graph that cannot execute.** Three
  defects shared one symptom, and it was the worst one available: the run
  started, no task in the affected region ever became ready, and the run sat in
  `running` indefinitely with nothing on screen explaining why. There was no
  error to read — from the scheduler's side nothing had gone wrong, it was
  waiting on a predecessor, correctly, forever.

  - a **cycle** (`a >> b >> c >> a`, or a self-dependency). Airflow does not
    reject this: the parser emits a perfectly well-formed `dag.json`.
  - a **`depends_on` naming a task that is not declared** — the more common typo.
  - a **duplicated `task_id`**, which is the same family for a different reason:
    the graph keys by id, so the losing definition was silently dropped and the
    DAG ran a subset of what was written.

  The error names the tasks involved (`cyclic task graph: a -> c -> b -> a`),
  because "cycle detected" alone leaves the author to find it by hand in a
  200-task DAG. Cycle detection is a three-color DFS rather than a visited set: a
  visited set reports a diamond — two paths rejoining at one task, one of the most
  ordinary shapes a real DAG has — as a cycle. Traversal follows declared task
  order, so the same DAG always reports the same cycle.

  Validated at compile and again at registration, so a hand-written or
  machine-generated `dag.json` cannot bypass the compiler.

- **A typo in an alert template is now a compile error.** An unknown placeholder
  is not a rendering failure — `Render` leaves it alone — so `{{taskk}}` used to
  survive compile and reach the operator verbatim, discovered in the alert that
  was supposed to explain an outage. `leoflow compile` now rejects it, naming the
  offending placeholder and listing the supported set. Airflow-style names
  (`{{ ds }}`) are reported by name rather than passing through as text.

- **`leoflow compile` now rejects a `dbt.project` or `dbt.manifest` path it cannot use.** The value
  is resolved with `filepath.Join(dagDir, project)` and, for a Pro image build,
  baked at that same relative path inside the image — so both an absolute path and
  one escaping upward were broken, and broken silently.

  `filepath.Join` does not treat an absolute second element specially:
  `Join("/dags/sales", "/opt/dbt/proj")` is `/dags/sales/opt/dbt/proj`. The
  leading slash was swallowed and dbt was pointed at a directory nobody named. A
  path like `../../../etc` resolved outside the DAG directory, and therefore
  outside the Docker build context, so the image could not contain it either.

  Both fields feed the same `filepath.Join` chain — `project` onto the DAG
  directory, then `manifest` onto that result — so both are checked. Validating
  only `project`, as the first version of this change did, left half the defect in
  place.

  Both surfaced only when dbt ran inside a pod, as "project directory does not
  exist", with nothing connecting it back to `leoflow.yaml`. The error now names
  the field, what it resolves to, and why that cannot work. `.`, `transform`,
  `./transform`, `dbt/transform`, `target/manifest.json` and paths that normalise
  back inside are unaffected.

### Fixed

- **The pod reconciler could garbage-collect a failed pod before its failure was
  recorded.** `Reconcile` reported a failed pod's task instance and then deleted
  the pod if it had aged out — but the delete ran whether or not the report
  succeeded. A transient metadatabase error during `FailTask` meant the pod (the
  only signal that would let the next tick retry) was deleted anyway, stranding
  the task instance in `running` until the slower heartbeat reaper caught it. The
  reconciler now defers collection of a failed pod until its failure is durably
  recorded; a succeeded pod, which has nothing to record, is still collected on
  age alone. (One component was both the state-recorder and the garbage-collector
  with no ordering between them.)

- **The pod reconciler and staging-volume GC ran on every replica, not just the
  leader.** The scheduler loop and its reapers are leader-gated (ADR 0009), but
  `startReconciler` and `startStagingGC` spawned unconditional tickers, so at
  `replicaCount > 1` every replica would list, reconcile, and delete the same task
  pods and staging PVCs — a follower racing the leader's provisioning. Both now
  gate on the same leadership signal (`scheduler.IsLeading`) the scheduler loop
  uses. No behaviour change at the default `replicaCount: 1`, where the single
  replica is always the leader; this is the correctness base that makes running
  Pro with more than one replica safe.

- **Alert messages carried a UUID where the run id belongs.** `{{run_id}}`
  rendered `RunState.RunID`, which is the `dag_runs` primary key — not the
  `run_id` an operator sees in the UI and passes to the API. The alert named a
  run nobody could look up. It now renders the user-facing id.

- **`{{logical_date}}` always rendered empty.** The placeholder was documented and
  substituted, but the dispatcher never populated the field, so every alert using
  it produced a dangling `for `. `RunState` now carries the logical date and the
  dispatcher passes it through.

- **A placeholder with no value renders `(none)`** instead of an empty string —
  `{{logical_date}}` on a manually triggered run, for example. `failed for
  logical date ` is indistinguishable from a truncated message.

### Security

- **Hardened the log sink against path escape.** `DiskSink` interpolated every
  `logs.Ref` field straight into a filesystem path
  (`{root}/{tenant}/{dag}/{run}/{task}/{try}.log`) with no containment, so any
  Ref carrying `../` or a separator read and wrote outside the log root. Both
  filesystem calls were annotated
  `//nolint:gosec // path is built from validated identity fields`, asserting a
  validation step that existed nowhere in `internal/` — which is why static
  analysis stayed quiet on those lines for the life of the repository.

  **Not reachable in any released version.** All five `logs.Ref` construction
  sites pass the database UUID as `RunID`: the scheduler builds `RunState.RunID`
  from `uuidToString(run.ID)`, dispatch mints that same value into the agent
  token, and both read paths resolve the caller's `run_id` through
  `ResolveRunRef` to `ref.DagRunID` before touching the sink. `dag_id` and
  `task_id` carry a charset pattern in the DAG schema, enforced at compile and
  again at registration. No release shipped an exploitable path. What shipped was
  a sink whose safety rested entirely on every current caller happening to pass a
  UUID, under a comment claiming a guarantee that was absent.

  The sink now performs all filesystem work through `os.Root` pinned to the log
  directory, so escape is refused by the runtime rather than by convention. That
  also closes a case no string check can see: a symlink inside the log root whose
  every path component is a legal name, where the traversal happens during kernel
  resolution. `logs.Ref` is additionally validated field by field, which names the
  offending field in the error.

  Separately, `POST /api/v2/dags/{dag_id}/dagRuns` accepted `dag_run_id` verbatim
  from the request body with no validation at all. It now rejects a value that is
  not a usable single path segment. Airflow-generated ids embed an RFC3339
  timestamp and keep working: separators are banned, punctuation is not.

## [0.1.2] - 2026-07-30

> **On-failure alerting, and a Pro install that can no longer be misconfigured into
> silence.** A failed task notifies on its own, without an Airflow callback in the
> loop; and three Pro misconfigurations that used to produce a healthy-looking but
> broken deployment now fail loudly at `helm install` or at boot.
>
> This promotes `v0.1.2-rc.2` unchanged. The candidate was exercised by hand on
> darwin/arm64 — the platform no CI job covers — including the alerting paths, both
> Pro guards, and the callback fix that prompted the respin. Per-candidate detail is
> in the `0.1.2-rc.2` and `0.1.2-rc.1` sections below.

### ⚠️ Upgrade note for Pro operators

`agentTLS.enabled: false` no longer yields a running deployment — it never yielded a
working one, and now says so at install time instead of crash-looping. Full detail and
the migration in the `0.1.2-rc.1` section below.

### Known issues carried into this release

- **A task can be marked `dispatch_lost` while its pod is still `Running` (#461).** The
  mechanism is now understood (#474): no reaper consults Kubernetes, nothing deletes the
  pod, and the `should_terminate` signal the agent honours is never sent. A slow image
  pull is enough to trigger it. Fix in progress.
- **Shutdown can hang when the Kubernetes API stops answering (#463).** Described in the
  `0.1.2-rc.2` section; the fix is written and lands next.

## [0.1.2-rc.2] - 2026-07-30

> Respin of `0.1.2-rc.1` for one defect, found by hands-on validation of that
> candidate: a task declaring `on_failure_callback` as a **list** — the shape Airflow
> itself produces — had it dropped silently. Nothing else changed; every other item
> in the 0.1.2 line is described in the `0.1.2-rc.1` section below and was verified
> against that candidate.

### Fixed

- **`on_failure_callback` written as a list now runs (#470).** Airflow 3 normalises a
  task's callback attributes to a list, so `@task(on_failure_callback=[notify])` — and
  any DAG copied from a real Airflow deployment — carries `[fn]`, not `fn`. The runtime
  already accepted both shapes; the compiler's gate tested only `callable()`, so the
  list form produced no marker in `dag.json`, no warning, and no callback at run time.
  A bare callable was unaffected.
- **An *unsupported* callback written as a list is loudly rejected again (#470).** The
  same gate guards the compile-time refusal for `on_success_callback` and
  `on_retry_callback`. In list form they slipped past it and were dropped in silence —
  the precise outcome the refusal exists to prevent, and worse than the case it was
  written for, because the author was told nothing.

### Known issues

- **Shutdown can hang when the Kubernetes API stops answering (#463).** The 0.1.2
  line wires the buffered dispatch drain into shutdown (#133) so in-flight dispatches
  settle instead of leaking — but the wait is unbounded and the Kubernetes client
  carries no per-call timeout. An apiserver that accepts the connection and never
  answers pins a worker, and with it the drain, until the runtime kills the process.
  In that specific case shutdown is worse than in 0.1.1, where the drain never ran at
  all. The fix is written and held for the next release rather than folded in here,
  to keep this candidate a targeted respin. Workaround: none needed unless your
  apiserver hangs; the pod is SIGKILLed at the end of its termination grace period.
- **A task can be marked `dispatch_lost` while its pod is still `Running` (#461)** —
  carried over from rc.1, still under investigation.

### Corrected from the rc.1 notes

The `0.1.2-rc.1` section claims, under Fixed, that `on_failure_callback` runs for
"list-normalised callbacks". Unbound DAGs did work; **lists did not**. Tags are
immutable (ADR 0033), so that section stands as published — the claim it makes is true
of this candidate, not of rc.1.

## [0.1.2-rc.1] - 2026-07-30

> First release candidate of the **0.1.2** line — **native on-failure alerting** and a
> **Pro install that can no longer be misconfigured into silence**. A failed task now
> notifies on its own, without an Airflow callback in the loop; and the three Pro
> misconfigurations that used to produce a healthy-looking-but-broken deployment now
> fail loudly at `helm install` or at boot.

### ⚠️ Upgrade note for Pro operators

**`agentTLS.enabled: false` no longer yields a running deployment.** It was never a
plaintext deployment — the control plane marks itself as the Pro edition and every
secrets RPC to a task pod was rejected, so tasks queued and hung with no visible
cause. That failure is now surfaced where you can act on it:

- `helm install`/`upgrade` **refuses to render** with `agentTLS.enabled=false`.
- The control plane **refuses to boot** without `LEOFLOW_SERVER_GRPC_TLS_CERT`/`_KEY`
  when `LEOFLOW_UI_EDITION=pro`.

**If your values override `agentTLS.enabled` to `false`,** provision a cert before
upgrading — cert-manager `Certificate` + CA trust bundle, then set
`agentTLS.serverCertSecret` and `agentTLS.caConfigMap`. Step-by-step in
[`docs/pro-tls.md`](docs/pro-tls.md). Installs on the `agentTLS.enabled: true` default
(unchanged since 0.1.1) are unaffected. For a plaintext local loop, use the Lite dev
server (`leoflow dev lite`), which is not subject to the Pro guards.

### Added

- **Native on-failure alerting (#424).** A DAG declares its alert targets and the
  control plane notifies on final task failure from a Go notifier — no Airflow
  callback in the request path. Configuration, `dag.json` fields and the notifier
  ship together; see [`docs/alerting.md`](docs/alerting.md).
- **Airflow `on_failure_callback`, in-process on final failure (#424).** The familiar
  Airflow hook runs where Leoflow already knows the task reached its terminal state,
  so existing DAG code keeps working alongside native alerting.
- **Alerts dedup per failure episode (#431).** Retries within one failure episode
  produce one notification, not one per attempt.
- **Saturation-drop metric (#435).** Drops caused by a saturated alert path are now a
  metric you can alert on, not just a log line.

### Changed

- **The Pro edition refuses to boot without TLS on the agent gRPC channel (#281).**
  Booting looked healthy while every secrets RPC failed; it now fails at boot with the
  reason. See the upgrade note above.
- **The Helm chart fails at render on the silent Pro misconfigs** — `agentTLS.enabled`
  with an empty `caConfigMap` (#280), a `ReadWriteOnce` logs PVC under more than one
  replica whether static or HPA-driven (#282), and `agentTLS.enabled=false` on a chart
  that only ever deploys Pro (#459). Each fails in about a second with an actionable
  message instead of a `CrashLoopBackOff` or a `Multi-Attach` hang.

### Fixed

- **Buffered dispatch drains on shutdown, and `Close()` is no longer racy (#133).**
  In-flight dispatches are no longer dropped when the control plane stops.
- **The parser rejects an oversized literal task argument at compile time (#149)**,
  instead of failing later in the pod.
- **The parser captures `retries`, `retry_delay` and `execution_timeout` from
  operators (#434)** — previously dropped, so a task silently ran with defaults.
- **`on_failure_callback` runs for unbound DAGs and list-normalised callbacks (#424).**

### Security

- **grpc → v1.82.1**, clearing `GHSA-hrxh-6v49-42gf`.
- **`x/net` and `x/text` bumped**, clearing `CVE-2026-46600` and `CVE-2026-56852`.
- **Trivy filesystem scan now runs on pull requests (#437)**, not only on push and
  schedule, so a vulnerable dependency is caught before merge.
- Nine further dependency bumps in the minor-and-patch group.

### Known issues

- **A task can be marked `dispatch_lost` while its pod is still `Running` (#461).**
  Observed once in CI and not reproducible on rerun; the pod is dispatched and alive,
  but its agent never reports in, and the control plane fails the task at the dispatch
  threshold. In production this risks a false failure (and its alert) on work that may
  still be executing. Under investigation — please report occurrences with the run id.

## [0.1.1] - 2026-07-10

> **dbt-native orchestration.** A dbt project becomes a Leoflow DAG — one pod per
> model, no Cosmos at runtime, no Airflow in the parser — and develops **locally with
> zero config** against an embedded duckdb. This promotes `v0.1.1-rc.1` after a clean
> Lite (arm64) end-to-end soak, plus a Go 1.26.5 toolchain bump for a newly-disclosed
> standard-library advisory (below) — no functional change from the rc.

### Security

- **Go toolchain 1.26.4 → 1.26.5**, closing `GO-2026-4970` (root escape via symlink +
  trailing slash in the standard-library `os` package), disclosed after the rc was cut
  and reachable from the installer's download path.

### The 0.1.1 line, in brief

- **dbt projects as Leoflow DAGs (ADR 0042).** A dbt project compiles straight to
  Leoflow tasks from its `manifest.json`, in Go — one task per model/seed/snapshot/test,
  wired by dbt's own dependency graph.
- **Mix dbt with operators (ADR 0043)** via `dbt_group("name")`, and **multiple dbt
  projects, one per business domain (ADR 0044)** via a namespaced `dbt_groups` map.
- **Adapters:** Postgres, Snowflake, BigQuery, Databricks, and **duckdb** — mapped from a
  managed connection at runtime, so no warehouse credential is baked into the image.
- **Zero-config local dbt (Lite):** a dbt project with no connection and no
  `profiles.yml` just runs against an embedded duckdb, never touching your global
  `~/.dbt`; model edits hot-reload.

Per-rc detail is in the `0.1.1-rc.1` section below.

## [0.1.1-rc.1] - 2026-07-05

> First release candidate of the **0.1.1** line — **dbt-native orchestration**. A dbt
> project becomes a Leoflow DAG (one pod per model, no Cosmos at runtime, no Airflow in
> the parser), and — new this line — a dbt project develops **locally with zero config**.

### Added

- **dbt projects as Leoflow DAGs (ADR 0042).** A dbt project compiles straight to
  Leoflow tasks from its `manifest.json`, in Go — one task per model/seed/snapshot/test,
  wired by dbt's own dependency graph. No Cosmos at runtime, no Airflow in the parser.
- **Mix dbt with operators (ADR 0043).** `dbt_group("name")` embeds a dbt project as a
  namespaced task group inside a normal `dag.py`, wired around your operators/sensors;
  `granularity` (node/level/folder) is a pod-packing knob.
- **Multiple dbt projects, one per business domain (ADR 0044).** A `dbt_groups` map lets
  one DAG carry several domain projects (`sales__*`, `marketing__*`), namespaced so
  identically-named models never collide.
- **Adapters:** Postgres, Snowflake, BigQuery, Databricks (the official `dbt-databricks`
  adapter), and **duckdb** — Leoflow maps a managed connection to each adapter's profile
  at runtime, so no warehouse credential is baked into the image.
- **Zero-config local dbt (Lite).** `leoflow lite` + a dbt project with no connection and
  no `profiles.yml` just runs — against an embedded **duckdb** file, generated
  transparently at compile and run time, never touching your global `~/.dbt`. Model
  edits hot-reload (the manifest re-parses with the per-DAG venv's dbt).

### Fixed

- **Lite runs dbt end-to-end (subprocess):** the per-DAG venv's `bin` is now on the
  task's PATH (so a bare `dbt` resolves), the dbt `--project-dir` is absolute for local
  builds (so the project resolves from the task workdir), and the manifest parses with
  the venv's dbt — the three gaps that stopped `leoflow lite` from running a dbt DAG.
- **`leoflow compile` self-heals the extracted parser after a binary upgrade** (#239), so
  a new build's features (like dbt) never fail against a stale `~/.leoflow/pysrc`.

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

[Unreleased]: https://github.com/neochaotic/leoflow/compare/v0.1.2...HEAD
[0.1.2]: https://github.com/neochaotic/leoflow/compare/v0.1.2-rc.2...v0.1.2
[0.1.2-rc.2]: https://github.com/neochaotic/leoflow/compare/v0.1.2-rc.1...v0.1.2-rc.2
[0.1.2-rc.1]: https://github.com/neochaotic/leoflow/compare/v0.1.1...v0.1.2-rc.1
[0.1.1]: https://github.com/neochaotic/leoflow/compare/v0.1.1-rc.1...v0.1.1
[0.1.1-rc.1]: https://github.com/neochaotic/leoflow/compare/v0.1.0...v0.1.1-rc.1
[0.1.0]: https://github.com/neochaotic/leoflow/compare/v0.1.0-rc.4...v0.1.0
[0.1.0-rc.4]: https://github.com/neochaotic/leoflow/compare/v0.1.0-rc.3...v0.1.0-rc.4
[0.1.0-rc.3]: https://github.com/neochaotic/leoflow/compare/v0.1.0-rc.2...v0.1.0-rc.3
[0.1.0-rc.2]: https://github.com/neochaotic/leoflow/compare/v0.1.0-rc.1...v0.1.0-rc.2
[0.1.0-rc.1]: https://github.com/neochaotic/leoflow/releases/tag/v0.1.0-rc.1
