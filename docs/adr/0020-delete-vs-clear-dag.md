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
