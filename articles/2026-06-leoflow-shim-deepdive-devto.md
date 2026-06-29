---
title: How we parse Apache Airflow DAGs without importing Airflow
published: true
description: 'Leoflow''s control plane is Go and never imports Apache Airflow — yet it reads standard airflow.sdk DAGs. The trick is a dependency-free structural shim that exec''s your dag.py, records the graph, and lets the real provider operator run later in the pod. Here''s the whole mechanism, end to end.'
tags: 'airflow, python, go, dataengineering'
series: Building Leoflow
id: 4014550
date: '2026-06-29T22:16:11Z'
---

> **TL;DR** — Leoflow runs a Go control plane that **never imports Apache Airflow**,
> yet compiles standard `airflow.sdk` DAGs. It does it with a **structural shim**: a
> pure-stdlib stand-in for `airflow` that the parser puts on the import path, then
> `exec`s your `dag.py` to *record* the graph (without running task bodies or
> installing a single provider). Arbitrary provider operators are **captured by
> class + kwargs** at compile time and run **for real in the task pod** at runtime.
> The parser drops from **262 MB / 136 packages** to **~44 KB / zero dependencies**.
> This is the engineering behind Leoflow v0.1.0.

---

## Where the shim sits

Before the trick, the shape of the whole system. The shim is one small box —
the **parser**, at compile time — and *everything downstream of `dag.json` is Go,*
with the real Airflow operator only ever appearing inside the task's pod:

