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

The catalog is **generated** from a real Airflow install (`scripts/gen_connectors.py`
→ `internal/connectors/catalog.json`, ADR 0039), so ~86 connector types ship with
a dropdown entry, the correct standard-field behavior, and the provider-specific
**custom fields** rendered in the form (e.g. Snowflake's `account`/`warehouse`, the
GCP keyfile) — no raw-JSON typing. It is the single source of truth shared by the
admin form, the sugar expansion, and compile validation. The common head:

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
| `oracle` | [oracle.md](oracle.md) | doc-only | ✅ documented + delivery test (table-driven, oracle row); service-name in Schema |
| `mongo` | [mongo.md](mongo.md) | doc-only | ✅ documented + delivery test (table-driven, mongo row); db in Schema, `srv` for Atlas |
| `ssh` / `sftp` / `ftp` | [file-transfer.md](file-transfer.md) | doc-only | ✅ documented + delivery test (`TestFileTransferConnectionURIShapeIntegration`); key auth in Extra |

## Cloud (documented)

| Type | Doc | Example DAG | Status |
|---|---|---|---|
| `google_cloud_platform` | [google_cloud_platform.md](google_cloud_platform.md) | [examples/gcp_gcs_load](https://github.com/neochaotic/leoflow/tree/main/examples/gcp_gcs_load) | ✅ documented — **key + keyless (Workload Identity)** (#77, #56); delivery test with a synthetic key; real-cloud e2e is manual |
| `snowflake` | [snowflake.md](snowflake.md) | doc-only (needs a Snowflake account) | ✅ documented + delivery test (`TestSnowflakeConnectionURIShapeIntegration`); account/warehouse/role round-trip via `__extra__`; real-cloud e2e is manual |
| `aws` | [aws.md](aws.md) | doc-only (needs an AWS account) | ✅ documented — **keyless (IAM role) + key-based** (ADR 0035); delivery test (`TestAWSConnectionURIShapeIntegration`); real-cloud e2e is manual |
| `slack` / `slackwebhook` | [slack.md](slack.md) | doc-only (needs a Slack workspace) | ✅ documented + delivery test (`TestSlackConnectionURIShapeIntegration`); bot-token round-trip; real-workspace e2e is manual |
| `databricks` | [databricks.md](databricks.md) | doc-only (needs a Databricks workspace) | ✅ documented + delivery test (`TestDatabricksConnectionURIShapeIntegration`); host + PAT + http_path round-trip; real e2e is manual |
| `wasb` / `adls` / `azure_*` | [azure.md](azure.md) | doc-only (needs an Azure account) | ✅ documented — **managed identity + key** (ADR 0035); delivery test (`TestWasbConnectionURIShapeIntegration`); real e2e is manual |
| `spark` / `spark_sql` / … | [spark.md](spark.md) | doc-only (needs a Spark cluster) | ✅ documented + delivery test (`TestSparkConnectionURIShapeIntegration`); host:port + tuning Extra round-trip |
| `kafka` | [kafka.md](kafka.md) | doc-only (needs a Kafka cluster) | ✅ documented + delivery test (`TestKafkaConnectionURIShapeIntegration`); full client config (incl. SASL) Extra round-trip |

## More connectors (documented + delivery test)

Each ships with a chain-of-custody delivery test and a cookbook recipe; all are
doc-only (a real run needs the external account/service). Cloud families share a
provider package (e.g. redshift/athena/emr → `apache-airflow-providers-amazon`;
bigquery/cloudsql/ads → `…-google`).

| Type | Doc | Type | Doc |
|---|---|---|---|
| `redshift` | [redshift.md](redshift.md) | `trino` | [trino.md](trino.md) |
| `athena` | [athena.md](athena.md) | `presto` | [presto.md](presto.md) |
| `emr` | [emr.md](emr.md) | `jdbc` | [jdbc.md](jdbc.md) |
| `gcpbigquery` | [gcpbigquery.md](gcpbigquery.md) | `docker` | [docker.md](docker.md) |
| `gcpcloudsql` | [gcpcloudsql.md](gcpcloudsql.md) | `salesforce` | [salesforce.md](salesforce.md) |
| `google_ads` | [google_ads.md](google_ads.md) | `telegram` | [telegram.md](telegram.md) |
| `cassandra` | [cassandra.md](cassandra.md) | `discord` | [discord.md](discord.md) |
| `neo4j` | [neo4j.md](neo4j.md) | `pagerduty` | [pagerduty.md](pagerduty.md) |
| `vertica` | [vertica.md](vertica.md) | `datadog` | [datadog.md](datadog.md) |
| `influxdb` | [influxdb.md](influxdb.md) | `tableau` | [tableau.md](tableau.md) |
| `druid` | [druid.md](druid.md) | `github` | [github.md](github.md) |
| `pinot` | [pinot.md](pinot.md) | `elasticsearch` | [elasticsearch.md](elasticsearch.md) |

## The long tail

Every curated connector above is first-class (impl + delivery test + cookbook).
Beyond them, **all ~86 connector types in the generated catalog** (ADR 0039) are
usable today via `connectors:` (dropdown + sugar) — they just don't each have a
dedicated cookbook recipe yet. Any other Airflow provider works via
`dependencies:`. Recipes are added connector-by-connector as they are promoted.

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
