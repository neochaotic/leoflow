# dbt projects as Leoflow DAGs

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
    models = dbt_group("analytics")          # the dbt project, expanded
    ping   = PythonOperator(task_id="notify", python_callable=notify)

    pull >> models >> ping
```

```yaml
# sales/leoflow.yaml
dag_id: sales
schedule: "@daily"
dbt_groups:
  analytics:                  # the name passed to dbt_group()
    project: ./analytics
    granularity: level
    connection: warehouse_pg  # managed connection (see §4)
```

At compile, the operators and the dbt project become **one** `dag.json`. The dbt
tasks are namespaced under the group (`analytics__stg`, `analytics__orders`, …),
the group's **roots** depend on `extract`, and `notify` depends on the group's
**leaves**:

```
extract → analytics__level_0 → analytics__level_1 → analytics__level_2 → notify
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

dbt needs a `profiles.yml`. You have two mutually-exclusive options:

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
    "host":"db.internal","port":5432,"login":"etl","password":"…","schema":"analytics"}'
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

**Adapters:** Postgres is supported today. Snowflake, BigQuery, and Databricks
(the official `dbt-databricks` adapter) are follow-ons; the adapter package is
declared like any dependency and the connection mapping is the extension point.

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

**Run-time errors** (a model that compiles but fails against the warehouse) are
isolated by granularity:

- **`node`:** the failing model fails its **own pod**; independent models succeed;
  only its downstream subtree is blocked.
- **fused:** dbt still materializes the group's good models, but the group task
  fails as a unit (coarser blast radius).

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
