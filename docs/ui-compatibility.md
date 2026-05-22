# Airflow UI Compatibility — Feasibility Investigation

**Status:** Investigation (Phase 5, 2026-05-22). This document records why the
naive Phase 5 goal — *run the unmodified Apache Airflow 3.2.x web UI against the
Leoflow API* — is **not viable as stated**, and what the realistic paths are.

## Finding: Airflow 3.x decoupled the UI onto an internal API

Airflow **2.x** served a Flask/FAB server-rendered UI; the stable REST API
(`/api/v1/`) was a separate, public surface. "Be API-compatible" largely meant
matching `/api/v1/`.

Airflow **3.x** (3.0 GA, Apr 2025) **rewrote the UI as a React SPA** served by a
FastAPI **API server**, and split the backend into *two* API surfaces:

| Surface | Purpose | Stability |
|---|---|---|
| **Public REST API** `/api/v2/` | external clients, OpenAPI-documented | stable, backward-compatible |
| **UI API** `/ui/*` (AIP-84) | powers the React UI (grid, graph/structure, dashboard, calendar) with aggregated/denormalized shapes | **internal, explicitly NOT backward-compatible, "should not be relied upon by external consumers"** |

The React UI is built against the **`/ui/*` API**, not the public `/api/v2/`.
The grid view, graph/structure view, and dashboard stats all come from `/ui/*`
endpoints whose shapes are tuned for the frontend and change between Airflow
releases by design (AIP-84 explicitly trades backward-compatibility away so the
UI can iterate).

### Consequence for Leoflow

Leoflow implements a subset of the **public `/api/v2/`**. It does **not**
implement `/ui/*`. Therefore:

- Pointing the **unmodified** Airflow 3.2.x UI at Leoflow's `/api/v2/` **will not
  work** — the UI calls `/ui/*` endpoints Leoflow doesn't serve.
- Implementing `/ui/*` to satisfy the UI means matching an **internal, unstable**
  API and re-chasing it on every Airflow minor release. That is a brittle,
  perpetual maintenance burden — the opposite of a stable compatibility target.

The original premise ("the `/api/v2/` API matches Airflow 3.2.x, so the UI just
works") reflects the **2.x** architecture. It does not hold for 3.x.

## Realistic paths

1. **Custom minimal UI (recommended).** Build a small React app against Leoflow's
   own stable `/api/v2/`. This is already on the post-MVP roadmap ("Custom UI
   (replacing the Airflow UI)"). It avoids chasing an internal API and gives us a
   stable contract we control. Best long-term fit with the GitOps/immutable
   thesis.
2. **Implement a pinned `/ui/*` subset.** Serve the Airflow 3.2.1 UI assets and
   implement exactly the `/ui/*` endpoints that version calls, pinned to 3.2.1.
   Delivers the familiar Airflow UI now, but is brittle and version-locked.
3. **Defer the UI.** For the MVP, the operator surface is the embedded **Scalar
   API reference** (`/docs`) plus the `leoflow runs` / `leoflow` CLI. The visual
   UI lands later via path 1 or 2.

## Public `/api/v2/` compatibility audit

Independent of the UI, the **public** API should stay Airflow-3-shaped for
external clients (Airflow operators, scripts, the `airflow` CLI's API mode). What
Leoflow exposes today aligns well on shape:

- ✅ Airflow-3 field naming: `logical_date` (not `execution_date`), `dag_run_id`,
  `task_instances`, `total_entries` pagination, the `__type` schedule field.
- ✅ Core resources: `GET /dags`, `GET|PATCH /dags/{id}`,
  `GET|POST /dags/{id}/dagRuns`, `.../taskInstances`, `.../logs/{try}`,
  `clearTaskInstances`, `xcoms`.
- ⚠️ **Breadth gap** vs Airflow 3.2 `/api/v2/`: not implemented yet —
  `/dags/{id}/details` (timetable_description, etc.), `/dagSources`, `/eventLogs`,
  `/variables`, `/connections`, `/pools`, `/assets`, `/monitor/health`,
  `/version`. These are needed for full external-client parity but not for the
  current execution surface.
- ⚠️ `/dags/{id}/versions` is **Leoflow-specific** (DAG-as-image versioning), not
  an Airflow endpoint.

## Recommendation

Treat "unmodified Airflow UI on `/api/v2/`" as **not viable** and pivot the UI
goal to **path 1 (custom minimal UI on the stable `/api/v2/`)**, with the public
API breadth gaps closed incrementally for external-client compatibility. This
warrants a short ADR recording the course-correction on the UI premise.

## Sources

- [Apache Airflow 3 is Generally Available](https://airflow.apache.org/blog/airflow-three-point-oh-is-here/)
- [AIP-84 UI REST API](https://cwiki.apache.org/confluence/display/AIRFLOW/AIP-84+UI+REST+API)
- [Airflow REST API endpoints & data models (DeepWiki)](https://deepwiki.com/apache/airflow/9.3-rest-api-endpoints-and-data-models)
