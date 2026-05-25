# Airflow 3.2.1 — Real UI/API Reference

Captured from a live `apache/airflow:3.2.1 standalone` (example DAGs) by driving the
React UI with Playwright and harvesting every endpoint the UI calls. Use this as the
**authoritative shape source** when implementing Leoflow's UI-facing API — do not guess
field names or nullability; copy from `bodies/*.json` and `SHAPES.md`.

- `bodies/*.json` — full real response bodies (29 endpoints), one file per endpoint.
- `SHAPES.md` — condensed type shape of each body.
- `screenshots/real_*.png` — each view as the real UI renders it.

## UI URL scheme (tabs are URL routes, not tab components)

The DAG view tabs are real client-side routes — replicate routing by path, not by a tab widget:

| Tab | URL |
|---|---|
| Overview (grid) | `/dags/{dag}` |
| Runs | `/dags/{dag}/runs` |
| Tasks | `/dags/{dag}/tasks` |
| Calendar | `/dags/{dag}/calendar` |
| Backfills | `/dags/{dag}/backfills` |
| Audit Log | `/dags/{dag}/events` |
| Code | `/dags/{dag}/code` |
| Details | `/dags/{dag}/details` |
| Task instance | click a state square in the grid → `/dags/{dag}/.../tasks/{task}` |

Left nav: Home, Dags, Assets, Browse, Admin, Docs. Header (DAG view) shows Schedule,
Latest Run, Next Run, Max Active Runs, Owner, Tags, Latest Dag Version + actions
(favorite ★, code, delete 🗑).

## Endpoint inventory → Leoflow status

`/ui/*` = internal AIP-84 API (unstable, what the SPA actually calls);
`/api/v2/*` = public stable API. Both must match for the embedded UI.

| Endpoint | Purpose | Leoflow | Issue |
|---|---|---|---|
| `GET /ui/config` | UI feature flags | minimal stub | — |
| `GET /api/v2/version` | version banner | ✅ | — |
| `GET /api/v2/monitor/health` | health widgets | ✅ real DB ping | — |
| `GET /api/v2/plugins` | plugin list | ✅ empty | — |
| `GET /api/v2/pools` | pool slots | stub empty | — |
| `GET /api/v2/importErrors` | parse errors banner | ✅ empty | — |
| `GET /ui/dashboard/dag_stats` | home: active/failed/running/queued **DAG** counts | **stub (zeroed)** | #39 |
| `GET /ui/dashboard/historical_metrics_data` | home: run + TI **state** counts | **stub (zeroed)** | #39 |
| `GET /api/v2/assets`, `/assets/events` | assets views | stub empty | — |
| `GET /api/v2/eventLogs` | **Audit Log** table | **missing** | #37 |
| `GET /ui/dags` | DAG list (+ filters) | partial (filters ignored) | #40 |
| `GET /api/v2/dags/{id}` / `/details` | DAG header + Details tab | ✅ | — |
| `GET /api/v2/dags/{id}/tasks` / `/tasks/{task}` | Tasks tab | ✅ | — |
| `GET /api/v2/dags/{id}/dagRuns` / `/{run}` | Runs | ✅ | — |
| `GET /ui/dags/{id}/latest_run` | grid header latest run | ✅ | — |
| `GET /ui/grid/runs/{id}` | grid columns (runs) | ✅ | — |
| `GET /ui/grid/structure/{id}` | grid rows (tasks) | ✅ | — |
| `GET /ui/grid/ti_summaries/{id}` | grid cell states | ✅ | — |
| `GET /ui/structure/structure_data` | graph nodes/edges | ✅ | — |
| `GET /api/v2/dags/{id}/dagRuns/{run}/taskInstances` / `/{task}` | TI list + single | ✅ | — |
| `GET .../taskInstances/{task}/tries` | per-attempt history | missing | — |
| `GET .../taskInstances/{task}/xcomEntries` | **task output / XCom** | **missing** | #38 |
| `GET .../taskInstances/{task}/logs/{try}` (JSON) | **drill-down logs** | text/plain only | #36, #43 |
| `GET /api/v2/dagSources/{id}` | Code tab source | ✅ (via dagSource) | — |
| `DELETE /api/v2/dags/{id}` | delete DAG | **missing** | #41 |
| `POST .../clearTaskInstances` | clear/retry | exists, verify filters | #42 |

## Key shapes to copy (full bodies in `bodies/`)

**`/ui/dashboard/dag_stats`** — `{active_dag_count, failed_dag_count, running_dag_count, queued_dag_count}` (counts of DAGs by latest-run state).

**`/ui/dashboard/historical_metrics_data`** —
`{dag_run_states:{queued,running,success,failed}, task_instance_states:{no_status,removed,scheduled,queued,running,success,restarting,failed,up_for_retry,up_for_reschedule,upstream_failed,skipped,deferred}, state_count_limit}`.

**`/api/v2/eventLogs`** —
`{event_logs:[{event_log_id,when,dag_id,task_id,run_id,map_index,try_number,event,logical_date,owner,extra,dag_display_name,task_display_name}], total_entries}`.

**`xcomEntries`** —
`{xcom_entries:[{key,timestamp,logical_date,map_index,task_id,dag_id,run_id,dag_display_name,task_display_name,run_after}], total_entries}`.

**logs JSON** — `{content:[{event,sources:[…]} | {timestamp,event,level,logger,filename,lineno}], continuation_token}`.
`::group::` / `::endgroup::` events drive the collapsible drill-down; `level` drives coloring.

## Task logs contract (crawled 2026-05-25, apache/airflow:3.2.1 standalone)

How the React SPA fetches task logs (captured from the live UI):

```
GET /api/v2/dags/{dag}/dagRuns/{run}/taskInstances/{task}/logs/{try}?map_index=-1
Accept: application/x-ndjson
```

- **Accept negotiation:** the SPA uses `application/x-ndjson` (one JSON event per
  line). `application/json` returns the single `{content:[…], continuation_token}`
  object. **`text/plain` is rejected** — `406 {"detail":"Only application/json or
  application/x-ndjson is supported"}`. (Leoflow additionally serves plain text as
  a permissive, curl-friendly superset; the SPA never uses it.)
- **No `follow`/streaming:** the SPA does a one-shot fetch and re-polls for live
  updates (it does not hold an SSE/stream). So accurate per-line `level` is what
  drives the live view's color + the "All Log Levels" filter.
- **`map_index=-1`** is sent for non-mapped tasks.
- **Event shapes** (`bodies/ti_logs_ndjson.ndjson`): every line carries
  `timestamp` (explicitly `null` on the `::group::`/`::endgroup::` markers).
  Group: `{timestamp:null, event, sources:[…]}` and `{timestamp:null, event}`.
  Task output: `{timestamp, event, level, logger:"task.stdout"|"task.stderr"}`.
  Framework lines add `filename, lineno` (and sometimes `ti`) — Leoflow emits no
  framework lines, so it legitimately omits those; its task output matches the
  `task.stdout` shape. Leoflow omits `timestamp` on group markers (vs real
  Airflow's explicit `null`) — cosmetic, the viewer treats absent and null alike.
