# ADR 0006: XCom with Redis Backend

**Status:** Accepted
**Date:** 2026-05-21

## Context

XCom ("cross-communication") is the mechanism for passing small payloads between tasks. Airflow stores XCom in the metadata database (Postgres), which has two consequences:

- Heavy writes degrade scheduler performance because XCom shares connection pool and ORM with the rest of Airflow.
- There is no automatic TTL: XCom rows accumulate forever unless manually cleaned.

XCom is supposed to be for **small typed payloads**, not for moving data. Yet Airflow does not enforce this, and users abuse it.

## Decision

Leoflow stores XCom in **Redis**, separated entirely from the Postgres metadata database. XCom has:

- A **hard size limit of 256 KB** per key. Writes above this fail with a clear error.
- A **TTL of 7 days** by default, configurable per DAG.
- A **typed schema (optional)** declared in the DAG.
- A **gRPC-only API**, never exposed via REST. Only the Agent talks to Redis indirectly through the Control Plane.

For payloads larger than 256 KB, users must use a blob store (S3/GCS) and pass a reference. Leoflow will not become a data movement system.

## Why Redis (not Postgres)

- **Latency.** Sub-millisecond round trips vs. tens of milliseconds for Postgres.
- **Native TTL.** Redis handles expiration automatically. No cleanup job needed.
- **Isolation.** XCom traffic cannot affect the scheduler's database performance.
- **Footprint.** A small Redis instance (1 GB) handles enormous XCom volume.

## Why Not Postgres-First Then Migrate

It is tempting to start with Postgres "to reduce dependencies" and migrate later. This is a trap:

- Migrating storage backends in production is painful and error-prone.
- The XCom protocol (size limits, TTL semantics, retrieval API) depends on the backend.
- Adding Redis later means a breaking change.

Better to pay the cost on day one and have a coherent design.

## Data Model in Redis

Keys follow this pattern:

```
xcom:{tenant_id}:{dag_id}:{run_id}:{task_id}:{key_name}
```

Values are JSON-encoded payloads. TTL is set on `SET`.

Index keys (for listing XComs by run, for the UI):

```
xcom_index:{tenant_id}:{dag_id}:{run_id}  →  SET of full keys
```

## Communication Flow

```
Task A finishes ──► Agent sends gRPC PushXCom ──► Control Plane writes Redis
                                                  │
Task B starts   ──► Agent sends gRPC FetchXCom ──► Control Plane reads Redis
                    (only if Task B declares it needs Task A's output)
```

Tasks pull XComs **lazily** at the moment of use, not at startup. The Control Plane validates that the requesting task has authorization (same DAG run, declared dependency).

## Schema Validation (Optional)

The DAG can declare expected XCom types:

```json
{
  "task_id": "extract",
  "entrypoint": "tasks.extract:run",
  "xcom_schema": {
    "output": { "type": "object", "properties": { "rows": { "type": "integer" } } }
  }
}
```

On push, the Control Plane validates the payload against the JSON Schema. Mismatches fail the task with a clear error.

This is **opt-in** for the MVP. Most users will not need it. Enterprise users will.

## Consequences

- Redis becomes a hard dependency, even in standalone mode. Standalone Docker Compose bundles Redis.
- Operators must monitor Redis memory and configure persistence (AOF recommended).
- The 256 KB limit must be documented prominently. Users coming from Airflow will hit it and need clear guidance toward blob stores.
- The XCom REST API for the UI is read-only and translates Redis reads into Airflow-compatible JSON responses.

## Alternatives Rejected

- **Postgres for XCom:** rejected due to performance impact on metadata DB and lack of native TTL.
- **S3 as XCom backend:** rejected as latency is too high for small payloads in the hot path.
- **In-memory only:** rejected because XCom must survive scheduler restarts mid-run.
