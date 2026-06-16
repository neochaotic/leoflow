---
connectors: [trino]
---

# Trino connection

Connect a task to a [Trino](https://trino.io/) coordinator to run
federated SQL across Hive, Iceberg, PostgreSQL, and other catalogs over a
managed Leoflow Connection.

## URI shape

```
trino://<login>:<password>@<host>:<port>/<catalog>
```

The control plane builds this from the Connection's fields and exports it
as `AIRFLOW_CONN_<CONN_ID>`. Reserved characters in the password are
percent-escaped; `TrinoHook` un-escapes them on the way out.

## Fields the UI asks for

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `trino_default`. Exported as `AIRFLOW_CONN_TRINO_DEFAULT`. |
| Conn Type | yes | `trino`. |
| Host | yes | Coordinator hostname, e.g. `trino.example.com`. |
| Port | usually `8080` | Coordinator HTTP port (`8443` for TLS). |
| Login | yes | Trino user, e.g. `etl_user`. |
| Password | optional | Password for `PASSWORD` auth. Encrypted at rest (ADR 0019). |
| Schema | optional | Default catalog, e.g. `hive`. |
| Extra | optional | JSON: `{"protocol":"https","catalog":"hive","schema":"default"}`. |

## Example DAG

The hook is imported **inside** the task body so the DAG file parses even
where the provider isn't installed.

```python
from airflow.sdk import dag, task


@dag(schedule=None, catchup=False, tags=["trino"])
def trino_query():
    @task
    def count_rows():
        from airflow.providers.trino.hooks.trino import TrinoHook

        hook = TrinoHook(trino_conn_id="trino_default")
        rows = hook.get_records("SELECT count(*) FROM hive.default.events")
        print("event count:", rows[0][0])

    count_rows()


trino_query()
```

```yaml
# leoflow.yaml
dag_id: trino_query
runtime: python3.12
requirements:
  - apache-airflow-providers-trino
connections:
  - conn_id: trino_default
    conn_type: trino
```

## Security notes

- **TLS**: set the coordinator port to `8443` and `Extra =
  {"protocol":"https"}`. Never run `PASSWORD` auth over plain HTTP.
- **Least privilege**: scope the Trino user to the catalogs and schemas
  the DAG needs; Trino's system access control enforces this.
- Never log `AIRFLOW_CONN_TRINO_DEFAULT`; it carries the password.

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery (`AIRFLOW_CONN_<CONN_ID>`).
- `TestTrinoConnectionURIShapeIntegration` — chain-of-custody delivery test.
- [Presto connection](presto.md) — the sibling query engine.
