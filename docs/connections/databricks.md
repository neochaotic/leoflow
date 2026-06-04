# Databricks connection

Trigger Databricks jobs / SQL from a task via a managed Leoflow Connection and the
Databricks provider hooks. The conn_type is `databricks`. A connection carries the
**workspace host** plus a **Personal Access Token** (PAT).

## Declare the provider

```yaml
# leoflow.yaml
dag_id: databricks_job
connectors:
  - databricks
```

## Fields the UI asks for

| Field | Where it goes | Notes |
|---|---|---|
| Conn Id | — | e.g. `databricks_default`. |
| Host | host | The workspace URL, e.g. `dbc-12345678-9abc.cloud.databricks.com`. |
| Password | password | The **Personal Access Token** (`dapi…`). Encrypted at rest (ADR 0019). |
| Extra | extra | Optional, e.g. `{"http_path": "/sql/1.0/warehouses/abc123"}` for SQL warehouses. |

The PAT round-trip (reserved characters) + host + `http_path` Extra are pinned by
`TestDatabricksConnectionURIShapeIntegration`.

## Example DAG (copy-paste)

Docs-only recipe (needs a real Databricks workspace). The hook is imported
**inside** the task.

```python
# dag.py
from __future__ import annotations

from airflow.sdk import DAG, task


@task
def run_now() -> None:
    from airflow.providers.databricks.hooks.databricks import DatabricksHook

    hook = DatabricksHook(databricks_conn_id="databricks_default")
    print("run_now: triggering job via DatabricksHook(databricks_default)")
    run_id = hook.run_now({"job_id": 1234})
    state = hook.get_run_state(run_id)
    print(f"run_now: run {run_id} state {state.life_cycle_state}")


with DAG("databricks_job", schedule=None, catchup=False, tags=["example"]):
    run_now()
```

```yaml
# leoflow.yaml
schema_version: "1.0"
dag_id: databricks_job
description: Trigger a Databricks job via DatabricksHook.
owner: examples
tags:
  - example
python_version: "3.11"
connectors:
  - databricks
```

### Run it

1. **Admin → Connections → +**, type `databricks`. Set Host (workspace URL) and
   the PAT in Password.
2. `leoflow lite path/to/this/dag` → trigger `databricks_job`.

## Security notes

- **Scope the PAT** to the workspace and rotate it regularly; it is encrypted at
  rest and never echoed back.
- Prefer a **service principal** token over a personal one for production jobs.
- **Never `print()` the URI** — it carries the PAT.

## Related

- `docs/connections/index.md` — *Installing a connector's provider*.
- ADR 0019 / 0021 — secret encryption + agent delivery.
