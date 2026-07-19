---
title: 'Run dbt as pods, not as a monolith — dbt-native orchestration without Cosmos'
published: false
description: 'Leoflow v0.1.1 turns a dbt project into a real DAG: one pod per model, mixed with your operators, with the warehouse connection generated in-pod and never baked into the image. Here''s the architecture, the trade-offs, and how it compares to Cosmos.'
tags: 'dbt, dataengineering, airflow, go'
cover_image: 'https://raw.githubusercontent.com/neochaotic/leoflow/main/docs/assets/screenshots/etl-graph.png'
series: Building Leoflow
id: 4181914
---

> **TL;DR** — Leoflow `v0.1.1` runs a **dbt project as a native DAG**: it reads
> dbt's own `manifest.json` and turns each node (seed, model, snapshot, test) into a
> Leoflow task, executed **pod-per-model** against your warehouse. No Apache Airflow
> in the control plane, no [Cosmos](https://astronomer.github.io/astronomer-cosmos/)
> at runtime. You can run dbt as the whole DAG, or **mix it with operators** in one
> graph; pack models into **fewer pods** with a single knob; and deliver warehouse
> credentials through a **managed connection** that generates `profiles.yml` *in the
> pod* — nothing secret ever lands in the image. Postgres, Snowflake, BigQuery, and
> Databricks. GitHub: **[neochaotic/leoflow](https://github.com/neochaotic/leoflow)**.

---

## The thing nobody enjoys: dbt + an orchestrator

dbt is great at *transforming* data. It is not an orchestrator — it doesn't know
about schedules, retries, pods, or the other twelve things that have to happen
around your models. So everyone bolts dbt onto Airflow. And there are exactly two
unhappy ways to do it:

1. **One big `dbt build` task.** Simple, and you lose everything: no per-model
   retry, no per-model observability, no parallelism the orchestrator can see. One
   model fails at 4 AM and you re-run the whole project.
2. **Cosmos.** It generates one Airflow task per model — the right granularity. But
   it does it by importing a Python library *at DAG-parse time*, re-parsing the dbt
   manifest on every scheduler heartbeat, with the whole configuration (`ProjectConfig`
   + `ProfileConfig` + `ExecutionConfig` + `RenderConfig`) stuffed into your `dag.py`,
   and a per-warehouse "profile mapping" class to bridge Airflow connections to dbt
   profiles. On a pod-per-task executor, that re-parse is a documented performance
   tax.

Leoflow takes the granularity Cosmos proved is correct — **one pod per model** — and
removes the cost.

## The idea: the manifest is already the graph

dbt compiles your project into `target/manifest.json` — the canonical DAG: every
node, its type, and its `depends_on` edges. Cosmos reads that file. **Leoflow reads
the same file, in Go, at compile time**, and emits flat tasks:

```
leoflow compile ./sales
  → dbt's manifest.json
  → for each node: a bash task `dbt run/seed/test --select <node>`, wired by depends_on
  → dag.json (the immutable artifact)
```

No library to import. No Airflow in the parser. No manifest re-parse on the hot
path — it's parsed once, baked into the image, and reused.

## Architecture in one picture

```
authoring                compile (Go)                runtime (k8s)
─────────                ────────────                ─────────────
dbt project   ─┐
(models/*.sql) ├─► leoflow compile ─► dag.json ─► scheduler ─► pod per model
leoflow.yaml  ─┘     reads manifest.json              │           ├─ leoflow-agent
(dag.py)              renders flat tasks               │           └─ dbt run --select X
                      generates the image              │              ↕
                                                        └────────────► shared warehouse
```

The model SQL stays pure dbt (author it in your dbt tooling). `leoflow.yaml` adds
the orchestration. The control plane is Go; the only Python is `dbt` itself, inside
the task pod.

## Two ways to author

**1. The dbt project *is* the DAG.** No Python — just a `leoflow.yaml`:

```yaml
dag_id: sales
schedule: "@daily"
dbt:
  project: .
  granularity: node      # one pod per model
  connection: warehouse_pg
```

**2. dbt mixed with operators.** Author a `dag.py` and drop a `dbt_group()` between
your operators — the Cosmos `DbtTaskGroup` capability, but the config stays in YAML:

```python
from leoflow import dbt_group
from airflow.providers.standard.operators.python import PythonOperator
from airflow.sdk import DAG

with DAG("sales", schedule="@daily"):
    extract  = PythonOperator(task_id="extract", python_callable=pull)
    models   = dbt_group("analytics")     # the dbt project, expanded into pods
    notify   = PythonOperator(task_id="notify", python_callable=ping)
    extract >> models >> notify
```

At compile, the operators and the dbt project merge into **one** `dag.json`: the dbt
tasks are namespaced (`analytics__stg`, …), the group's roots depend on `extract`,
and `notify` depends on the group's leaves. One graph, one grid, pod-per-task across
all of it.

## The pod-packing knob: split vs fused

One pod per model is great for isolation — and a lot of pods. So `granularity` is a
knob:

| `granularity` | pods | when |
|---|---|---|
| `node` (split) | one per model | dev loop, strict per-model isolation/retry |
| `level` / `folder` (fused) | one per wave / folder | production at scale — far fewer startups |

The surprise: **fused is not sequential.** A fused group runs `dbt build --select
<members>` in one pod, and dbt's own engine parallelizes the independent models up
to its `threads`. Measured on three independent 3s models: `--threads 1` → 14s,
`--threads 4` → 8s. You get fewer pods *and* in-pod parallelism — the failure domain
is the only thing that gets coarser.

## The credential trick: no `profiles.yml` in the image

This is where Leoflow pulls ahead of Cosmos's `profile_mapping` boilerplate. Set a
managed connection:

```yaml
dbt:
  connection: warehouse_pg
```

The compiled command becomes:

```
python -m leoflow_runtime --dbt-profile warehouse_pg <profile> && dbt run --select …
```

The connection is stored **encrypted** in the control plane and delivered to the pod
over the agent seam; the runtime **generates `profiles.yml` in the pod** from it,
then dbt runs. **No credential is ever baked into the image.** We test this
adversarially: the e2e bakes a *deliberately broken* `profiles.yml`; the tasks still
succeed, proving the managed connection — not the baked file — was used.

Postgres, Snowflake, BigQuery, and Databricks (the official `dbt-databricks`
adapter) are mapped from the connection; the adapter-specific bits (account,
`keyfile_dict`, `http_path`) come from the connection's `extra`.

## A footgun Leoflow disarms: the syntax error that stops everything

dbt re-parses the *whole project* on every invocation. So a compilation error in one
unimportant model would, naively, break **every** task's pod. Leoflow stops it at the
**build parse-gate**: `dbt parse` runs at compile/build time, so a broken project
**never produces an artifact** — you find out loudly and early, not at 4 AM. Run-time
errors (a model that compiles but fails against the warehouse) stay isolated: at
`node` granularity the bad model fails its own pod, and the rest keep going.

## Leoflow vs Cosmos, at a glance

| | Cosmos | Leoflow |
|---|---|---|
| Translation | Python lib at DAG-parse time | Go at compile time |
| Manifest | re-parsed per `DbtDag` init | parsed once, baked, reused |
| Config | 4 config objects in `dag.py` | declarative `leoflow.yaml` |
| Connection → profile | per-warehouse `profile_mapping` class | one `connection:` line, generated in-pod |
| Credential in image | your problem | never baked |
| Pod packing | execution mode | `granularity` knob (split/fused, parallel) |
| Mixing with operators | `DbtTaskGroup` | `dbt_group()` |
| Control plane | Airflow (Python) | Go |

## Why use it

- **You already write dbt.** Nothing changes in how you write models — `ref()` is the
  edge. You add one `leoflow.yaml`.
- **You want per-model pods without the Cosmos tax** — no parse-time library, no
  manifest re-parse, no profile-mapping classes.
- **You care about secrets** — the warehouse credential lives in the control plane,
  encrypted, and is materialized in the pod, never in the image.
- **You need to mix** — a Python extract before, a Slack ping after, dbt models in the
  middle, all in one graph.
- **You want to dial cost** — flip `granularity` from `node` to `level` and trade a
  swarm of pods for a few, keeping parallelism.

## Status

dbt support is `v0.1.1` on Leoflow. Validated end-to-end on real k3d clusters:
pod-per-model execution, the operator-mixing merge, and the managed-connection
profile generation (postgres), with the four adapter mappings unit-tested. It's the
same control plane that already runs the Airflow 3.2 UI and the generic provider
operators.

**→ [github.com/neochaotic/leoflow](https://github.com/neochaotic/leoflow)** — write
a dbt DAG, point `leoflow compile` at it, and watch one pod per model light up the
grid. Tell us where it bites.

Apache 2.0. Thanks for reading.
