# Cassandra connection

Connect a task to an Apache Cassandra cluster (the `CassandraHook`) over a
managed Leoflow Connection. The keyspace lives in the Schema field.

## URI shape

```
cassandra://<login>:<password>@<host>:<port>/<keyspace>
```

The control plane builds this URI from the Connection's fields. Reserved
characters in the password (e.g. `@`, `:`, `/`) are percent-escaped by the
URI builder; `CassandraHook` un-escapes them back. The reserved-character
round-trip is pinned by
`TestCassandraConnectionURIShapeIntegration` (in `internal/storage/`).

## Fields the UI asks for

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `cassandra_target`. Exported as `AIRFLOW_CONN_CASSANDRA_TARGET` (uppercased). |
| Conn Type | yes | `cassandra`. |
| Host | yes | A contact point. For a multi-node cluster, pass extra hosts in **Extra** (`{"hosts":["n2","n3"]}`). |
| Schema | yes | The keyspace. |
| Login | yes | The Cassandra role. |
| Password | yes | Stored encrypted at rest (ADR 0019). |
| Port | optional | Defaults to `9042`. |
| Extra | optional | JSON — e.g. `{"load_balancing_policy":"DCAwareRoundRobinPolicy"}`. Encrypted at rest. |

## Example DAG

The provider import must live **inside** the task body — a top-level
provider import fails compilation in the parser sidecar.

```python
from datetime import datetime
from airflow.decorators import dag, task


@dag(schedule=None, start_date=datetime(2024, 1, 1), catchup=False)
def cassandra_smoke():
    @task
    def query():
        from airflow.providers.apache.cassandra.hooks.cassandra import CassandraHook

        hook = CassandraHook(cassandra_conn_id="cassandra_target")
        session = hook.get_conn()
        row = session.execute("SELECT release_version FROM system.local").one()
        print("cassandra up:", row.release_version)

    query()


cassandra_smoke()
```

```yaml
# leoflow.yaml
name: cassandra_smoke
schedule: null
image:
  python: "3.11"
  requirements:
    - apache-airflow-providers-apache-cassandra
connectors: [cassandra]
```

## Security notes

- **Secrets in logs**: never `print()` the URI itself — it carries the
  password. Log host + port + keyspace only.
- **TLS in transit**: pass SSL options in **Extra**; `CassandraHook`
  honours them via the driver's `ssl_context`.
- **gRPC channel (agent ↔ control plane)**: Connections are only served
  over an authenticated channel (see #58 + ADR 0021).

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery (`AIRFLOW_CONN_<CONN_ID>`).
- #142 — connector cookbook umbrella.