![Leoflow architecture — leoflow compile (parser + shim) produces dag.json + image; a Go control plane (API, scheduler, executor router) dispatches one pod per task; the worker pod runs the agent over gRPC with the real provider operator; Postgres holds metadata, Redis on Pro](https://raw.githubusercontent.com/neochaotic/leoflow/main/articles/assets/architecture-full.png)

Keep that picture in mind: the **only** place Python (and Airflow) lives is the
parser sidecar and the worker pod. The scheduling path in the middle is pure Go.

## The constraint that forces the design

Leoflow's scheduler is Go — no GIL, no Python in the hot path (that's the whole
point: Airflow's Python control plane is what makes it slow). But a Leoflow DAG is a
**standard Apache Airflow 3.2 DAG**, written against `airflow.sdk`:

```python
from airflow.sdk import DAG, task
from airflow.providers.standard.operators.bash import BashOperator

with DAG("etl", schedule="@daily"):
    pull = BashOperator(task_id="pull", bash_command="echo '[1,2,3]' > /tmp/raw.json")

    @task
    def transform() -> int:
        import json
        return len(json.load(open("/tmp/raw.json")))

    pull >> transform()
```

So: **how does a control plane that never imports Airflow read a DAG written against
the Airflow SDK?** Importing real Airflow into the parser would drag in the GIL, the
dependency tree, and parse-time side effects — exactly what we're escaping. The
answer (ADR 0024) is to not import Airflow at all.

## The weight you don't carry

This isn't a micro-optimization. We measured the "just import the SDK" path —
`pip install ./parser apache-airflow-task-sdk` on Python 3.12:

| Parser dependency | Size | Third-party packages |
|---|---|---|
| Real `apache-airflow-task-sdk` (→ pulls `apache-airflow-core`) | **262 MB** | **136** |
| Leoflow's structural shim | **~44 KB** | **0** |

The 262 MB is grpc, babel, cryptography, sqlalchemy, libcst, pydantic,
opentelemetry, aiohttp… **none** of which a *parser* uses — it constructs DAG and
operator objects and reads a handful of attributes. And it can't be trimmed:
`task-sdk → core`, and `providers-standard/http → apache-airflow (meta) → core`.
Dropping Airflow makes the parser **pure Python and small enough to embed in the Go
binary** — no parser venv, no `pip` at install time, no Airflow-version coupling.

To be precise: **this is the *parser's* weight, not the whole system's.** The real
task SDK and a DAG's providers *do* get installed — in the **task image, per DAG**
(`pip install` at build time, or the Lite venv), because that's where the operator
actually runs them. Leoflow doesn't *delete* that weight; it moves it **off the
scheduling hot path** (the Go control plane never installs or imports Airflow) and
**splits it per DAG** (each image carries only its own providers — never one fat
shared worker for all 1,500). The parser is the part that gets to be ~44 KB.

## The shim: a structural stand-in for `airflow`

The parser ships a **pure-standard-library** package that *looks* like `airflow` —
same import paths, same attribute surface the compiler reads — and **nothing else**.
It's put ahead of any real Airflow on the import path, and then the parser simply
**exec's your `dag.py`**:

```python
import runpy
runpy.run_path("dag.py", run_name="__leoflow_dag__")  # `airflow` resolves to the shim
```

Running the file *builds structure*. Here's the core of the shim (paraphrased):

```python
_CURRENT: list = []     # stack of DAGs being defined
COLLECTED: dict = {}    # dag_id -> DAG, filled as each DAG context is entered

class DAG:
    def __init__(self, dag_id, schedule=None, tags=None, **kw):
        self.dag_id, self.schedule, self.task_dict = dag_id, schedule, {}
        COLLECTED[dag_id] = self
    def __enter__(self):  _CURRENT.append(self); return self
    def __exit__(self, *e): _CURRENT.pop()

class BaseOperator:
    def __init__(self, task_id, **kwargs):
        self.upstream_task_ids, self.downstream_task_ids = set(), set()
        # attach to the active DAG and store every kwarg as an attribute
        dag = kwargs.get("dag") or (_CURRENT[-1] if _CURRENT else None)
        if dag: dag.task_dict[task_id] = self
    def __rshift__(self, other):    # a >> b records the edge
        self.downstream_task_ids.add(other.task_id)
        other.upstream_task_ids.add(self.task_id)
        return other
```

![The shim flow — dag.py is exec'd under a structural stand-in for airflow; DAG/operators register into COLLECTED, which the compiler turns into dag.json](https://raw.githubusercontent.com/neochaotic/leoflow/main/articles/assets/shim.png)

`with DAG(...)` registers; constructing an operator attaches it to the active DAG and
stores its kwargs; `>>` records edges; `@task` builds the node but **never runs the
body**. The compiler then reads `COLLECTED` — exactly the attributes it needs
(`dag_id`, `tags`, `task_dict`, and per task `task_id`, `upstream_task_ids`,
`trigger_rule`, `python_callable`, `op_args`/`op_kwargs`, `bash_command`,
`endpoint`/`method`) — and emits an immutable `dag.json`.

Two properties fall straight out of this:

- **Unsupported constructs can't be faked.** A `from airflow.<thing>` the shim doesn't
  model raises `ModuleNotFoundError`, which the loader turns into a clear *"not
  supported by Leoflow"* error — at compile time, never a silent half-run.
- **Parsing has no side effects.** `@task` bodies never execute during parsing, so a
  DAG file can't trigger its own work just by being read — the thing that makes
  Airflow's dag-parsing both slow and risky.

The control plane now has the graph **without importing Airflow or installing one
provider**.

## The long tail: capture, don't reimplement

Modeling all **1,500+** provider operators in the shim would be a treadmill. So for
anything beyond the native handful (`bash`, `python`, `http`, `empty`), the shim has a
**meta-path finder** (ADR 0040) that synthesizes *any*
`airflow.providers.<x>.{operators,sensors,transfers}.<Class>` on demand. It doesn't
implement the operator — it **captures** it: records the operator's **real dotted
class path** and its **constructor kwargs**, then registers it like any node:

```python
# in the dag.py — a provider operator the shim has never heard of
from airflow.providers.common.sql.operators.sql import SQLExecuteQueryOperator
SQLExecuteQueryOperator(task_id="rollup", conn_id="warehouse", sql="insert into ...")
# captured as: { class: "airflow.providers.common.sql.operators.sql.SQLExecuteQueryOperator",
#                kwargs: { conn_id: "warehouse", sql: "insert into ..." } }
```

No provider is installed in the parser. The dotted path and kwargs are just data in
`dag.json`.

## What the shim won't do (on purpose)

The shim does *all* the structural parsing — but it draws three deliberate lines, and
each one is a feature, not a gap:

- **It never runs your task bodies.** `@task` and operators only *build structure* at
  parse time; the code inside a task runs later, in the pod. Reading a DAG can't
  trigger its work.
- **It doesn't capture hooks.** `airflow.providers.*.hooks.*` is intentionally *not*
  synthesized — a hook is a runtime client (it opens real connections), so it belongs
  inside a `@task` body that runs in the pod, never at parse time. Operators and
  sensors are captured; hooks are left to the runtime.
- **It rejects what it can't model, loudly.** Anything outside the supported surface
  and the generic provider path — an unknown `from airflow.<thing>`, or a file with no
  `dag_id` / multiple DAGs — fails at compile time with a precise message, instead of
  being silently mis-parsed.

So *"does the shim do everything?"* — it does everything **structural**, and
deliberately hands **execution** (and hooks) to the runtime. That boundary *is* the
design.

## The seam: the *real* operator runs in the pod

At runtime, inside the task's own pod — where the provider *is* installed, baked into
that DAG's image — the agent reconstructs and runs the genuine operator:

```python
import_string(dotted_class)(**captured_kwargs).execute(context)
```

The real Airflow operator executes, with the real provider, against the real
connection — while the control plane that scheduled it never imported either. Here is
the whole life of a DAG, with the shim wired to every component it touches across the
three phases:

![Drill-down — compile time: dag.py is exec'd under the shim (_core for supported ops, _generic captures provider operators as class + kwargs) while leoflow.yaml connectors pip-install the real providers into the DAG image; the shim emits dag.json. Schedule time: the Go control plane reads dag.json and dispatches a pod, never parsing. Run time: the worker pod's agent does import_string(class)(kwargs).execute(context) — the captured class + kwargs arrive via the task spec, the real provider is present in the image](https://raw.githubusercontent.com/neochaotic/leoflow/main/articles/assets/shim-drilldown.png)

Read it left to right:

1. **Compile time (no Airflow).** The shim's `_core` handles the supported ops;
   `_generic` captures every provider operator as `class + kwargs`. In parallel,
   `connectors:` in `leoflow.yaml` `pip install`s the *real* providers into the DAG's
   image. Out comes `dag.json` (graph + task types + captured class/kwargs).
2. **Schedule time (Go).** The control plane consumes `dag.json` and dispatches a pod
   per task. It **never parses** a DAG — it reads an immutable artifact.
3. **Run time (the pod).** The agent reconstructs the operator from the captured
   `class + kwargs` (delivered in the task spec) and `execute()`s it — and the real
   provider is right there in the image.

**Compile time: structure, dependency-free, in Go's world. Run time: the real Airflow
operator, in an isolated pod.** That split is the entire design — it's how you get
Airflow's ecosystem fidelity without Airflow's control plane.

## Fidelity: golden tests are the contract

A shim is only safe if it produces *exactly* what real Airflow would. So fidelity is
pinned by **golden tests**: for every shipped example, the shim's structural output is
asserted **byte-equal to the real Airflow-based compiler's** output. Drift is caught
in CI **without installing Airflow** (the golden corpus is regenerated from the real
compiler only when the supported surface changes). When we first built it, those
golden diffs caught two real fidelity gaps — duplicate `task_id` auto-suffixing and
list fan-in — which the shim now handles. There's also an escape hatch:
`LEOFLOW_PARSER_BACKEND=airflow` runs the real `DagBag` for side-by-side diffing.

## Why it matters

- **No GIL, no Airflow imports in scheduling** — the control plane stays fast and
  Go-native.
- **No dependency hell** — each DAG owns its image; the parser needs zero providers,
  and 136 transitive packages leave your supply-chain surface.
- **No parse-time surprises** — reading a DAG can't run it.
- **Full operator fidelity** — the actual provider operator runs in the pod, guarded
  by golden tests.

It's all open source (Apache 2.0): **[github.com/neochaotic/leoflow](https://github.com/neochaotic/leoflow)**.
ADR 0024 (the shim) and ADR 0040 (operator capture) have the gory details.
