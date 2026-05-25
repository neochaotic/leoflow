# Examples

A gallery of ready-to-run DAGs under [`examples/`](https://github.com/neochaotic/leoflow/tree/main/examples),
covering every Leoflow task type and the common patterns. Each is compile-valid
(`leoflow compile`) and authored parser-safe (heavy imports live *inside* the
tasks). Run any of them with:

```bash
leoflow dev examples/<name>      # hot-reload at http://localhost:8088, then Trigger
```

## The gallery

| Example | Shows | Task type | Deps |
|---|---|---|---|
| `taskflow_sales` | TaskFlow ETL, data via XCom | python | — |
| `xcom_typed` | typed XCom payloads + validation | python | — |
| `fan_out_aggregate` | fan-out to parallel pods → fan-in | python | — |
| `montecarlo_pi` | parallel compute (estimate π) | python | — |
| `http_jsonplaceholder` | call a public JSON API | python | requests |
| `weather_open_meteo` | public weather API (no key) | python | requests |
| `api_chain` | chain two API calls | python | requests |
| `duckdb_http_csv` | DuckDB reads a remote CSV, aggregates | python | duckdb |
| `postgres_load` | load to external Postgres via a Connection | python | psycopg2 |
| `csv_report` | **scheduled** (cron `0 6 * * *`) report | python | — |
| `bash_pipeline` | shell tasks | **bash** (BashOperator) | — |
| `http_operator` | HTTP request run **inline** | **http_api** (HttpOperator) | — |

All three Leoflow task types are represented — **python** (TaskFlow `@task` /
PythonOperator), **bash** (BashOperator), and **http_api** (HttpOperator, executed
inline by the control plane). For a measured ~1 GB pipeline see the
[ETL case study](etl-staging-case-study.md).

!!! tip "Import heavy deps inside the task"
    `import duckdb` / `import requests` go **inside** the task function — the DAG
    parser imports the module to extract structure and does not have the task
    image's dependencies.

## Removing a DAG (clear vs. deregister)

Leoflow is GitOps: the **source is the source of truth**, so deleting is two
distinct actions (ADR 0020).

| Action | Effect |
|---|---|
| **Clear history** (UI trash · `leoflow dags delete <id>`) | deletes runs/tasks; the **DAG stays registered** |
| **Deregister** (`leoflow dags delete <id> --deregister`) | removes the DAG artifact (DAG + versions) |

But deregister alone is **not permanent while the source exists** — it gets
re-registered:

- **Dev (`leoflow dev`):** the watcher re-registers the DAG on the next reload.
  To remove it for good, **delete the DAG's file** (or stop/point `leoflow dev`
  elsewhere).
- **Production (CI deploy):** the next deploy that still includes the DAG
  re-registers it as a new version. To remove it for good, **drop it from the
  repo/CI**, then optionally deregister to clear what is registered now.
- **Demo:** seeded once at boot with no watcher/CI re-registering, so a clear or
  deregister sticks until you re-seed.

In short: to truly remove a DAG, **remove its source** (file in dev, repo in
prod); `--deregister` just clears the current registration. (The embedded Airflow
UI's trash maps to *clear history*; an explicit "Clear vs Deregister" dialog is
planned for the custom UI — ADR 0018/0020.)
