# Connections cookbook

A Connection is a piece of credentialised configuration the control plane
encrypts at rest (ADR 0019) and delivers to a running task as
`AIRFLOW_CONN_<CONN_ID>` (ADR 0021). User code (Python `psycopg2`,
`requests`, your operator of choice) reads the env var and connects.

This page is the index. Each linked entry below is a focused recipe with the
URI shape, an example DAG, and how to test it.

!!! warning "Pre-alpha"
    Only a subset of Airflow's standard connection types are documented +
    tested at this stage. The list below grows as we land them.

## Locally-testable (Docker / Lima)

| Type | Doc | Example DAG | Status |
|---|---|---|---|
| `postgres` | [postgres.md](postgres.md) | [examples/postgres_load](https://github.com/neochaotic/leoflow/tree/main/examples/postgres_load) | ✅ documented + automated test (#138) |
| `mysql` / `mariadb` | [mysql.md](mysql.md) | [examples/mysql_load](https://github.com/neochaotic/leoflow/tree/main/examples/mysql_load) | ✅ documented + automated test (#69, table-driven via #138) |
| `mssql` | [mssql.md](mssql.md) | [examples/mssql_load](https://github.com/neochaotic/leoflow/tree/main/examples/mssql_load) | ✅ documented + automated test (#71, table-driven via #138) |
| `sqlite` | [sqlite.md](sqlite.md) | [examples/sqlite_load](https://github.com/neochaotic/leoflow/tree/main/examples/sqlite_load) | ✅ documented + automated test (#70, dedicated test for file-path shape; Tier 1 — no service needed) |
| `redis` | [redis.md](redis.md) | [examples/redis_load](https://github.com/neochaotic/leoflow/tree/main/examples/redis_load) | ✅ documented + automated test (#73, table-driven via #138; Tier 1 — redis already in CI services) |
| `http` / `https` | [http.md](http.md) | [examples/http_load](https://github.com/neochaotic/leoflow/tree/main/examples/http_load) | ✅ documented + automated test (#75, dedicated test for `__extra__` round-trip; Tier 1 — no service needed) |

## Cloud (deferred past alpha)

These need provider accounts to test end-to-end; the umbrella issues are
filed but the cookbook entries are not part of the first alpha cut.

- `aws` (#76), `google_cloud_platform` (#77), `snowflake` (#78),
  `oracle` (#72), `kafka` (#82), `ssh` (#79), `ftp` (#80), `sftp` (#81),
  `mongo` (#74)

## Contract every entry honours

Every entry in this cookbook ships with all three of:

1. **A doc page** (this dir) covering: URI shape, default port, Lite-vs-Pro
   caveats, security notes (TLS, auth modes).
2. **An example DAG** under `examples/<type>_*/` with its own
   `README.md` showing how to spin up the dependency (Docker / Lima), how
   to create the Connection in the UI, and the expected end-state of the
   target system.
3. **An automated test** that proves the delivery chain — the integration
   test under `internal/storage/` (companion to the example) gated by the
   `integration` build tag, runs in CI against a real Postgres.

If a layer is mocked or the e2e test is manual-only, the doc says so and a
follow-up issue covers the gap.

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery.
- #67 — connectors umbrella.
- #142 — the cookbook umbrella (this page).
