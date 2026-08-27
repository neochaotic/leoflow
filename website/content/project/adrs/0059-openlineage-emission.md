---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /adr/0059-openlineage-emission.html
# --- end AUTO redirect aliases ---
title: "ADR 0059: OpenLineage emission from the Go control plane → OpenMetadata"
linkTitle: "0059 · OpenLineage emission → OpenMetadata"
weight: 590
description: "ADR 0059: OpenLineage emission from the Go control plane → OpenMetadata"
---

**Status:** Accepted — implementation scheduled after v0.4.1 (2026-08-27); v1a lifecycle events + v1b dbt-first dataset lineage, dbt slice first
**Date:** 2026-08-26
**Relates:** ADR 0042 (dbt native rendering — the manifest is the lineage source), ADR 0043 (taskgroup split/fused execution — dbt node identity), ADR 0051 (separate orchestration/execution state machines — the seam an emit hook rides), ADR 0040 (airflow operator support — the `openlineage.sqlparser` transitive import), ADR 0035 (audit surface — the reuse-vs-separate decision), ADR 0056 (task-log object sink — the "opt-in external transport, default off" precedent)
**Issues:** #760 (discovery: OpenLineage emission from the Go control plane → OpenMetadata integration)

## Context

Leoflow has **no data-lineage or catalog integration today**. Verified: zero
references to `openlineage`, `OpenLineage`, `OpenMetadata`, or `marquez` anywhere
in `internal/`, `cmd/`, or `pkg/` Go source. The only occurrence of the string
in the whole repository is a *transitive Python import* that the Airflow
compat-shim must stub — `openlineage.sqlparser`, pulled in by
`apache-airflow-providers-postgres`'s `hooks/postgres.py`
(`website/content/project/planning/airflow-connector-compatibility.md:261`,
`:318`). That is a dependency to be *removed* during shrinkage, not a working
integration.

### This does not come for free from Airflow

Airflow's OpenMetadata lineage is **scheduler-side**: the `[lineage] backend`
key in `airflow.cfg`, fired by Airflow's own scheduler, or the OpenMetadata
"Airflow Managed APIs" plugin loaded into the Airflow webserver. Leoflow runs
**neither** — it is a Go control plane with pod-per-task SDK execution and no
`airflow.cfg`, no Python scheduler, no webserver. That hook never fires. Any
lineage emission is **net-new, deliberate, Go-side work**.

### Why OpenLineage rather than an OpenMetadata SDK

OpenLineage is the vendor-neutral wire format that OpenMetadata, Marquez, and
DataHub all ingest. Emitting OpenLineage `RunEvent`s over the standard HTTP
transport turns "OpenMetadata integration" into "point Leoflow's OpenLineage
endpoint at OpenMetadata's OL ingestion URL" — and the same emitter serves
Marquez and DataHub users at zero extra cost. Binding to an OpenMetadata Go SDK
would couple us to one catalog and one release cadence for no compatibility gain.
This is consistent with the enterprise/governance direction (cataloging + lineage
as a headline capability) without picking a catalog winner for our users.

### Where lifecycle transitions actually happen (the emit-site survey)

The control plane's run/task lifecycle is driven by the scheduler's per-tick
`advance`, which is the single point that already holds the full run picture —
the task graph (`RunState.Tasks`), current states (`RunState.States`), attempt
counts, and per-task timing (`RunState.EndedAt`, `RetryDelaySeconds`,
`RescheduleAt`) — see the `RunState` struct at
`internal/scheduler/scheduler.go:55-95`.

- **RUN start** is the first write in `advance`:
  `SetRunState(ctx, run.RunID, DagRunStateRunning)`
  (`internal/scheduler/scheduler.go:780`), on first sight of a queued run.
- **RUN terminal** (Success/Failed) is `FinalizeRun(run)` + `SetRunState`
  (`internal/scheduler/scheduler.go:810-816`), immediately followed by
  `maybeAlertFailure`.
- **TASK transitions** are applied in `applyPlanned`
  (`internal/scheduler/scheduler.go:946`) and batched through `flushTransitions`
  (`internal/scheduler/scheduler.go:996`), which is *already* the site of the
  existing observer hook: `s.recorder.RecordTaskTransition(from, to, dagID)` at
  `internal/scheduler/scheduler.go:1005`, `:1030`, `:1055`, `:1068`, `:1189`.

