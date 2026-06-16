# Pinot connection

Connect a task to an Apache Pinot real-time OLAP store (the `PinotDbApiHook`)
over a managed Leoflow Connection. Queries go through the Pinot broker.

## URI shape

```
pinot://<login>:<password>@<host>:<port>/<schema>
```

The control plane builds this URI from the Connection's fields. Reserved
characters in the password (e.g. `@`, `:`, `/`) are percent-escaped by the
URI builder; `PinotDbApiHook` un-escapes them back. The reserved-character
round-trip is pinned by `TestPinotConnectionURIShapeIntegration` (in
`internal/storage/`).

## Fields the UI asks for

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `pinot_target`. Exported as `AIRFLOW_CONN_PINOT_TARGET` (uppercased). |
| Conn Type | yes | `pinot`. |
| Host | yes | The Pinot broker host. |
| Schema | optional | The schema namespace (commonly `default`). |
| Login | optional | Pinot is often unauthenticated; set if your broker requires it. Encrypted at rest. |
| Password | optional | Stored encrypted at rest (ADR 0019). |
| Port | optional | Defaults to `8000` (broker). |
| Extra | optional | JSON — e.g. `{"endpoint":"query/sql"}`. Encrypted at rest. |

## Example DAG

The provider import must live **inside** the task body — a top-level
provider import fails compilation in the parser sidecar.

```python
from datetime import datetime
from airflow.decorators import dag, task


@dag(schedule=None, start_date=datetime(2024, 1, 1), catchup=False)
def pinot_smoke():
    @task
    def query():
        from airflow.providers.apache.pinot.hooks.pinot import PinotDbApiHook

        hook = PinotDbApiHook(pinot_broker_conn_id="pinot_target")
        rows = hook.get_records("SELECT 1")
        print("pinot up:", rows)

    query()


pinot_smoke()
```

```yaml
# leoflow.yaml
name: pinot_smoke
schedule: null
image:
  python: "3.11"
  requirements:
    - apache-airflow-providers-apache-pinot
connectors: [pinot]
```

## Security notes

- **Secrets in logs**: never `print()` the URI itself if it carries a
  password. Log host + port + schema only.
- **TLS in transit**: front the broker with HTTPS and set the endpoint in
  **Extra** accordingly.
- **gRPC channel (agent ↔ control plane)**: Connections are only served
  over an authenticated channel (see #58 + ADR 0021).

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery (`AIRFLOW_CONN_<CONN_ID>`).
- #142 — connector cookbook umbrella.
