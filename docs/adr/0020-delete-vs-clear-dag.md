# ADR 0020: "Delete DAG" Clears History; Deregister Is Separate

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
