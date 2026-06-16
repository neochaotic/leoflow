# Druid connection

Connect a task to an Apache Druid cluster (the `DruidDbApiHook`) over a
managed Leoflow Connection. The query path goes through the Druid broker.

## URI shape

```
druid://<login>:<password>@<host>:<port>/<schema>
```

The control plane builds this URI from the Connection's fields. Reserved
characters in the password (e.g. `@`, `:`, `/`) are percent-escaped by the
URI builder; `DruidDbApiHook` un-escapes them back. The reserved-character
round-trip is pinned by `TestDruidConnectionURIShapeIntegration` (in
`internal/storage/`).

## Fields the UI asks for

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `druid_target`. Exported as `AIRFLOW_CONN_DRUID_TARGET` (uppercased). |
| Conn Type | yes | `druid`. |
| Host | yes | The Druid broker host. |
| Schema | optional | The schema namespace (commonly `druid`). |
| Login | yes | The Druid user. |
| Password | yes | Stored encrypted at rest (ADR 0019). |
| Port | optional | Defaults to `8082` (broker). |
| Extra | optional | JSON — e.g. `{"endpoint":"druid/v2/sql"}`. Encrypted at rest. |

## Example DAG

The provider import must live **inside** the task body — a top-level
provider import fails compilation in the parser sidecar.

```python
from datetime import datetime
from airflow.decorators import dag, task


@dag(schedule=None, start_date=datetime(2024, 1, 1), catchup=False)
def druid_smoke():
    @task
    def query():
        from airflow.providers.apache.druid.hooks.druid import DruidDbApiHook

        hook = DruidDbApiHook(druid_broker_conn_id="druid_target")
        rows = hook.get_records("SELECT 1")
        print("druid up:", rows)

    query()


druid_smoke()
```

```yaml
# leoflow.yaml
name: druid_smoke
schedule: null
image:
  python: "3.11"
  requirements:
    - apache-airflow-providers-apache-druid
connectors: [druid]
```

## Security notes

- **Secrets in logs**: never `print()` the URI itself — it carries the
  password. Log host + port + schema only.
- **TLS in transit**: front the broker with HTTPS and set the endpoint in
  **Extra** accordingly.
- **gRPC channel (agent ↔ control plane)**: Connections are only served
  over an authenticated channel (see #58 + ADR 0021).

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery (`AIRFLOW_CONN_<CONN_ID>`).
- #142 — connector cookbook umbrella.
