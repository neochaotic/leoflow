# ADR 0007: Airflow UI Compatibility for the MVP

**Status:** Accepted
**Date:** 2026-05-21

## Context

Building a workflow orchestrator means building a UI: graph view, grid view, log viewer, manual triggers, task clearing. This is months of frontend work.

Apache Airflow has a mature, battle-tested UI (rewritten in React for Airflow 3). It is open source under the same Apache 2.0 license that Leoflow uses.

## Decision

For the MVP, Leoflow uses the **unmodified Airflow 3.2.x UI**. The Leoflow Control Plane exposes a REST API that matches the Airflow 3.2.x public API (`/api/v2/...`) closely enough that the UI works without modification.

We **do not fork** the UI in the MVP. We ship the Airflow Docker image and configure it to point at the Leoflow API.

## Rationale

- **Time saved.** Months of frontend work avoided.
- **Familiar to users.** Airflow users transfer their muscle memory directly.
- **Free updates.** When the Airflow UI improves, Leoflow benefits.
- **Forces good API design.** Compatibility with a real UI keeps us honest.

## How Compatibility Is Achieved

The Go Control Plane implements the subset of `/api/v2/` endpoints that the UI consumes. Endpoints not consumed by the UI are not implemented in the MVP.

The internal Leoflow domain model is **richer** than Airflow's (cleaner state machine, native versioning, multi-tenancy). Internal types are translated to Airflow-compatible DTOs in the HTTP handler layer.

This is an **anti-corruption layer** in domain-driven-design terms: the public API speaks Airflow, the internal core speaks Leoflow.

## Concrete Endpoints in the MVP

| Endpoint | Method | Purpose |
|---|---|---|
| `/auth/token` | POST | Issue JWT for username/password |
| `/api/v2/dags` | GET | List DAGs |
| `/api/v2/dags/{dag_id}` | GET, PATCH | Get DAG, pause/unpause |
| `/api/v2/dags/{dag_id}/dagRuns` | GET, POST | List runs, trigger run |
| `/api/v2/dags/{dag_id}/dagRuns/{run_id}` | GET | Run details |
| `/api/v2/dags/{dag_id}/dagRuns/{run_id}/taskInstances` | GET | List task instances |
| `/api/v2/dags/{dag_id}/dagRuns/{run_id}/taskInstances/{task_id}` | GET | Task instance details |
| `/api/v2/dags/{dag_id}/dagRuns/{run_id}/taskInstances/{task_id}/logs/{try}` | GET | Task logs |
| `/api/v2/dags/{dag_id}/clearTaskInstances` | POST | Clear (re-run) tasks |
| `/api/v2/xcoms/{...}` | GET | Read-only XCom view |

The OpenAPI spec is in `docs/api/openapi.yaml`.

## What Does Not Work Out of the Box

Some Airflow features need backend support that the MVP does not implement:

- **Backfill UI.** Disabled or stubbed for the MVP.
- **Manual mark success/failed.** Stubbed; v1.1 feature.
- **DAG dependencies graph.** Stubbed; v1.1 feature.
- **Plugins, providers list, connections UI.** Stubbed.

These are all areas where the UI will either show empty states or refuse to function. Acceptable for the MVP.

## Future: Custom UI

In a later version (v2.x), Leoflow may ship its own UI optimized for the richer internal model. At that point, the Airflow-compatible API remains as a backward-compatibility layer.

## Consequences

- Operators deploy two containers: `leoflow-server` (Go) and `airflow-webserver` (Python). The Airflow webserver is configured with `AIRFLOW__API__BACKEND` pointing at the Leoflow API.
- The Leoflow team must track Airflow 3.x API changes. Minor version bumps in Airflow may require Leoflow patches.
- Authentication must work both ways: the UI obtains a JWT via `/auth/token` and forwards it on subsequent calls.
- The CORS configuration in `leoflow-server` must allow the Airflow webserver's origin.

## Alternatives Rejected

- **Fork the Airflow UI.** Rejected for the MVP due to maintenance burden. Open option for v2.
- **Build a new UI from scratch.** Rejected for the MVP due to time cost.
- **Use a third-party UI (Argo UI, etc.).** Rejected because they do not match Airflow's mental model and would surprise users.

## Revision History

- **2026-05-23 (Phase 5).** The premise was refined during implementation. This
  ADR assumed the Airflow 2.x model: a *separate* Airflow webserver pointed at the
  Leoflow API via `AIRFLOW__API__BACKEND`, with `/api/v2` parity sufficient to
  render the UI. Airflow 3.x invalidated that — the React SPA targets an internal,
  non-public `/ui/*` API (AIP-84), not just `/api/v2`. Leoflow therefore **embeds
  the pinned Airflow 3.2.1 SPA in `leoflow-server`** (single binary, no second
  container) and **implements `/ui/*` pinned to 3.2.1**. The two-container
  deployment and CORS notes above are superseded by the single-origin embed. See
  ADR 0017 (asset serving), ADR 0018 (custom UI as the strategic north star), and
  `docs/ui-compatibility.md` for the full learnings. The original decision —
  reuse Airflow's UI rather than build one for the MVP — still holds.
