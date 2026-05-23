# Reverse Analysis — Airflow 3.2.1 vs Leoflow, MVP readiness

**Date:** 2026-05-23
**Goal:** stabilize toward a first release — a clean MVP without fancy features.
The MVP target: one end-to-end DAG with three tasks passing XCom, clear logs, the
home-panel filters working, and no zombie/stuck runs.

Method: ran a live `apache/airflow:3.2.1 standalone`, triggered the canonical
`example_xcom` DAG, and captured its task logs, XCom, and lifecycle via the API
(`/tmp/revanalysis/`, and the curated fixtures in `docs/reference/airflow-real/`).
Cross-checked against the Airflow 3.x docs (XCom, task states, task logging).

---

## 1. Airflow 3.2 key concepts (from the docs) and how Leoflow maps

### XCom
| Airflow behavior | Leoflow status |
|---|---|
| Default key `return_value`; `@task` return auto-pushes | ✅ runtime writes the return value; agent pushes `return_value` |
| TaskFlow function args pulled from upstream XCom (`transform(extract())`) | ✅ #51 — parser emits `xcom_input`, runner binds `fn(**kwargs)` |
| Stored small, in the metadata store (configurable backend) | ✅ Redis backend + Postgres `xcom_index`; ≤256 KB cap |
| Operators auto-push with `do_xcom_push` (BashOperator pushes last stdout line) | ⚠️ only Python `@task` push/pull is wired; **Bash/HTTP operator XCom is not** (MVP: use Python tasks for XCom) |
| **XComs clear on task retry** (idempotency) | ⚠️ not explicitly cleared on `ResetForRetry`/clear — verify (Redis TTL masks it; a real clear should purge the key) |
| `multiple_outputs=True` splits a dict into keyed XComs | ❌ not implemented (not MVP) |

### Task instance states (13 in Airflow)
`none → scheduled → queued → running → success` is the happy path. Leoflow's
`task_state` enum has: none, scheduled, queued, running, success, failed,
skipped, upstream_failed, up_for_retry. **Not modeled:** `deferred` (v0.3,
deprioritized), `up_for_reschedule` (sensors), `restarting`, `removed`. None are
MVP-blocking.

### Task logging
Airflow: per-attempt files `dag_id=.../run_id=.../task_id=.../attempt={try}.log`;
UI fetches structured JSON (`content[]` of `{timestamp,event,level,logger,...}`)
with `::group::`/`::endgroup::` collapsible source markers; live tail while
running. **Leoflow matches all of these** (#36 ship, #43 structured `::group::`,
#44 real level/stream, live tail via Redis).

---

## 2. Live capture — the "clear logs" reference (example_xcom)

Real Airflow `bash_push` log (Accept: application/json), first items:
```jsonc
{ "event": "::group::Log message source details",
  "sources": ["/opt/airflow/logs/dag_id=example_xcom/run_id=.../task_id=bash_push/attempt=1.log"] },
{ "event": "::endgroup::" },
{ "timestamp": "...Z", "event": "DAG bundles loaded: ...", "level": "info",
  "logger": "airflow.dag_processing...", "filename": "manager.py", "lineno": 209 },
{ "timestamp": "...Z", "event": "Task instance is in running state", "level": "info", "logger": "task.stdout" }
```
Leoflow's `serveStructuredLogs` produces the same shape (group fold + per-line
level). Difference: Airflow emits framework log lines (DagBag load, state
transitions) with rich `logger`/`filename`/`lineno`; Leoflow currently emits the
**task's own stdout/stderr** only. For MVP that is acceptable — the user's task
prints are what matter — but the logs are "thinner" than Airflow's.

`example_xcom` task set (mixes operator types + TaskFlow + cross-operator pull):
`bash_push`, `bash_pull`, `pull_value_from_bash_push`, `push_by_returning`,
`puller`, `push` — all `success`. XCom shape matches Leoflow's `xcomEntries`.

---

## 3. MVP target — gap analysis

Target: **3-task DAG, XCom between tasks, clear logs, home filters, no stuck.**

| Requirement | Status | Notes |
|---|---|---|
| 3 tasks end-to-end on real pods | ✅ | k3d demo, pod-per-task (#35) |
| XCom passed between tasks | ✅ | Python `@task` value passing (#51), verified `{rows:100}→doubled:200` |
| Clear logs (structured, levels, drill-down) | ✅ | #36/#43/#44; shipped from pods |
| Home dashboard counts (not zeroed) | ✅ | #39 |
| Home/list filters (state, paused) | ✅ | #40 |
| No zombie / stuck-without-reason | ✅ | #46 (note + metric) + #50 (fail-fast undispatchable); reconciler catches pod failures |
| Delete DAG, clear/retry, audit, code=python | ✅ | #41, #42, #37, #49 |
| Admin (Variables, Connections) | ✅ | #45 + ADR 0019 |

### Remaining MVP-relevant gaps (small, non-blocking)
1. **XCom not cleared on retry/clear** — Airflow purges XCom on retry for
   idempotency; Leoflow relies on Redis TTL. Low risk (return_value overwrites),
   but a clean clear should purge. (follow-up)
2. **Operator XCom (Bash/HTTP)** — only Python `@task` XCom is wired. For the MVP
   "3 operators passing XCom", use **3 Python tasks** (the clean path). Mixed
   operator XCom is post-MVP.
3. **Task-level audit events** empty (#52) — secondary.
4. **Empty stubs** still in nav-hidden sections (assets, pools, providers) —
   intentionally hidden until backed (#26–#32).

### Not-MVP (deprioritized)
Deferrable tasks (#13), Jinja templating (#25), assets/datasets, providers,
multiple_outputs.

---

## 4. MVP readiness verdict

The happy path is **functionally complete and verified**: a 3-task Python DAG
passing XCom runs end-to-end on real pods with clear, structured, shipped logs;
the home panel shows real counts and filters work; undispatchable/stuck tasks
fail fast with a visible reason rather than hanging. The remaining gaps are small
and non-blocking for a first release.

**Recommended pre-release checklist:** (a) clear XCom on retry/clear; (b) decide
whether MVP needs Bash/HTTP-operator XCom or Python-only is enough; (c) publish
the leoflow-migrate image (#48); (d) one more end-to-end run captured as the
release smoke (the k3d e2e already asserts state + log shipping + XCom #51).
