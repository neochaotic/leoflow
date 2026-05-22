# Phase 5.2 — DAG List, Grid View, Graph View

## Goal

Implement the /ui/* endpoints for the three core read-only screens: DAG list,
Grid view (per-DAG run history), Graph/Structure view (DAG topology).

End state: log in (5.1) → see DAG list → click DAG → Grid with runs + per-task
states live → click task instance → log (live tail, Phase 4) → Graph tab →
topology rendered. Trigger/clear NOT in scope (→ 5.3).

## Prerequisites

Phase 5.1 complete. Re-read docs/ui-compatibility.md incl. 5.1 section.

## Constraints

Same as 5.1. Coverage 80%. All shapes pinned to Airflow 3.2.1 _private_ui.yaml.

## Deliverables

1. **GET /ui/dags** — UI-shaped DAG collection (per-DAG aggregates not in the
   public API: last_run_state, active_runs_count, etc.). Paging (limit/offset),
   filters (tags, only_active, paused). Read DagRepository + DagRunRepository;
   1–2 SQL queries total, no N+1.
2. **GET /ui/dags/{dag_id}/latest_run** — DAGRunLightResponse for most recent
   run; 404 if none.
3. **GET /ui/grid/structure/{dag_id}** — grid left-column task topology
   (topologically sorted); include `children` field (empty; no task groups in
   MVP).
4. **GET /ui/grid/runs/{dag_id}** — recent runs as grid columns (dag_id, run_id,
   state, run_type, start_date, end_date, duration, …); paging; logical_date DESC.
5. **GET /ui/grid/ti_summaries/{dag_id}** — per-run per-task state summaries
   (run_id→task_id→state). Most DB-heavy: ONE SQL join of dag_runs+task_instances
   by dag_id, group in Go; ETag on max(updated_at) for conditional GET.
6. **GET /ui/structure/structure_data?dag_id=** — Graph nodes (per task +
   operator/trigger hints) + edges (per dependency from/to). StructureDataResponse
   shape. Source: dag_versions.spec JSONB current version.
7. **GET /api/v2/dags/{dag_id}/details** — UI detail header: timetable_description
   (cron→English helper: cover @daily/@hourly/simple cron, fall back to raw),
   owners, schedule_interval (__type-tagged), max_active_runs, catchup,
   next_dagrun, etc.
8. **GET /api/v2/version** — {version, git_version} from internal/version.

## TDD

Per endpoint: happy path (fixtures), empty state, permission (403 w/o read:dag),
tenant isolation, spec validation. Integration (//go:build integration): 3 DAGs
× 10 runs × 5 tasks; assert aggregates; ti_summaries ≤2 SQL queries regardless of
size. E2E (//go:build e2e): login → DAG → grid colors → Graph tab nodes →
task-instance log; screenshots ui-phase-5-2-grid.png, ui-phase-5-2-graph.png.

## Performance (largest fixture 10×100×20 = 20k TIs)

ti_summaries <100ms p95; grid/runs <50ms p95; structure_data <50ms p95. Profile
and fix before done if exceeded.

## Out of scope (→ 5.3)

/ui/config; degradation stubs (dashboard/calendar/gantt/dependencies/backfills/
teams); trigger/clear/pause E2E; full docker-compose+README screenshots.

## Acceptance

All endpoints match spec; unit+integration+E2E pass; ≥80% coverage; perf met;
screenshots committed; lint A+; govulncheck clean; ui-compatibility.md 5.2 section;
test→feat commits.

## Attention point (founder)

Riskiest: /ui/grid/ti_summaries/{dag_id} performance. A naive N+1 with 20k task
instances kills the server. In PR review, require the EXPLAIN ANALYZE of the
sqlc-generated SQL. Keep it to ≤2 queries; group in Go.
