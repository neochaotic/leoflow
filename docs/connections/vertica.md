# Vertica connection

Connect a task to a Vertica analytics database (the `VerticaHook`) over a
managed Leoflow Connection. The database lives in the Schema field.

## URI shape

```
vertica://<login>:<password>@<host>:<port>/<database>
```

The control plane builds this URI from the Connection's fields. Reserved
characters in the password (e.g. `@`, `:`, `/`) are percent-escaped by the
URI builder; `VerticaHook` un-escapes them back. The reserved-character
round-trip is pinned by `TestVerticaConnectionURIShapeIntegration` (in
`internal/storage/`).

## Fields the UI asks for

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `vertica_target`. Exported as `AIRFLOW_CONN_VERTICA_TARGET` (uppercased). |
| Conn Type | yes | `vertica`. |
| Host | yes | DNS name or IP. |
| Schema | yes | The database name. |
| Login | yes | The Vertica user. |
| Password | yes | Stored encrypted at rest (ADR 0019). |
| Port | optional | Defaults to `5433`. |
| Extra | optional | JSON — e.g. `{"ssl":true}` to force TLS. Encrypted at rest. |

## Example DAG

The provider import must live **inside** the task body — a top-level
provider import fails compilation in the parser sidecar.

```python
from datetime import datetime
from airflow.decorators import dag, task


@dag(schedule=None, start_date=datetime(2024, 1, 1), catchup=False)
def vertica_smoke():
    @task
    def query():
        from airflow.providers.vertica.hooks.vertica import VerticaHook

        hook = VerticaHook(vertica_conn_id="vertica_target")
        rows = hook.get_records("SELECT version()")
        print("vertica up:", rows)

    query()


vertica_smoke()
```

```yaml
# leoflow.yaml
name: vertica_smoke
schedule: null
image:
  python: "3.11"
  requirements:
    - apache-airflow-providers-vertica
connectors: [vertica]
```

## Security notes

- **Secrets in logs**: never `print()` the URI itself — it carries the
  password. Log host + port + database only.
- **TLS in transit**: set `{"ssl":true}` in **Extra** to force TLS.
- **gRPC channel (agent ↔ control plane)**: Connections are only served
  over an authenticated channel (see #58 + ADR 0021).

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery (`AIRFLOW_CONN_<CONN_ID>`).
- #142 — connector cookbook umbrella.
