---
title: "ADR 0043: TaskGroup as a first-class construct with split/fused execution"
linkTitle: 0043 · TaskGroup as a first-class construct with split/fused execution
weight: 430
description: "ADR 0043: TaskGroup as a first-class construct with split/fused execution"
---

**Status:** Accepted — implementation partial. The dbt-domain split ships (`internal/dbt/`, ADR 0044); the **generic** `TaskGroup` construct in a `dag.py` remains deliberately rejected by the parser (it is in `test_unsupported_constructs_error_clearly`), so an author cannot yet use TaskGroup outside dbt. The decision — TaskGroup as a first-class construct with split/fused execution — is accepted; the general-purpose implementation is deferred. Do not read "Accepted" as "TaskGroup is available."
**Date:** 2026-06-25
**Companions:** ADR 0003 (DAG as immutable artifact), ADR 0024 (parser shim — currently rejects TaskGroup), ADR 0040 (operator support), ADR 0042 (dbt support), the editions split (Lite/Pro)

## Context

ADR 0042 ships dbt as **"a dbt project = the whole DAG"**: the dbt path has no
`dag.py`, the topology comes entirely from the manifest. That cannot mix dbt models
with the Airflow operators/sensors ADR 0040 already supports — there is no Python DAG
to attach other tasks to. Cosmos solves this with `DbtTaskGroup`: a **TaskGroup**
dropped into a normal DAG alongside other tasks.

Two needs converge here:
1. **Mix operators + dbt in one DAG** — the Cosmos `DbtTaskGroup` shape.
2. **A pod-packing knob** — run a group's tasks as one pod, or split into many.

Both point at making **TaskGroup first-class** (today the shim rejects it), with the
dbt group as a specialized TaskGroup.

## Decision

### 1. TaskGroup is first-class; dbt is a specialized TaskGroup

A group is a named set of tasks with a single entry (its roots) and exit (its leaves)
for wiring. The Airflow UI already understands task groups. A dbt group's members are
generated from the manifest (ADR 0042); a generic group's members are authored tasks.

### 2. Execution mode per group: `split` (default) vs `fused`

| mode | meaning |
|---|---|
| **`split`** | each task in the group is its own pod — Leoflow's scheduler parallelizes across pods, full per-task isolation/retry. The default; preserves pod-per-task. |
| **`fused`** | the whole group runs in **one pod**, whose **in-pod engine parallelizes the group's sub-DAG** (not merely sequential). Fewer pod startups; coarser failure domain; no per-task grid granularity. |

For dbt, `split` = node granularity (pod per model); `fused` = one `dbt build --select
<members>` invocation; `level`/`folder` are middle grounds (fused sub-groups).

### 3. Fused parallelism is delegated to an in-pod engine

A fused group is **not** sequential — its in-pod engine runs independent members
concurrently while respecting the internal DAG. For **dbt**, that engine is dbt's own
scheduler (`threads`), which we get for free. A **generic-operator** fused group would
need a Leoflow **in-pod runner** (a goroutine pool executing the sub-DAG); that is
deferred (see Scope). So we implement fused **for dbt first** and design the shape so a
generic in-pod runner can plug in later.

The concurrency degree comes from the engine's config: for dbt, `threads` (from the
connection/profile); generally, a future `concurrency` on the group.

### 4. Config location: structure in `dag.py`, execution mode in `leoflow.yaml`

- **Group structure** (which tasks, the edges) lives in **`dag.py`** — it is topology.
- **Execution mode** (split/fused, concurrency) lives in **`leoflow.yaml`** — it is a
  deployment concern, the same rationale as staging/resources ("not an Airflow DAG
  attribute"). It is overlaid onto dag.json at compile, like the existing `tasks:`
  overrides.

Why leoflow.yaml for the mode: it must be able to **vary Lite vs Pro from one DAG** —
fuse in Lite for a fast dev loop, split in Pro for isolation — which a value baked into
dag.py could not. Default is `split`.

```yaml
# leoflow.yaml
groups:
  default: { execution: split }
  transform: { execution: fused }     # override per group
dbt_groups:
  analytics:
    project: ./analytics
    granularity: level                # = the group's execution mode for dbt
    connection: warehouse_pg          # managed connection, not a baked profiles.yml
    threads: 4                        # in-pod parallelism for the fused engine
```

### 5. Authoring: three surfaces, each concern in its place

The opposite of Cosmos's everything-in-verbose-Python (`ProjectConfig` +
`ProfileConfig` + `ExecutionConfig` + `RenderConfig`):

- **Models** → the dbt project (`models/*.sql`), authored in dbt tooling. Untouched.
- **Orchestration** → `dag.py`, where operators and the dbt group are wired with `>>`.
  The dbt group is a thin handle whose heavy config lives in leoflow.yaml.
- **Packing + connection** → `leoflow.yaml`.

```python
# dag.py — orchestration only
from leoflow import dbt_group
with DAG("sales", schedule="@daily"):
    extract = HttpOperator(task_id="extract", ...)
    transform = dbt_group("analytics")    # config in leoflow.yaml
    notify = SlackNotifier(task_id="notify", ...)
    extract >> transform >> notify
```

At compile, the shim captures `extract`/`notify` and the `dbt_group` placeholder; the
Go compiler renders the manifest into namespaced tasks (`analytics.stg`, …) and wires
the external edges: every upstream of the group → the dbt **roots**, the dbt **leaves**
→ every downstream. One dag.json carries both, quotient-acyclicity still enforced.

## Evidence (spike)

**Fused parallelism is real, not sequential.** Three independent slow dbt models
(`pg_sleep(3)`) under one `dbt build`:
- `--threads 1` → **14s** (a, b, c sequential)
- `--threads 4` → **8s** (a, b, c concurrent; the ~6s delta is the parallel wave)
- the dependent `mart` waited for all three (internal DAG respected) and materialized.

So one pod (one dbt invocation) parallelizes the group via its own engine. This is the
core premise of `fused`.

## Scope (this ADR, dbt-first)

In: TaskGroup as a concept; `execution: split|fused` config in leoflow.yaml; **fused
for dbt** (dbt threads as the in-pod engine); the `dbt_group()` authoring handle;
edge-wiring + task_id namespacing under the group.

Deferred: a **generic in-pod runner** for fusing arbitrary operators (real semantic
questions — shared image, sequential vs parallel, failure domain); split of arbitrary
(non-dbt) groups beyond what pod-per-task already gives.

## Open questions

- The shim primitive for `dbt_group()` / `with TaskGroup(...)` (the parser rejects
  groups today — ADR 0024).
- Edge-wiring semantics when a group is empty, or has multiple roots/leaves.
- Surfacing per-member status from a fused pod (dbt `run_results.json`) so the UI grid
  stays observable per model even when packed into one pod (ties to ADR 0042 #397).
- Real-pod fused validation (one pod runs `dbt build --select group` with threads).

## Consequences

- Operators + dbt coexist in one DAG (the Cosmos `DbtTaskGroup` capability) without
  Cosmos and without new runtime dependencies.
- A single pod-packing knob (`split`/`fused`) spans dbt today and generic groups later,
  with parallelism preserved inside a fused pod.
- New surface: TaskGroup in the shim + dag.json, the leoflow.yaml `groups:`/`dbt_groups:`
  config, edge-wiring, and (later) a generic in-pod runner.
