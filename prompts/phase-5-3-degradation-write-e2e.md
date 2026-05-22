# Phase 5.3 — Graceful Degradation, Write Flows, and Full E2E Validation

## Goal

Close Phase 5. Wire remaining /ui/* as graceful stubs so the UI never errors on
uncovered screens, validate write flows (trigger, clear, pause/unpause) E2E
through the actual UI, and ship the docker-compose experience.

End state: README "Getting Started" works — clone → docker compose up → browser →
working DAG run with logs — in under 5 minutes.

## Prerequisites

Phase 5.2 complete (login, DAG list, Grid, Graph rendering).

## Constraints

Same as 5.1/5.2. Coverage 80%. Shapes pinned to spec.

## Deliverables

1. **GET /ui/config** — ConfigResponse: instance_name "Leoflow",
   auto_refresh_interval 30, hide_paused_dags_by_default false, theme default,
   disable unsupported features (assets_enabled/plugins_enabled false). Match
   each field to the spec. Second backend lever (with /ui/auth/menus).
2. **Graceful stubs** (200, schema-valid empty per spec): /ui/dashboard/
   historical_metrics_data, /ui/dashboard/dag_stats, /ui/calendar/{dag_id},
   /ui/gantt/{dag_id}/{run_id}, /ui/dependencies, /ui/backfills,
   /ui/next_run_assets/{dag_id}, /ui/partitioned_dag_runs,
   /ui/pending_partitioned_dag_run/{dag_id}/{partition_key},
   /ui/dags/{dag_id}/dagRuns/{dag_run_id}/deadlines, /ui/teams,
   /ui/connections/hook_meta. Writes on these → 501 problem+json with detail
   pointing to the issue tracker (UI shows a toast).
3. **Write flows E2E** (already on /api/v2/*): verify the UI buttons trigger
   them. If the UI calls /ui/dags/{id} (PATCH) instead of /api/v2/dags/{id}, add
   the alias. Flows: trigger run (modal+conf → run in grid <5s); clear task
   instance (→none→scheduled/queued, visible); pause/unpause (DB updates,
   reflected on reload).
4. **docker-compose.yml** (full): postgres 16-alpine, redis 7-alpine,
   leoflow-server (local Dockerfile w/ embedded UI). Recommended
   **bring-your-own-k8s**: compose runs pg/redis/leoflow-server; README documents
   pointing at local k3d/kind. Boot <30s; bootstrap admin + print creds; optional
   sample DAG for the 5-min demo.
5. **E2E suite** (test/e2e/full_flow_test.go, //go:build e2e): testcontainers-go
   boots the stack; push 2-task DAG (python+http_api); chromedp/playwright walk:
   login → DAG in list → trigger → states transition → logs → clear+re-exec →
   pause (no auto-trigger). Screenshots each step. Dedicated CI job, not per-PR.
   `make e2e-full`.
6. **Screenshots** docs/screenshots/: 01-login, 02-dag-list, 03-grid-view,
   04-graph-view, 05-task-log, 06-triggered-run.
7. **README** Getting Started: real docker-compose command, 5-min demo with
   checkpoints, header screenshot.
8. **CHANGELOG** [0.1.0-rc1]: Phase 5 complete, compose getting-started, E2E suite.
9. **ADR 0007 (Airflow UI Compatibility) Revision History** — note the premise
   refined from 2.x-style /api/v2 parity to pinned 3.x /ui/* approach; reference
   ADR 0017 + ui-compatibility.md. (Authorized ADR modification.)
10. **TDD:** each stub returns schema-valid empty + validates against spec; write
    aliases delegate to existing handlers; /ui/config shape + instance_name. The
    full E2E IS the failing test — implement until green.

## Acceptance

/ui/config correct; stubs schema-valid empty; 501 detail on unsupported writes;
3 write flows verified E2E in browser; compose up <30s; E2E suite passes on fresh
stack; README getting-started verified on a clean machine; screenshots committed +
embedded; ADR 0007 revision; CHANGELOG 0.1.0-rc1; ≥80% coverage; A+; govulncheck
clean; test→feat commits.

## Definition of Done for Phase 5

After 5.3 merges, Phase 5 is closed: the README MVP demo is real — clone, docker
compose up, working orchestrator with the Airflow UI in 5 minutes. Phase 6
(hardening) then polishes for production.

---

## Attention points during execution (founder guidance)

- **5.1 — curated-menu risk.** If the 3.2.1 UI ignores /ui/auth/menus and calls
  e.g. /ui/connections directly, it 404s and pollutes the console — ugly, not
  fatal. The 5.3 empty stubs cover this, so the phase order (auth → views →
  stubs) is correct.
- **5.2 — ti_summaries performance.** /ui/grid/ti_summaries/{dag_id} is the
  performance danger: with 20k task instances a naive N+1 kills the server. In
  PR review, require the EXPLAIN ANALYZE of the sqlc-generated SQL.
- **5.3 — full E2E.** The full-flow E2E is where it really breaks. If a step
  fails, do NOT thrash "fixing" things that already worked — pause, identify
  which of the 6 steps broke, and focus only on that step (ideally a fresh
  session).
