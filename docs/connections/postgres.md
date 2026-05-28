# Postgres connection

Connect a task to an external Postgres (the warehouse, an OLAP, a vendor
DB) over a managed Leoflow Connection.

## URI shape

```
postgres://<login>:<password>@<host>:<port>/<schema>
```

The control plane builds this URI from the Connection's fields. The
percent-escaping (e.g. `@` in a password becomes `%40`) is handled by the
URI builder; the receiving Python `psycopg2.connect(<URI>)` un-escapes
back to the original. Special characters in passwords are explicitly
covered by `TestConnectionDeliveryChainOfCustodyIntegration` — see #138.

## Fields the UI asks for

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `pg_target`. Exported as `AIRFLOW_CONN_PG_TARGET` (uppercased). |
| Conn Type | yes | `postgres`. |
| Host | yes | DNS name or IP. From inside a k3d cluster use `host.k3d.internal` for a host-bound port. |
| Schema | optional | The database name (Postgres calls it a "database"; Airflow calls it "schema" for historical reasons). Defaults vary by driver. |
| Login | yes | The Postgres role. |
| Password | yes | Stored encrypted at rest (ADR 0019). The UI never shows it again after save; use `leoflow lite reset-password` analogue is N/A — delete + recreate the Connection. |
| Port | optional | Defaults to `5432`. |
| Extra | optional | A JSON object — e.g. `{"sslmode":"require"}`. Stored encrypted at rest alongside the password. |

## Example DAG

[`examples/postgres_load`](https://github.com/neochaotic/leoflow/tree/main/examples/postgres_load) reads `AIRFLOW_CONN_PG_TARGET`, opens a `psycopg2`
connection, and writes 20 rows into the target. The example's
[README](https://github.com/neochaotic/leoflow/tree/main/examples/postgres_load/README.md) walks through:

1. Spinning up a target Postgres with Docker.
2. Creating the Connection in **Admin → Connections**.
3. Triggering the DAG.
4. Verifying the rows in the target.

## Lite vs Pro caveats

- **Lite (subprocess executor)** runs the task on the host. The Connection
  URI's `host` resolves against the host's name lookup — `localhost` works
  for a host-bound Docker container; `host.docker.internal` is unnecessary.
- **Lite (k3d executor)** runs the task in a pod inside the k3d cluster.
  Use `host.k3d.internal` to reach a host-bound port.
- **Pro (Kubernetes)** runs the task in a pod in your cluster. The target
  Postgres must be reachable via cluster DNS (a `Service`) or an external
  DNS that the pod's network policy permits.

## Security notes

- **TLS in transit**: pass `sslmode=require` (or `verify-full`) in
  **Extra** to force TLS. `psycopg2` honours it via the connection string.
- **Secrets in logs**: never `print()` the URI itself — it carries the
  password. If you must trace, log the host + port + login only.
- **gRPC channel (agent ↔ control plane)**: Connections are only served
  over an authenticated channel; without TLS, the server refuses to send
  secrets by default (see #58 + ADR 0021). Lite enables this for local
  use; Pro must run with TLS.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `AIRFLOW_CONN_<ID>` is missing in the task env | The Connection is on a different tenant, or the agent's gRPC channel refused secrets (insecure) | Confirm the Connection's tenant matches the task's; check the agentrpc server log for a "secrets delivery is not configured" or "insecure channel" message. |
| `psycopg2.OperationalError: connection to server at "host" failed` | Network or DNS misconfiguration | From the task's pod / host, `psql 'postgres://...'` with the same URI to isolate the network problem from the delivery contract. |
| `pq: password authentication failed` | The percent-escape round-trip is broken (regression in the URI builder) | The integration test pins this; run it against your DB
URL and file an issue with the password shape that triggered the failure. |

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery.
- #68 — Postgres connector umbrella.
- #138 — the contract test this page documents.