The `Recorder` interface is declared at `internal/scheduler/scheduler.go:228`
(`RecordTaskTransition(from, to, dagID string)`), implemented by the Prometheus
`Metrics` recorder (`internal/observability/metrics.go:278`), and wired once at
`cmd/leoflow-server/main.go:1136` (`sched.SetRecorder(metrics)`). **This observer
seam is the cleanest emit site** — every legal transition already funnels through
it, it sees `run.DagID` and the from/to states, and it is a `Set*`-injected
optional dependency exactly like the alerter and dispatcher.

The authoritative *per-task* terminal state originates from the agent report RPC
(`mapState`, `internal/agentrpc/server.go:601-608`), but that path sees one task
in isolation, not the run graph. The scheduler tick is where the isolated report
becomes a graph-aware transition — so the graph-shaped START/COMPLETE/FAIL events
belong at the scheduler seam, with the report RPC providing exit-code/reason
detail that the scheduler already reads back as `RunState.EndedAt` /
last-failure-kind.

### The existing audit surface is not a lifecycle event stream

The audit system is a Postgres `audit_log` table
(`migrations/005_xcom_index_and_audit.up.sql`) written synchronously on
**user/compliance actions** — `dag.create`, `dag_run.trigger`, `auth.login`,
secret-scope warnings — via `Repository.RecordTaskActionAudit` /
`RecordAuthEvent` / `RecordSecretScopeWarning` → `q.CreateAuditLog`
(`internal/storage/repository.go:605`, `:676`, `:728`, `:973`). It is
actor-keyed (`user_id`), not lifecycle-keyed, and has no run-start/task-running
rows. It is the wrong pipe for OL: OL events are high-frequency machine
lifecycle facts destined for an *external* catalog, not tenant-scoped compliance
rows for the UI's audit view. OL emission should be a **separate transport**
(the OpenLineage HTTP client), attached at the scheduler `Recorder` seam — not a
new `audit_log` writer.

## Decision

Emit **OpenLineage `RunEvent`s from the Go control plane**, at the scheduler's
existing transition-observer seam, behind an opt-in server-config block that is
**default-off**. Ship it in facet tiers, dbt-first for real dataset lineage.

### D1 — Emit site: a second observer on the scheduler seam

Introduce a `LineageEmitter` interface in `internal/scheduler` (interfaces live
with their consumer, per house convention) with run- and task-granular methods,
and a `sched.SetLineageEmitter(...)` setter mirroring `SetRecorder` /
`SetAlerter`. Call it from the same three points the `Recorder` already fires:
run START at `scheduler.go:780`, run COMPLETE/FAIL at `:810-816`, and per-task
transitions in `flushTransitions`/`applyPlanned`. A nil emitter is a no-op (the
default), so the OFF path is byte-for-byte today's behavior. The concrete
implementation lives in a new `internal/lineage` package and is wired at
`cmd/leoflow-server/main.go` next to `SetRecorder`.

Rationale: this seam already sees every legal transition with the run's DagID and
graph in hand, is already proven as an optional injected observer, and keeps all
user-code-free control-plane rules intact (ADR 0048) — OL events are derived from
control-plane state, never from user code.

### D2 — v1 facet scope: run/job first, dataset lineage deferred (except dbt)

OpenLineage facets split cleanly by cost against what the control plane already
knows:

**Cheap now (Tier 1 — the v1 core):**
- **Run facet** — `runId` (UUID from `run.RunID`), `parentRun` (the DAG run for a
  task-level event). Available directly.
- **Job facet** — namespace + job name from `run.DagID` / task ID. Available
  directly.
- **`nominalTime` facet** — logical/scheduled interval; the scheduler already
  computes logical times (`createScheduledRun`, `scheduler.go:760`). Available.
- **`errorMessage` facet** on FAIL — the classified failure reason already
  carried on the durable outcome (`internal/taskoutcome/record.go`,
  `FailedBecause`/`FailedWith`) and surfaced to the scheduler. Available.

