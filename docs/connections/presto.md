---
connectors: [presto]
---

# Presto connection

Connect a task to a [Presto](https://prestodb.io/) coordinator to run
distributed SQL over a managed Leoflow Connection. Presto is the upstream
project Trino forked from; the Connection shape is identical, only the
scheme and hook differ.

## URI shape

```
presto://<login>:<password>@<host>:<port>/<catalog>
```

The control plane builds this from the Connection's fields and exports it
as `AIRFLOW_CONN_<CONN_ID>`. Reserved characters in the password are
percent-escaped; `PrestoHook` un-escapes them on the way out.

## Fields the UI asks for

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `presto_default`. Exported as `AIRFLOW_CONN_PRESTO_DEFAULT`. |
| Conn Type | yes | `presto`. |
| Host | yes | Coordinator hostname, e.g. `presto.example.com`. |
| Port | usually `8080` | Coordinator HTTP port (`8443` for TLS). |
| Login | yes | Presto user, e.g. `etl_user`. |
| Password | optional | Password for `PASSWORD` auth. Encrypted at rest (ADR 0019). |
| Schema | optional | Default catalog, e.g. `hive`. |
| Extra | optional | JSON: `{"protocol":"https","catalog":"hive","schema":"default"}`. |

## Example DAG

```python
from airflow.sdk import dag, task


@dag(schedule=None, catchup=False, tags=["presto"])
def presto_query():
    @task
    def count_rows():
        from airflow.providers.presto.hooks.presto import PrestoHook

        hook = PrestoHook(presto_conn_id="presto_default")
        rows = hook.get_records("SELECT count(*) FROM hive.default.events")
        print("event count:", rows[0][0])

    count_rows()


presto_query()
```

```yaml
# leoflow.yaml
dag_id: presto_query
runtime: python3.12
requirements:
  - apache-airflow-providers-presto
connections:
  - conn_id: presto_default
    conn_type: presto
```

## Security notes

- **TLS**: set the coordinator port to `8443` and `Extra =
  {"protocol":"https"}`. Never run `PASSWORD` auth over plain HTTP.
- **Least privilege**: scope the Presto user to the catalogs the DAG needs.
- Never log `AIRFLOW_CONN_PRESTO_DEFAULT`; it carries the password.

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery (`AIRFLOW_CONN_<CONN_ID>`).
- `TestPrestoConnectionURIShapeIntegration` — chain-of-custody delivery test.
- [Trino connection](trino.md) — the modern fork of Presto.
