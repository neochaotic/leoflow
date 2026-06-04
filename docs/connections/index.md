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

## Installing a connector's provider

A Connection only carries credentials. To *use* a connector — whether through
its Airflow hook (`PostgresHook`) or a raw driver (`psycopg2`) — the matching
Python package has to be in the image / venv. Leoflow gives you two ways to
declare that in `leoflow.yaml`, and you pick whichever fits:

=== "connectors: (the sugar)"

    ```yaml
    dag_id: my_pipeline
    connectors:
      - postgres
      - http
    ```

    Short names you don't have to memorise. At compile, each expands to its
    `apache-airflow-providers-*` package (ADR 0038), which pulls the hook **and**
    its driver transitively — so `connectors: [postgres]` is enough to use either
    `PostgresHook` or raw `psycopg2`. One line per connector, no version to recall.

=== "dependencies: (the escape hatch)"

    ```yaml
    dag_id: my_pipeline
    dependencies:
      - apache-airflow-providers-postgres==6.0.0   # pin the provider, or…
      - psycopg2-binary==2.9.10                    # …just the driver, your call
    ```

    Full control: pin an exact version, install only the driver, or add a package
    the catalog doesn't know about. Advanced users who already think in pip
    specifiers stay here.

Both lists are additive — the effective install is `expand(connectors) +
dependencies`. A name in `connectors:` that isn't in the catalog **fails the
compile** with the offender, the known list, and a pointer to `dependencies:`,
so a typo never slips through to a runtime `ModuleNotFoundError` in the task pod.

!!! tip "Import provider hooks inside the task function"
    Leoflow parses your DAG without providers installed (the parser only needs the
    DAG's *shape*). So put hook/operator imports **inside** the `@task` body, not at
    the module top level:

    ```python
    @task
    def load():
        from airflow.providers.postgres.hooks.postgres import PostgresHook  # here
        hook = PostgresHook(postgres_conn_id="pg_target")
        ...
    ```

    A provider import at the module top level fails the compile with an actionable
    message telling you to move it into the task and declare it via `connectors:`.
    The import works at runtime because `connectors:`/`dependencies:` installed the
    provider into the task image / venv.

### Connector → provider package

The curated catalog (`internal/connectors`) is the single source of truth shared
by the admin connection form, the sugar expansion, and compile validation:

| `connectors:` name | pip package |
|---|---|
| `postgres` | `apache-airflow-providers-postgres` |
| `mysql` | `apache-airflow-providers-mysql` |
| `sqlite` | `apache-airflow-providers-sqlite` |
| `mssql` | `apache-airflow-providers-microsoft-mssql` |
| `oracle` | `apache-airflow-providers-oracle` |
| `redis` | `apache-airflow-providers-redis` |
| `mongo` | `apache-airflow-providers-mongo` |
| `http` | `apache-airflow-providers-http` |
| `aws` | `apache-airflow-providers-amazon` |
| `google_cloud_platform` (alias `gcp`, `google`) | `apache-airflow-providers-google` |
| `snowflake` | `apache-airflow-providers-snowflake` |
| `ssh` | `apache-airflow-providers-ssh` |
| `ftp` | `apache-airflow-providers-ftp` |
| `sftp` | `apache-airflow-providers-sftp` |
| `kafka` | `apache-airflow-providers-apache-kafka` |

The package boundary is **not** a mechanical join of the dotted hook path
(`amazon.aws` → `amazon`; `microsoft.mssql` → `microsoft-mssql`), which is
exactly why the mapping is curated rather than derived.

When you press **Test** on a connection of a known type, the result line also
reminds you to declare the provider (`connectors: [<type>]`) — a Connection in the
UI carries credentials, but the DAG still has to install the hook to use them.

## Locally-testable (Docker / Lima)

| Type | Doc | Example DAG | Status |
|---|---|---|---|
| `postgres` | [postgres.md](postgres.md) | [examples/postgres_load](https://github.com/neochaotic/leoflow/tree/main/examples/postgres_load) | ✅ documented + automated test (#138) |
| `mysql` / `mariadb` | [mysql.md](mysql.md) | [examples/mysql_load](https://github.com/neochaotic/leoflow/tree/main/examples/mysql_load) | ✅ documented + automated test (#69, table-driven via #138) |
| `mssql` | [mssql.md](mssql.md) | [examples/mssql_load](https://github.com/neochaotic/leoflow/tree/main/examples/mssql_load) | ✅ documented + automated test (#71, table-driven via #138) |
| `sqlite` | [sqlite.md](sqlite.md) | [examples/sqlite_load](https://github.com/neochaotic/leoflow/tree/main/examples/sqlite_load) | ✅ documented + automated test (#70, dedicated test for file-path shape; Tier 1 — no service needed) |
| `redis` | [redis.md](redis.md) | [examples/redis_load](https://github.com/neochaotic/leoflow/tree/main/examples/redis_load) | ✅ documented + automated test (#73, table-driven via #138; Tier 1 — redis already in CI services) |
| `http` / `https` | [http.md](http.md) | [examples/http_load](https://github.com/neochaotic/leoflow/tree/main/examples/http_load) | ✅ documented + automated test (#75, dedicated test for `__extra__` round-trip; Tier 1 — no service needed) |

## Cloud (documented)

| Type | Doc | Example DAG | Status |
|---|---|---|---|
| `google_cloud_platform` | [google_cloud_platform.md](google_cloud_platform.md) | [examples/gcp_gcs_load](https://github.com/neochaotic/leoflow/tree/main/examples/gcp_gcs_load) | ✅ documented — **key + keyless (Workload Identity)** (#77, #56); delivery test with a synthetic key; real-cloud e2e is manual |

## Cloud (deferred past alpha)

These need provider accounts to test end-to-end; the umbrella issues are
filed but the cookbook entries are not part of the first alpha cut.

- `aws` (#76), `snowflake` (#78),
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