**Hard (Tier 3 — deferred, honestly):**
- **`dataSource` / dataset `inputs` + `outputs` facets** — real table/column
  lineage. This is the valuable facet **and** the expensive one. For a generic
  bash/python task the control plane has *no idea* what tables it read or wrote —
  `TaskSpec` (`internal/domain/dag.go:132-179`) carries `DependsOn`,
  `OperatorClass`, `OperatorArgs`, `Entrypoint`, and XCom edges, but **no dataset
  fields**. Deriving datasets without user annotation would require SQL parsing
  of task output, which the Go control plane deliberately does not do (no user
  code in the control plane, ADR 0048). Generic dataset lineage is **out of scope
  for v1** and should not be promised.

**The exception that makes v1 worth shipping — dbt (Tier 2):** see D3.

### D3 — dbt-first dataset lineage is the highest-value, lowest-effort slice

Leoflow already renders a dbt `manifest.json` into node-level tasks
(`internal/dbt/render.go`, ADR 0042/0043). The manifest **is** a dataset lineage
graph — but our current parser reads only the subset needed to build the task
DAG: `resource_type`, `name`, `fqn`, `config.materialized`, and
`depends_on.nodes` (`internal/dbt/render.go:66-83`). It does **not** read the
physical-relation fields OL needs — `database`, `schema`, `alias`/`relation_name`,
and `columns`. So dbt dataset lineage is real but not free: it requires

1. **extending `manifestNode`** (`internal/dbt/render.go:71`) to capture
   `database` / `schema` / `alias` (the physical `db.schema.table` that becomes an
   OL dataset name) and optionally `columns` (for column-level facets), and
2. **carrying the derived input/output datasets forward**, because rendering
   happens at **compile time** (`dbt.Compile`, `internal/dbt/compile.go:38`) and
   produces bash `TaskSpec`s in `dag.json`; the manifest is **not retained in the
   control plane at run time**. Either (a) stamp per-task lineage facets into a
   new optional `TaskSpec` field at compile time (survives in the immutable
   `dag.json`, ADR: DAGs are immutable artifacts), or (b) re-read
   `manifest.json` / `run_results.json` from run staging. **Option (a) is
   preferred** — it keeps the run-time control plane free of dbt artifacts and
   fits the "compile once, immutable" model.

Because dbt is *already* our node graph, mapping each node's parents
(`depends_on`, resolved through ephemerals in `taskParents`,
`internal/dbt/render.go:93`) to OL dataset input/output edges is a
manifest-shape transform, not a lineage inference problem. This is the slice that
delivers *actual table-level lineage into OpenMetadata* for the least work, and
it should be v1's headline. Column-level lineage is a follow-on once
`manifestNode.columns` is captured.

### D4 — Config surface: opt-in, default off, separate transport

Add a `lineage` section to `ServerConfig` (`internal/config/server.go:17`),
alongside `Observability` and `Logs`, following the object-log sink precedent
(ADR 0056, keyless-first). Proposed keys:

```yaml
lineage:
  enabled: false                 # master switch, default OFF
  transport: http                # http | console(debug) — no others in v1
  url: ""                        # OpenLineage endpoint, e.g. http://openmetadata:8080/api/v1/openlineage/api/v1/lineage
  api_key: ""                    # bearer/API-key auth; empty = no auth
  namespace: "leoflow"           # OL job namespace
  emit_dbt_datasets: true        # Tier-2 dbt dataset lineage (no-op if enabled=false)
  timeout: 5s                    # per-emit HTTP timeout; emit is best-effort, never blocks the tick
```

Emission is **best-effort and non-blocking**, exactly like `maybeAlertFailure`
(`scheduler.go:860`): a slow or dead catalog must never stall the scheduler tick
or fail a run. Emit in a bounded worker (semaphore, drop-with-metric on
saturation) reusing the alerter's proven pattern. Do **not** reuse the
`audit_log` table (D-Context) — OL is a separate external transport.

### D5 — Non-dbt operators: out of scope for v1

For Airflow provider operators/sensors (ADR 0040), the Python providers *do* pull
`openlineage.sqlparser` transitively — but that runs **inside the user task pod's
Python runtime**, not in the Go control plane, and the compat-shim's stated
direction is to **drop** that import during shrinkage
(`airflow-connector-compatibility.md:361`, "4.5× shrinkage (drop IAM, dialects,
lineage, ...)"). Harvesting operator-level lineage would mean either (a) running
OpenLineage's Python extractors in-pod and reporting facets back over the agent
gRPC seam, or (b) SQL-parsing in Go. Both are large, and (a) reverses a shrinkage
decision. **Deferred.** v1 gives generic operators run/job/nominalTime/error
lifecycle events only — no datasets.

