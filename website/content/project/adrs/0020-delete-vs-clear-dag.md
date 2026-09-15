---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /adr/0020-delete-vs-clear-dag.html
# --- end AUTO redirect aliases ---
title: "ADR 0020: \"Delete DAG\" Clears History; Deregister Is Separate"
linkTitle: "0020 · \"Delete DAG\" Clears History; Deregister Is Separate"
weight: 200
description: "ADR 0020: \"Delete DAG\" Clears History; Deregister Is Separate"
---

**Status:** Accepted
**Date:** 2026-05-23
**Deciders:** Project founder

## Context

Airflow's UI "delete DAG" removes the DAG's DB records (runs, task instances,
history), but the DAG **reappears** because the dag processor re-scans the dags/
folder — the `.py` file is the source of truth. So "delete" is effectively
"clear history" for a DAG that keeps existing.

Leoflow is GitOps: the pushed artifact (dag.json in the DB) is the source of
truth and there is **no auto-reload** (a deleted DAG does not come back). Mapping
Airflow's effectively-non-destructive trash button to a permanent cascade delete
is surprising and lossy — a user clicking the trash to "clear" instead destroys
the DAG.

## Decision

Split the two operations Airflow conflates:

1. **Clear history** — the default destructive UI action (the trash icon, which
   the embedded SPA issues as `DELETE /api/v2/dags/{dag_id}`): delete the DAG's
   `dag_runs` (cascading `task_instances` and `xcom_index`), but **keep the
   `dags` and `dag_versions` rows registered**. Matches Airflow's effective UX
   and is the natural way to clear zombie/stuck runs from the UI.

2. **Deregister** — explicit, rare: remove the DAG artifact entirely (the prior
   cascade hard-delete). Exposed as `DELETE /api/v2/dags/{dag_id}?deregister=true`
   for the CLI / custom UI; re-push to restore.

## Consequences

- The SPA trash button now clears history instead of destroying the artifact; the
  DAG remains after "delete" (refetch shows it with no runs), mirroring Airflow.
- Clearing a DAG's history is a first-class, non-destructive operation — useful
  for stuck/zombie runs (see #46/#50).
- Deregister stays available but must be asked for explicitly (the `deregister`
  flag), so accidental artifact loss is unlikely.
- A future custom UI (ADR 0018) can present "Clear" and "Deregister" as two
  distinct, clearly-labeled actions.

## Amendment (2026-05-24): clearing a task re-binds the run to the current version

Clearing **task instances** (re-run, distinct from "clear history" above) is the
**single mutability exception** to the otherwise-immutable run↔version pin
(ADR 0003: DAGs are immutable artifacts). Decided by the project founder.

- A DAG run is created pinned to a `dag_version`; everything **within a version**
  stays reproducible/idempotent (same `dag.json` + same image).
- **`clear` (with reset) re-binds the run to the DAG's current registered
  version**, so a re-run after a code/yaml fix executes against the **newest
  image + config** — not the version the run was pinned to. The update source
  differs by environment but the rule is identical: in **dev** the current version
  is the **last hot-reload** (`leoflow dev` registers one per save); in
  **production** it is the **last deploy** (CI push). When the version is
  unchanged, clear is a plain state reset.
- This matches Airflow, whose clear re-runs against the current DAG code, and is
  what makes "fix the DAG, clear the failed task, watch it pass" work.
- **Drift guard:** if the current version removed or renamed a task that is being
  cleared, that task no longer exists in the spec — the re-run simply has nothing
  to schedule for it (no crash). Clearing tasks that still exist is unaffected.
- Implemented as `ResetDagRunToVersion` (sets the run's `dag_version_id` to the
  DAG's `current_version_id` on reset). Tasks not cleared keep their results; the
  per-run staging volume is re-attached by its deterministic name (ADR 0022), so a
  clear+re-run reuses upstream staged data.

## Amendment (2026-09-15): the version choice becomes a request flag defaulting to Airflow's, and the Airflow claim above is withdrawn

Approved by the project founder.

### What was wrong

The 2026-05-24 amendment justifies the unconditional re-bind with:

> This matches Airflow, whose clear re-runs against the current DAG code

That was true of Airflow 2. It is **not** true of Airflow 3.x, the stated
compatibility target. Verified against Airflow 3.2.1: `clear_task_instances`
takes `run_on_latest_version: bool = False`, defaulting to the run's **pinned**
version and using the latest only when explicitly asked
(`airflow/models/taskinstance.py`, `run_on_latest_version` parameter and the
`get_dag_for_run` / `get_latest_version_of_dag` branch; the API body field is
documented "(Experimental)").

So leoflow did the **opposite of Airflow's default** while citing Airflow as the
reason. The sentence is withdrawn. The decision it justified is kept, on its own
merits, and is now expressible either way.

### What changes

`clear` gains `run_on_latest_version`, matching Airflow's name and semantics:

- `true` — re-bind the run to the DAG's current registered version. The
  behaviour described in the 2026-05-24 amendment, unchanged.
- `false` — re-open the run but keep the version it was created with, so the
  re-run executes the **image that produced the original attempt**.

This also separates two decisions that were one boolean. Re-opening a run
(`reset_dag_runs`) and choosing its version are independent; folding the second
into the first meant there was no way to re-run last week's task as it was last
week, and no way to re-open a run without also moving it forward a version.

Implemented as `ReopenDagRunKeepingVersion` alongside `ResetDagRunToVersion`.

### The default follows Airflow: `false`

**`run_on_latest_version` defaults to `false`**, as in Airflow. Decided by the
project founder. A clear therefore **reproduces the attempt it is clearing**, on
the image that produced it.

This is a **behaviour change** from the 2026-05-24 amendment, which re-bound
unconditionally. What it changes in practice:

- *Clearing a task to re-run it as it was* — a week-old failure, a flake, an
  infra-failed attempt — now does exactly that, which was impossible before.
- *Clearing a task to test a fix* no longer picks the fix up on its own. Two ways
  to do it, both explicit: **trigger a new run** (always the current version), or
  pass `run_on_latest_version=true`. The first is what Airflow users already do;
  the second is the old behaviour, still one field away.

The 2026-05-24 amendment described "fix the DAG, clear the failed task, watch it
pass" as what the re-bind buys. That loop still exists — it now asks for the
newest version out loud instead of assuming it. Under `leoflow dev`, where every
save registers a version, this is the difference worth knowing: after editing,
clearing an old run re-runs the **old** code unless you say otherwise.

### Consequence for run↔version immutability

The 2026-05-24 amendment called the re-bind "the single mutability exception" to
ADR 0003. The exception is now **opt-in**: by default a run keeps the version it
was created with, and mutability happens only when a caller asks for it. That is
a stronger position than the one this ADR originally took, and it is Airflow's.
