# ADR 0022: Ephemeral Per-DAG-Run Staging Volume

**Status:** Accepted
**Date:** 2026-05-23
**Deciders:** Project founder

## Context

XCom is for passing small typed values between tasks (≤256KB, Redis-backed); it
is not data storage. File-heavy pipelines need to stage large intermediate data
between a run's tasks (extract writes GBs, transform reads them). The options
considered (see issue #55 and the chat discussion):

- A **global `/tmp` volume** mounted on every pod — rejected: shared mutable
  state across all runs/DAGs, no isolation, no GC story, contradicts the
  stateless-pod model (and ADR's "no shared filesystem").
- **Object storage** (S3/GCS) via a cloud connection — the right answer for
  durable / cross-run / cross-DAG data, but needs a cloud account + the
  Connections runtime wiring (ADR 0021 / #54), which we do not have yet. MinIO is
  no longer a comfortable bundled default.
- An **ephemeral, per-DAG-run shared volume**, managed by Leoflow — fills the gap
  for in-run scratch with no cloud dependency.

How Airflow solves this in 3.x: it offers `airflow.io.ObjectStoragePath` (fsspec
+ a Connection) for object storage, and a custom XCom backend to offload large
values — but it has **no first-class ephemeral shared volume**; PVCs are wired
manually via pod templates with no managed lifecycle or isolation. So a managed
per-run volume is a Leoflow value-add.

A hard requirement surfaced in design: **a re-run must not lose the temporary
data** produced by already-successful upstream tasks, or the re-executed task
fails. The volume's lifecycle must therefore be tied to the **DAG run**, not to a
task pod, and survive retries and clear+re-run.

## Decision

Add an **opt-in, Leoflow-managed ReadWriteMany volume scoped to each DAG run**.

- **Opt-in, declared in `leoflow.yaml`** (compiled into the immutable `dag.json`):
  ```yaml
  staging:
    enabled: true
    size: 5Gi
    storage_class: ""   # empty = the cluster's default RWX StorageClass
  ```
- **One PVC per run, deterministically named** `leoflow-staging-<dag_id>-<run_id>`,
  mounted at `/staging` in every task pod of that run, exposed as
  `LEOFLOW_STAGING_DIR`. The deterministic name is what lets a clear+re-run
  **re-attach the same PVC**.
- **Lifecycle tied to the DAG run, not the pod:**
  - Created when the run leaves `queued`.
  - **Persists across task retries and clear+re-run** — a re-run re-attaches the
    same PVC, so upstream outputs are still present and the task does not fail for
    missing data.
  - Garbage-collected only after the run reaches a terminal state **and** a
    **24-hour TTL** elapses (so a re-run shortly after a failure still finds the
    data). A reconciler sweeps orphaned PVCs, mirroring the existing pod
    reconciler.
- **Requires an RWX StorageClass.** If `staging.enabled` is set and the cluster
  has no RWX class, fail fast with a clear error at dispatch — never silently
  degrade.
- **Isolation, not transactions.** "Atomic per DAG" means the volume is a single
  isolated unit per run; it is a shared filesystem, not ACID. Tasks are
  responsible for atomic writes (write-temp-then-rename). This is documented.

Default is **off**: XCom for small handoffs, object storage for durable data.

## Consequences

- File-heavy pipelines get real in-run staging with no cloud account, isolated
  per run, with a clear managed lifecycle — closing the gap Airflow leaves to
  manual pod-template wiring.
- Re-runs are safe: the per-run PVC and the 24h post-terminal TTL guarantee
  upstream temp data survives retries and clears.
- New operational surface: PVC provisioning latency/cost, a GC reconciler, and an
  RWX StorageClass dependency (not universal). Co-locating a run's pods may
  constrain K8s scheduling.
- Two data paths coexist by design: ephemeral per-run (this ADR) and durable
  object storage (future, via Connections). The volume is not for cross-run or
  cross-DAG data.

## Alternatives considered

- **Global `/tmp` RWX volume.** Rejected: shared mutable state, no isolation, no
  GC, contradicts stateless pods.
- **Object storage only (S3/GCS via connection).** The durable answer and still
  recommended for persistent/cross-run data, but requires a cloud account and the
  Connections runtime wiring (#54); does not cover the no-cloud, in-run scratch
  case. Tracked separately.
- **Pod-template PVC (Airflow style).** Rejected as the default: no managed
  lifecycle, isolation, or re-run safety — exactly the gaps this ADR closes.
- **Delete the volume on run terminal (no TTL).** Rejected: a clear+re-run right
  after a failure would lose the data; the 24h TTL is the safety margin.