### D6 — Positioning: generic OpenLineage emitter, OM-documented

Ship a **generic OpenLineage emitter**, not an OpenMetadata-specific connector.
Provide an **OpenMetadata setup guide** in the docs (the OL ingestion URL, the
API-key wiring, screenshots of dbt lineage landing in OM) so the enterprise
"OpenMetadata integration" story is concrete — while the same emitter serves
Marquez and DataHub. First-class OM coupling (OM Go SDK, OM-specific entity
push) buys nothing OL doesn't already give and forfeits the vendor-neutral win.

## Effort estimate (rough, per slice)

| Slice | Scope | Effort |
|---|---|---|
| **v1a — lifecycle events** | `LineageEmitter` seam + `internal/lineage` HTTP client + run/job/nominalTime/errorMessage facets + config block + best-effort worker + tests | **~1 week** |
| **v1b — dbt datasets** (headline) | extend `manifestNode` (db/schema/alias), derive input/output datasets at compile, stamp into `TaskSpec`, emit dataset facets for dbt tasks + tests + OM setup doc | **~1 week** |
| v2 — column-level dbt | capture `manifestNode.columns`, `columnLineage` facet | ~3–4 days |
| v3 — operator lineage | in-pod extractor → gRPC facet report (reverses shrinkage) | **large / separate ADR** |

Recommended v1 = **v1a + v1b**. v1a alone is a lifecycle-only tease; v1b is what
puts real lineage on the OpenMetadata graph and justifies the feature.

## Alternatives

### A1 — Decline / defer (the honest do-nothing)

Ship nothing; revisit when a paying enterprise names lineage as a gating
requirement. **Cost:** the enterprise/governance pitch stays weaker than
Airflow-on-OpenMetadata, which *does* have a lineage story out of the box. **When
this wins:** if near-term roadmap capacity is better spent on HTTPS/SSO/stability
and no customer is blocked on lineage today. This is a legitimate outcome of this
ADR — the decision is "build v1a+v1b" vs "defer", and the maintainer owns it.

### A2 — Re-implement Airflow's `airflow.cfg [lineage] backend` (non-option)

Explicitly rejected. Leoflow runs no `airflow.cfg`, no Airflow scheduler, and no
webserver, so the hook has nowhere to fire (see Context). Emulating it would mean
standing up an Airflow-shaped lineage backend the rest of the system does not
use — pure compatibility theater. Leoflow's answer is OpenLineage-native from the
Go side.

### A3 — Bind to an OpenMetadata Go SDK / push OM entities directly

Rejected (D6). Couples us to one catalog and its release cadence, forfeits
Marquez/DataHub for free, and gains no compatibility over the OL wire format that
OM already ingests.

### A4 — Emit from the agent (per-pod) instead of the control plane

Rejected. The agent sees one task in isolation with no graph, no nominalTime, and
no run-terminal view; it would have to reconstruct what the scheduler already
holds (`RunState`), and it would put emission on the user-code side of the seam.
The scheduler observer seam (D1) already has the full picture.

## Consequences

- **Positive:** a vendor-neutral lineage story (OpenMetadata / Marquez / DataHub)
  with real dbt table-level lineage; strengthens the enterprise/governance pitch;
  reuses a proven optional-observer seam with a byte-for-byte OFF default; no user
  code enters the control plane.
- **Negative / cost:** dbt dataset lineage requires extending the manifest parser
  and adding a `TaskSpec` lineage field (a compile-artifact schema addition —
  additive, but it touches the immutable `dag.json` shape); generic-operator
  lineage is genuinely out of reach for v1 and will disappoint anyone expecting
  Airflow-provider parity; a new outbound HTTP dependency (best-effort, but one
  more thing to observe).
- **Neutral:** the `audit_log` table is untouched; OL rides its own transport.

## Non-goals

- Re-implementing `airflow.cfg [lineage]` (A2).
- Generic (non-dbt) dataset/column lineage in v1 (D2, D5).
- Running OpenLineage's Python SQL extractors in the pod (D5, v3).
- Any OpenMetadata-specific entity model beyond the OL wire format (D6).
