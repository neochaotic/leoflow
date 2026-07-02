---
title: Leoflow v0.1.0 — run your Airflow DAGs on a Go control plane (no Airflow in the hot path)
published: true
description: 'Write standard Apache Airflow 3.2 DAGs in Python; Leoflow parses them with a dependency-free shim, runs each task as its own pod, and serves the Airflow UI — with a Go control plane that has no GIL and no Airflow in the scheduling path. v0.1.0 ships the shim, 86 connectors, generic provider operators + sensors, and a resilient local Lite edition.'
tags: 'airflow, go, dataengineering, kubernetes'
series: Building Leoflow
id: 4014552
date: '2026-06-28T20:09:06Z'
---

> **TL;DR** — Leoflow `v0.1.0` is the first stable release. You write **standard
> Apache Airflow 3.2 DAGs in Python**; Leoflow's parser turns them into an immutable
> `dag.json` using a **structural shim** that imports *zero* Airflow, the scheduler
> (Go, no GIL) runs **one pod per task**, and the real Airflow provider operator
> executes *in the pod* — so the control plane never imports Airflow, but your tasks
> get full provider fidelity. Ships with **86 connection types**, **generic provider
> operators + sensors** (including reschedule-mode), and a **Docker-free local Lite
> edition** that self-heals. GitHub: **[neochaotic/leoflow](https://github.com/neochaotic/leoflow)**.

---

## The bet

Airflow's execution model is right: a DAG of tasks, each task in its own pod
(`KubernetesExecutor` proved it). What's slow is the **Python control plane** — a
scheduler that imports your DAGs (and all their dependencies) into a GIL-bound
process, re-parses them constantly, and turns "add a provider" into a dependency-hell
negotiation across every DAG.

Leoflow keeps the model and rewrites the control plane in **Go**. No GIL. No Airflow
in the scheduling path. Each DAG is its **own container image**, so dependencies are
the DAG's problem, not the platform's. And the public API speaks **Airflow 3.2**, so
the **real Airflow UI** runs on top, unmodified.

The catch: if the control plane is Go and never imports Airflow, how does it read a
DAG written against the Airflow SDK? That's the shim — and it's the most interesting
piece of v0.1.0.

Here's the shape of it — your DAG becomes an immutable artifact, and a Go control
plane schedules it onto a pod per task:

![Leoflow architecture — dag.py compiles to dag.json + image, a Go control plane schedules one pod per task](https://raw.githubusercontent.com/neochaotic/leoflow/main/articles/assets/architecture.png)

## You write real Airflow DAGs

No new DSL. This is a Leoflow DAG:

```python
from airflow.sdk import DAG, task
from airflow.providers.standard.operators.bash import BashOperator

with DAG("sales", schedule="@daily"):
    pull = BashOperator(task_id="pull", bash_command="echo '[1, 2, 3]' > /tmp/raw.json")

    @task
    def transform() -> int:
        import json
        return len(json.load(open("/tmp/raw.json")))

    pull >> transform()
```

`@task`, `>>`, `schedule`, trigger rules, fan-in/fan-out, `PythonOperator`,
`BashOperator` — the constructs you already know. You compile it:

```bash
leoflow compile ./sales   # → dag.json + a container image
```

## How the shim works (`dag.py → dag.json`, no Airflow imported)

Here's the trick (ADR 0024). The parser **exec's your `dag.py`** — but with a
**structural stand-in for `airflow`** on the import path. Pure standard library,
zero third-party deps. It reproduces *exactly* the attribute surface the compiler
reads, and nothing else:

![The shim flow — dag.py is exec'd under a structural stand-in for airflow; DAG/operators register into COLLECTED, which the compiler turns into dag.json](https://raw.githubusercontent.com/neochaotic/leoflow/main/articles/assets/shim.png)

Two consequences fall straight out of this design:

- **An unsupported construct can't be faked.** A `from airflow.providers.foo...`
  that the shim doesn't model raises `ModuleNotFoundError` — which the loader turns
  into a clear *"not supported by Leoflow"* error at compile time, never a silent
  half-run. Loud beats subtle.
- **Task bodies never execute during parsing.** `@task` calls only build the graph.
  Parsing a DAG can't trigger its side effects — the thing that makes Airflow's
  DAG-parsing both slow and dangerous.

So the control plane gets the graph **without importing Airflow or installing a
single provider**.

### …but the *real* operator runs in the pod

Airflow's ecosystem is **1,500+ operators**; modeling each in the shim would be a
treadmill. So Leoflow splits them (ADR 0040):

- **A native fast path** for the hottest few — `bash`, `python`, `http`, `empty` —
  which Leoflow runs with its *own* Go/runtime code. No Airflow in the pod at all;
  this is the "no Python in the hot path" part. A deliberate, growing whitelist.
- **A generic path** for the long tail. The shim's **meta-path finder** synthesizes
  *any* `airflow.providers.<x>.{operators,sensors,transfers}.<Class>` on demand and
  **captures** it — recording the operator's **real dotted class path** and its
  **constructor kwargs**, without the provider installed in the parser.

Then, at runtime, inside the task's own pod (where the provider *is* installed, via
the image), the agent does essentially:

```python
import_string(dotted_class)(**captured_kwargs).execute(context)
```

The genuine Airflow operator runs, with the genuine provider, in an isolated pod —
while the control plane that scheduled it never imported either. **Compile-time:
structure, dependency-free. Run-time: the real thing, in a pod.** That seam is the
whole design.

![Native fast path vs generic capture — bash/python/http run natively; everything else is captured by class+kwargs and the real operator runs in the pod](https://raw.githubusercontent.com/neochaotic/leoflow/main/articles/assets/operator.png)

## Operators, sensors & 86 connectors

**A provider operator is just an import.** Anything outside the native fast path is
captured at compile time and runs *for real* in the pod — e.g. a SQL rollup against
your warehouse, its `conn_id` resolving to a managed connection:

```python
from airflow.providers.common.sql.operators.sql import SQLExecuteQueryOperator
from airflow.sdk import DAG

with DAG("rollup", schedule="@daily", tags=["example"]):
    SQLExecuteQueryOperator(
        task_id="daily_rollup",
        conn_id="warehouse",   # a managed Connection (created in the UI)
        sql="insert into rollup select day, count(*) from events group by day",
    )
```

(`BashOperator`/`PythonOperator`/`HttpOperator` are the *native* path — Leoflow runs
those itself, no Airflow in the pod. Everything else takes the generic path above.)

> **Run it locally:** in `leoflow lite`, add a `warehouse` Postgres connection
> (Admin → Connections) plus `events`/`rollup` tables, then trigger the DAG — the
> real `SQLExecuteQueryOperator` resolves the connection and writes the rollup. This
> exact path is validated end-to-end.

**Providers as a one-liner.** A DAG declares what it needs; `connectors:` is sugar
(ADR 0038) that expands to the `apache-airflow-providers-*` packages and bakes them
into *that DAG's* image — no shared worker, no platform-wide dependency vote:

```yaml
# leoflow.yaml
dag_id: sales
connectors: [postgres, http]      # → providers baked into THIS dag's image
```

**86 connection types**, generated from real Airflow (ADR 0039) so the connection
forms match field-for-field, are available in the UI. A managed connection is
delivered to the task pod as `AIRFLOW_CONN_<ID>` — the credential never lives in the
image:

```python
@task
def load(rows: list[tuple]) -> None:
    import os, psycopg2
    dsn = os.environ["AIRFLOW_CONN_PG_TARGET"]   # a managed Connection, injected in-pod
    with psycopg2.connect(dsn) as conn:
        conn.cursor().executemany("INSERT INTO cats VALUES (%s, %s)", rows)
```

**Sensors, including reschedule mode.** A `mode='reschedule'` sensor **releases its
pod** between checks:

```python
from airflow.providers.standard.sensors.date_time import DateTimeSensor

DateTimeSensor(task_id="wait_until_six", target_time="{{ ds }}T06:00:00+00:00",
               mode="reschedule")
```

A sensor waiting six hours isn't holding a pod for six hours: each not-ready poke
surfaces `up_for_reschedule`, frees the pod, and is re-dispatched when it's time to
check again.

## Lite: zero to a local grid, Docker-free

`leoflow lite` is the local edition — no Kubernetes, no cloud:

```bash
leoflow lite --postgres managed     # embedded Postgres, no Docker required
```

It scaffolds a starter DAG, brings up an embedded Postgres, starts the control
plane, and serves the **Airflow 3.2 UI** at `localhost:8088` — hot-reloading on every
save. v0.1.0 hardened it to be resilient:

- **Docker wedged? It keeps working.** If the Docker daemon is present but
  unresponsive, Lite falls back to the managed (Docker-free) Postgres instead of
  failing on `docker compose up`.
- **It self-heals its state.** Reusing a metadata DB used to leave "ghost" DAGs and
  stale import errors you couldn't remove from the UI. Now, on boot, Lite reconciles
  the registered DAGs against your workspace — deregistering what's gone and clearing
  orphan import errors — fail-safe (it never wipes on an unreachable control plane).
  It's gated by an end-to-end CI test so it can't silently regress.

## Leoflow vs an Airflow control plane

| | Airflow | Leoflow v0.1.0 |
|---|---|---|
| Control plane | Python (GIL) | Go (no GIL) |
| DAG parsing | imports Airflow + your deps; bodies can run | structural shim, zero deps, bodies never run |
| Provider deps | shared, platform-wide | per-DAG image (`connectors:`) |
| Operator fidelity | real | **real** (runs in the pod via captured class+kwargs) |
| Task isolation | pod-per-task (K8s executor) | pod-per-task |
| DAG artifact | mutable in the dagbag | immutable `dag.json` + image |
| UI | Airflow UI | the **same** Airflow 3.2 UI |
| Local dev | needs the stack | `leoflow lite`, Docker-free |

## Why use it

- **You already write Airflow DAGs** — keep them. The shim reads standard
  `airflow.sdk`; your operators run for real in the pod.
- **You're tired of dependency hell** — each DAG owns its image; adding a provider to
  one DAG never touches another.
- **You want the control plane off the critical path** — Go, no GIL, no DAG imports,
  no parse-time side effects.
- **You want the Airflow UI without the Airflow scheduler** — v0.1.0 serves the real
  3.2 UI on a Go core.
- **You want a real local loop** — `leoflow lite`, no Kubernetes, that doesn't fall
  over when your Docker does.

## Status

v0.1.0 is the first **stable** release (the `v0.1.0-rc.N` series soaked and promoted —
SemVer carries the maturity; no alpha/beta). It ships the shim, 86 connectors,
generic provider operators + sensors (reschedule included), the resilient Lite
edition, and the embedded Airflow 3.2 UI. dbt-native rendering is next (v0.1.1).

**Try it in 30 seconds:**

```bash
curl -fsSL https://raw.githubusercontent.com/neochaotic/leoflow/main/install.sh | sh
leoflow lite
```

**→ [github.com/neochaotic/leoflow](https://github.com/neochaotic/leoflow)** — point
`leoflow lite` at a DAG and watch it light up the grid. Tell us where it bites.

Apache 2.0. Thanks for reading.
