# InfluxDB connection

Connect a task to an InfluxDB time-series database (the `InfluxDBHook`) over a
managed Leoflow Connection. InfluxDB 2.x authenticates with an **org + token**
carried in Extra — not login/password.

## URI shape

```
influxdb://<host>:<port>?__extra__=<json>
```

The control plane builds this URI from the Connection's fields. The
`__extra__` query parameter carries the org and token JSON, percent-escaped;
`InfluxDBHook` recovers it. The token round-trip through `__extra__` is pinned
by `TestInfluxdbConnectionURIShapeIntegration` (in `internal/storage/`).

| Connection fields | URI |
|---|---|
| host=`warehouse.example.com` port=`8086` extra=`{"org":"acme","token":"…"}` | `influxdb://warehouse.example.com:8086?__extra__=%7B…%7D` |

## Fields the UI asks for

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `influxdb_target`. Exported as `AIRFLOW_CONN_INFLUXDB_TARGET` (uppercased). |
| Conn Type | yes | `influxdb`. |
| Host | yes | DNS name or IP of the InfluxDB endpoint. |
| Port | optional | Defaults to `8086`. |
| Schema | no | Leave blank — InfluxDB 2.x uses bucket/org, not a DB schema. |
| Login | no | Not used by token auth. |
| Password | no | Not used by token auth. |
| Extra | yes | JSON with `org` and `token`, e.g. `{"org":"acme","token":"<token>"}`. Encrypted at rest (ADR 0019). |

## Example DAG

The provider import must live **inside** the task body — a top-level
provider import fails compilation in the parser sidecar.

```python
from datetime import datetime
from airflow.decorators import dag, task


@dag(schedule=None, start_date=datetime(2024, 1, 1), catchup=False)
def influxdb_smoke():
    @task
    def query():
        from airflow.providers.influxdb.hooks.influxdb import InfluxDBHook

        hook = InfluxDBHook(conn_id="influxdb_target")
        client = hook.get_conn()
        print("influxdb up:", client.ping())

    query()


influxdb_smoke()
```

```yaml
# leoflow.yaml
name: influxdb_smoke
schedule: null
image:
  python: "3.11"
  requirements:
    - apache-airflow-providers-influxdb
connectors: [influxdb]
```

## Security notes

- **Token in Extra**: the API token is the credential. It is encrypted at
  rest (ADR 0019) and delivered only over an authenticated gRPC channel.
- **Secrets in logs**: never `print()` the URI — its `__extra__` carries the
  token. Log host + port only.
- **gRPC channel (agent ↔ control plane)**: Connections are only served
  over an authenticated channel (see #58 + ADR 0021).

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery (`AIRFLOW_CONN_<CONN_ID>`).
- #142 — connector cookbook umbrella.
