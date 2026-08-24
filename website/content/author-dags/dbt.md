---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /dbt.html
# --- end AUTO redirect aliases ---
title: dbt projects as DAGs
linkTitle: dbt
weight: 30
description: Render a dbt project into a Leoflow DAG with native model-level tasks.
---

Leoflow runs a **dbt** project as a native DAG: it reads dbt's own
`manifest.json` and turns each dbt node (seed, model, snapshot, test) into a
Leoflow task, executed **pod-per-task** against your warehouse — no Apache
Airflow in the control plane, and no [Cosmos](https://astronomer.github.io/astronomer-cosmos/)
at runtime.

> **vs Cosmos.** Cosmos generates Airflow tasks from a dbt project by importing a
> Python library at DAG-parse time. Leoflow does the same translation in Go at
> compile time, from the same `manifest.json` — so there is no library to import,
> no profile-mapping boilerplate, and no per-run re-parse of the manifest.

There are **two ways** to bring dbt into Leoflow:

1. **The dbt project *is* the DAG** — declare it in `leoflow.yaml`. The fast path
   when a DAG is purely dbt.
2. **dbt mixed with operators** — author a `dag.py`, drop a `dbt_group()` between
   your operators. This is the Cosmos `DbtTaskGroup` capability.

---

## 1. The dbt project is the DAG

A Leoflow DAG is normally a `dag.py`. For a pure-dbt DAG there is **no Python** —
the DAG's shape comes from dbt's `ref()`/`source()` graph. You write dbt the way
you always do, and add one `leoflow.yaml`:

```
sales/                         # the DAG = a dbt project + leoflow.yaml
├── leoflow.yaml               # the only Leoflow file
├── dbt_project.yml            # dbt
├── profiles.yml               # dbt (or use a managed connection — see below)
├── seeds/raw_orders.csv
└── models/
    ├── staging/stg_orders.sql #  select … from {{ ref('raw_orders') }}
    └── marts/orders.sql       #  select … from {{ ref('stg_orders') }}
```

```yaml
# leoflow.yaml
schema_version: "1.0"
dag_id: sales
schedule: "@daily"             # optional; empty = on-demand (Lite dev loop)
owner: data-team
dbt:
  project: .                   # dir containing dbt_project.yml
  granularity: node            # node | level | folder  (see §3)
```

Compile it like any DAG:

```console
$ leoflow compile ./sales --image registry.example.com/sales:v1
Compiled ./sales -> dag.json (image registry.example.com/sales:v1, version 9f3a2c1)
```

`leoflow compile` reads the dbt manifest and emits one task per node:

| task_id | command |
|---|---|
| `raw_orders` | `dbt seed --select raw_orders` |
| `stg_orders` | `dbt run --select stg_orders` (after `raw_orders`) |
| `orders` | `dbt run --select orders` (after `stg_orders`) |
| `unique_orders_id` | `dbt test --select unique_orders_id` (after `orders`) |

You never write task dependencies — `{{ ref('stg_orders') }}` **is** the edge.

> The manifest comes from `dbt parse` (run for you in Lite on each save; baked at
> image-build time in Pro). Set `dbt.manifest: target/manifest.json` to point at a
> pre-built one.

---

## 2. Mixing dbt with operators

To run operators **before/after** your models in the same DAG, author a `dag.py`
and embed the dbt project with `dbt_group("<name>")`:

```python
# sales/dag.py
from leoflow import dbt_group
from airflow.providers.standard.operators.python import PythonOperator
from airflow.sdk import DAG

def extract(): ...
def notify(): ...

with DAG("sales", schedule="@daily"):
    pull   = PythonOperator(task_id="extract", python_callable=extract)
    models = dbt_group("transform")          # the dbt project, expanded
    ping   = PythonOperator(task_id="notify", python_callable=notify)

    pull >> models >> ping
```

```yaml
# sales/leoflow.yaml
dag_id: sales
schedule: "@daily"
dbt_groups:
  transform:                  # the name passed to dbt_group()
    project: ./transform
    granularity: level
    connection: warehouse_pg  # managed connection (see §4)
```

At compile, the operators and the dbt project become **one** `dag.json`. The dbt
tasks are namespaced under the group (`transform__stg`, `transform__orders`, …),
the group's **roots** depend on `extract`, and `notify` depends on the group's
**leaves**:

```
extract → transform__level_0 → transform__level_1 → transform__level_2 → notify
```

> **vs Cosmos.** Cosmos puts the whole configuration in the `dag.py`
> (`ProjectConfig`, `ProfileConfig`, `ExecutionConfig`, `RenderConfig` + many
> kwargs). Leoflow keeps the `dag.py` to topology and moves the config to
> `leoflow.yaml` — so the same DAG can pack differently in Lite vs Pro without
> editing Python.

---

## 3. Granularity — split vs fused

`granularity` controls how dbt nodes are packed into pods. It is a knob, not a
fixed "one model = one pod".

| `granularity` | what it does | pods |
|---|---|---|
| **`node`** *(default)* | one task per dbt node — `dbt run/seed/test --select <node>` | many — full per-model isolation, retry, and grid granularity |
| **`level`** | one task per topological wave; safe by construction | few |
| **`folder`** | one task per model folder (`staging`, `marts`, …) | few |

`node` is **split** (Leoflow's scheduler parallelizes across pods, one model per
pod). `level`/`folder` are **fused** — a group runs as a single
`dbt build --select <members>` invocation.

### Fused is parallel, not sequential

A fused group runs in **one pod**, but dbt's own engine parallelizes the group's
independent models up to its `threads` setting, respecting the internal DAG.
Measured on three independent models (3s each):

| | time |
|---|---|
| `dbt build --threads 1` | ~14s (sequential) |
| `dbt build --threads 4` | ~8s (the three run concurrently) |

So `fused` trades **per-model isolation and grid granularity** for **far fewer
pod startups**, while keeping in-pod parallelism. Rule of thumb:

- **Lite (dev loop):** `node` — cheap local subprocess, per-model visibility.
- **Pro (production):** `level`/`folder` — fewer pods at scale; `node` when you
  want strict per-model isolation and can afford the pods.

> The fused trade-off is the same one Cosmos faces; the difference is Leoflow
> exposes it as a single declarative knob.

---

## 4. The warehouse connection

dbt needs a `profiles.yml`. Leoflow resolves it for you — pick the one that fits:

### Zero-config local (Lite → duckdb)

On **Lite**, a dbt project with **no `connection:` and no `profiles.yml` of its own**
just runs — against an embedded **duckdb** file (`leoflow_local.duckdb`, in the project)
with no setup at all:

```console
$ leoflow lite            # write models, hit Trigger — that's it
```

Leoflow generates the duckdb profile transparently at both compile (`dbt parse`) and run
time, in the task's working dir — **never touching your global `~/.dbt`**. It's the
ideal way to develop and test transformations before wiring a real warehouse. Add a
`connection:` (below) or a project `profiles.yml` at any time and that wins instead — the
default only kicks in when there's nothing configured.

### Managed connection (recommended for Pro)

Set `connection:` to a Leoflow connection id. Leoflow delivers the connection to
the pod (encrypted at rest, decrypted in-pod) and the runtime **generates
`profiles.yml`** before dbt runs — **no credential is ever baked into the image**.

```yaml
dbt:
  project: .
  connection: warehouse_pg
```

```console
# create the connection once (UI, or the API)
$ curl -X POST .../api/v2/connections -d '{
    "connection_id":"warehouse_pg","conn_type":"postgres",
    "host":"db.internal","port":5432,"login":"etl","password":"…","schema":"transform"}'
```

The compiled command becomes:

```
python -m leoflow_runtime --dbt-profile warehouse_pg <profile> && dbt run --select …
```

> **vs Cosmos.** Cosmos bridges Airflow connections to dbt profiles with a
> per-warehouse `profile_mapping` class declared in Python. Leoflow does it from
> the connection automatically — one `connection:` line, zero mapping classes,
> and nothing secret in the image.

### Bring your own `profiles.yml`

Omit `connection:` and ship a `profiles.yml` in the project (it is baked into the
image). Simple for Lite; you own the credential delivery.

> Use **one or the other** — a `connection:` makes Leoflow generate the profile;
> without it, your baked `profiles.yml` is used.

**Adapters:** Postgres, Snowflake, BigQuery, Databricks (the official
`dbt-databricks` adapter, not the community one), and **duckdb** (embedded, for
zero-server local dev) are supported — Leoflow maps the managed connection to each
adapter's profile. Declare the adapter package
(`dbt-snowflake`, `dbt-bigquery`, `dbt-databricks`, …) as a dependency so it lands
in the image.

Each cloud adapter supports modern, service-account auth — Leoflow's recommended
mode for automation — alongside the legacy password/key-file mode. Everything is
driven by the connection's `extra`, so nothing secret is baked into the image, and
the connection form surfaces these fields with inline help:

| Warehouse | Recommended auth | Set in the connection | Legacy fallback |
|---|---|---|---|
| **Snowflake** | key-pair | `private_key_content` (inline PEM) or `private_key_file` (path), optional `private_key_passphrase` | `password` |
| **BigQuery** | keyless (Workload Identity / ADC) | `method: oauth` | `keyfile_dict` |
| **Databricks** | OAuth M2M (service principal) | `client_id` + `client_secret` (or `auth_type: oauth`) | access token (PAT) |

The recommended mode wins when its fields are present; otherwise the legacy mode
is used. Per-warehouse setup — required fields (`account`/`warehouse`, `http_path`,
…), example payloads, and precedence — lives in the connection reference:
[Snowflake](/connections/snowflake/), [BigQuery](/connections/gcpbigquery/),
[Databricks](/connections/databricks/).

---

## 5. Failure isolation & the build parse-gate

**A syntax error in one model does not blow up production.** dbt parses the whole
project on every invocation, so a compilation error in *any* model would, in
naive setups, break *every* task. Leoflow stops that at the **build parse-gate**:

- **Pro:** `dbt parse` runs at image-build time. A broken project **never
  produces an image** — nothing deploys.
- **Lite:** `leoflow compile` runs `dbt parse` on save — you fix it before running.

So a syntax error fails **loudly and early**, never at 5am. Baking the manifest +
`partial_parse` reinforces this: runtime pods reuse the build-time parse.

### The baked manifest, and Slim CI (`state:modified+`)

The manifest that Leoflow compiles from is `dbt parse`'s `target/manifest.json`. On
**Pro** it is produced at image-build time and copied into the DAG image (alongside
`partial_parse.msgpack`), immutable with that artifact; on **Lite** `leoflow compile`
parses on save. Leoflow reads it **at compile time** to render tasks — dbt itself
reuses the baked copy at runtime.

That baked manifest is exactly the ingredient dbt's **Slim CI**
(`dbt build --select state:modified+ --defer --state <prod-artifacts>`) needs — build
only changed models and their downstreams, deferring unchanged refs to production
relations. Leoflow does **not** yet offer this as a turnkey recipe, because two pieces
are missing:

1. **No supported way to fetch the deployed manifest** to diff against — it currently
   lives only baked inside the immutable DAG image, with no export CLI/API.
2. **The compiler never emits `--state`/`--defer`/`state:modified+`** selectors — it
   selects by node/level/folder only.

If you drive dbt yourself in CI (outside Leoflow's compilation) you can already run
Slim CI by supplying your own prior `manifest.json` as `--state`. A first-class
recipe wired to Leoflow's artifacts is tracked as a future enhancement.

**Run-time errors** (a model that compiles but fails against the warehouse) are
isolated by granularity:

- **`node`:** the failing model fails its **own pod**; independent models succeed;
  only its downstream subtree is blocked.
- **fused:** dbt still materializes the group's good models, but the group task
  fails as a unit (coarser blast radius).

### Retrying a fused group re-runs the whole group

A fused group is one `dbt build --select <members>` task. If it fails mid-way, dbt
keeps the models it already built — but **retrying the task re-runs the entire
group from scratch**, including the models that already succeeded. dbt is not
resumed from its failure point here (that would need `dbt retry`, which reads the
previous run's `target/run_results.json` — an artifact Leoflow does not yet persist
across pod attempts). On warehouses billed per compute-second, retrying a
mostly-green group re-bills the green models.

This is the flip side of the fused trade-off. If retry efficiency matters more than
pod count for a given DAG, use **`granularity: node`**: each model is its own task,
so a retry re-runs only the failed model (and its blocked downstream), exactly like
Airflow's per-task retry. Choose per DAG:

- **Expensive warehouse + flaky sources → `node`** — pay in pods, save on recompute.
- **Cheap/idempotent models at scale → `level`/`folder`** — pay a little recompute
  on the rare retry, save on pod startups.

Resumable fused retries (persisting `run_results.json` so `dbt retry` can skip the
already-built models) are tracked as a future enhancement.

---

## 6. Where config lives (two YAML worlds)

| file | owner | describes |
|---|---|---|
| `dbt_project.yml`, `profiles.yml`, `models/**/*.yml` | **dbt** | the transformation (models, materializations, tests, connection) |
| `leoflow.yaml` | **Leoflow** | the DAG (id, schedule, granularity, packing, managed connection) |

They never overlap: `dbt_project.yml` never mentions schedules/pods; `leoflow.yaml`
never mentions SQL. Author your **models** in your dbt tooling (VS Code + dbt
Power User, dbt Cloud IDE); Leoflow only adds orchestration and packing.

---

## 7. Adapter assurance — what's verified how

Leoflow generates each warehouse's `profiles.yml`. How thoroughly that generation
is tested varies by adapter:

| Adapter | Profile shape | Live query in CI |
|---|---|---|
| **postgres** | contract + live | ✅ real dbt on k3d (`e2e-dbt`) |
| **duckdb** | contract + live | ✅ real dbt on Lite (`e2e-lite-dbt`) |
| **snowflake** | ✅ contract-tested | ⚠️ hand-verified only |
| **bigquery** | ✅ contract-tested | ⚠️ hand-verified only |
| **databricks** | ✅ contract-tested | ⚠️ hand-verified only |

**Contract-tested** means CI feeds Leoflow's emitted profile through the *real* dbt
adapter's own credential parsing (`dbt-adapter-contracts` job): correct field
names, alias resolution, required fields, and each auth mode (Snowflake key-pair,
BigQuery keyless, Databricks OAuth M2M) are validated against the actual adapter —
without connecting to a warehouse. What it does **not** prove is that a real query
succeeds against your account.

**Live-query verification for the cloud adapters is maintainer-owned** — it needs
real warehouse accounts + CI secrets. The template is `test/e2e/dbt-connection-e2e.sh`
(today it runs against a local Postgres warehouse); pointing it at a real Snowflake/
BigQuery/Databricks account, gated on org secrets, is the remaining step.

---

## Reference

`leoflow.yaml` `dbt:` (whole-DAG) and each `dbt_groups:` entry (embedded) accept:

| field | meaning |
|---|---|
| `project` | directory containing `dbt_project.yml` |
| `granularity` | `node` \| `level` \| `folder` (default `node`) |
| `manifest` | optional pre-built `manifest.json` path (project-relative); empty runs `dbt parse` |
| `connection` | managed Leoflow connection id; empty = bring-your-own `profiles.yml` |
| `schedule` | *(whole-DAG `dbt:` only)* cron/preset; empty = on-demand |

## Cosmos at a glance

| | Cosmos | Leoflow |
|---|---|---|
| Where the translation runs | Python lib at DAG-parse time | Go at compile time |
| Manifest | re-parsed per `DbtDag` init | parsed once, baked, reused |
| Config | in the `dag.py` (4 config objects) | in `leoflow.yaml` (declarative) |
| Connection → profile | per-warehouse `profile_mapping` class | one `connection:` line, generated in-pod |
| Pod packing | execution mode + per-model | `granularity` knob (split/fused) |
| Mixing with operators | `DbtTaskGroup` in a DAG | `dbt_group()` in a `dag.py` |
