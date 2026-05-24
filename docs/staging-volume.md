# Ephemeral per-DAG-run staging volume

Status: **design** (ADR 0022, issue #55). This document is the visual/how-to
companion to the ADR.

## What it is

An opt-in, Leoflow-managed **ReadWriteMany (RWX) volume scoped to a single DAG
run**, mounted at `/staging` in every task pod of that run. It is the place for
**large intermediate data** between a run's tasks — the gap XCom (≤256KB) and
object storage (durable, cross-run) don't fill.

- **Opt-in** via `leoflow.yaml` → compiled into the immutable `dag.json`.
- **Atomic/isolated per run**: one PVC per run, deterministic name; runs and DAGs
  never collide.
- **Re-run safe**: the PVC's lifecycle is tied to the *run*, not the pod — it
  survives retries and clear+re-run, so upstream outputs are still there.
- **GC'd** after the run is terminal **plus a 24h TTL**.

It is **not** durable, cross-run, or cross-DAG storage — use object storage
(S3/GCS via a Connection) for that.

## Configuration

```yaml
# leoflow.yaml
staging:
  enabled: true
  size: 5Gi
  storage_class: ""   # empty = the cluster's default RWX StorageClass
```

Tasks read/write under `$LEOFLOW_STAGING_DIR` (= `/staging`), shared across the
run's tasks.

## Lifecycle

```mermaid
flowchart TD
    A["leoflow.yaml staging.enabled"] -->|leoflow compile| B["dag.json (immutable)"]
    B -->|push| C["Control plane"]
    C -->|run leaves 'queued'| D["Ensure PVC<br/>leoflow-staging-&lt;dag&gt;-&lt;run&gt;<br/>(RWX, idempotent)"]
    D --> E["Task pods of the run<br/>mount it at /staging<br/>(LEOFLOW_STAGING_DIR)"]
    E --> F["Tasks share files within the run"]
    F -->|task fails → clear + re-run| G["Re-attach the SAME PVC<br/>(deterministic name)<br/>→ upstream data still present"]
    G --> E
    F -->|run terminal| H["Mark run done"]
    H -->|+ 24h TTL| I["GC reconciler deletes the PVC<br/>(sweeps orphans too)"]
```

### Why the run, not the pod

```
Task pod lifetime:   [extract]   [transform]   [load]      (each pod is ephemeral)
PVC lifetime:        |-------------------------------------------------| run + 24h
                     ^create on run start                  ^GC after terminal+TTL
```

A clear+re-run reuses the deterministically-named PVC, so data written by
already-successful upstream tasks is not lost — the re-executed task finds its
inputs and does not fail. The 24h TTL after a terminal state is the safety margin
for a re-run shortly after a failure.

## Requirements & failure mode

- A cluster **RWX StorageClass** (NFS / EFS / CephFS / Filestore). If
  `staging.enabled` is set and no RWX class is available, dispatch **fails fast**
  with a clear error — it never silently degrades.
- The volume is a shared filesystem, **not transactional**. "Atomic per DAG" means
  isolation per run, not ACID. Tasks should write atomically
  (write-temp-then-rename).

## When to use what

| Need | Mechanism |
|---|---|
| Small handoff / parameters (≤256KB) | **XCom** (Redis) |
| Large intermediate data **within a run** | **Staging volume** (this) |
| Durable / cross-run / cross-DAG data | **Object storage** (S3/GCS) via a Connection |

## How Airflow 3.x compares

Airflow offers `airflow.io.ObjectStoragePath` (fsspec + a Connection) for object
storage and a custom XCom backend for large values, but has **no first-class
ephemeral shared volume** — PVCs are wired manually via pod templates with no
managed lifecycle or isolation. The managed per-run volume here is a Leoflow
value-add.
