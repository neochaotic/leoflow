# DAG authoring

A Leoflow DAG is two files in a project directory, compiled into an **immutable
artifact** (`dag.json` + a container image, versioned together — ADR 0003).

```
dags/my_pipeline/
  dag.py         # real Apache Airflow SDK 3.2.x code
  leoflow.yaml   # Leoflow deploy config (not an Airflow file)
```

Scaffold one with `leoflow init dags/my_pipeline`.

## Workspace layout (multi-DAG, Lite only)

> **Lite-specific.** Multi-DAG workspace discovery and the hot-reload watcher
> exist only in `leoflow lite` — the developer-mode loop. **Pro** does not have
> a "workspace"; in Pro every DAG ships as its own image-and-`dag.json` pair,
> built by CI and registered via `leoflow push dag.json`. See
> [The development → deploy lifecycle](#the-development--deploy-lifecycle).

`leoflow lite` watches a **workspace** that can hold many DAGs as sibling
subdirectories. The default workspace is `~/leoflow/` (set by `leoflow setup`).

```
~/leoflow/                       # workspace root
  recurring_print/               # one DAG project per subdir
    leoflow.yaml
    dag.py
  recurring_parallel/
    leoflow.yaml
    dag.py
  ml/
    train/                       # nested subdirs are scanned too
      leoflow.yaml
      dag.py
```

**Recommended layout (best practice, not enforced)**: name the subdirectory the
same as the `dag_id`. The binding is by `dag_id` (yaml field, or the subdir
basename when no yaml is present — see below), but matching names make the
workspace navigable by humans and grep-friendly. `~/leoflow/sales_etl/` for a
DAG named `sales_etl`.

### Discovery rules

`leoflow lite` walks the workspace and treats every subdirectory containing a
`dag.py` (with or without a `leoflow.yaml`) as a project. The scan:

- Goes **at most 5 levels deep** from the workspace root. A DAG at
  `<ws>/a/b/c/d/dag.py` is the deepest valid case. Deeper paths are skipped.
- Skips the `exclude_paths` defaults: `.git`, `__pycache__`, `*.pyc`, `.venv`,
  `venv`, and any other hidden directory (`.*`).
- Fails **loud** on a duplicate `dag_id`: if two subdirs resolve to the same
  id, lite refuses to compile any of them and prints both paths so you can
  rename one. There is no last-write-wins.

Every compile log line names the resolved config source — either the absolute
path of the `leoflow.yaml` that was loaded, or `auto-defaults: <subdir>` when
none exists. This is the one line to grep when "which config did it pick up?"
is the debugging question.

### `leoflow.yaml` is optional

A subdir with just a `dag.py` is a valid project. Leoflow synthesizes a config
with `dag_id = <subdir-basename>` and every other field filled from the schema
defaults (see [Configuration → Defaults](configuration.md#defaults)). Add a
`leoflow.yaml` when you need to pin a Python version, declare dependencies, set
per-task overrides, or change the `dag_id` to something other than the subdir
name.

## dag.py — the Airflow dialect

`dag.py` is **real Airflow SDK 3.2.x** code, imported by the Python parser via the
Airflow `DagBag`. TaskFlow and classic operators both work:

```python
from airflow.sdk import DAG, task

@task
def extract() -> dict:
    return {"rows": 100}

@task
def transform(data: dict) -> dict:
    return {"rows": data["rows"], "doubled": data["rows"] * 2}

with DAG("my_pipeline", schedule="@daily", catchup=False, tags=["etl"]):
    transform(extract())
```

### Supported

Leoflow accepts a **closed set of three task types** today. Anything outside
the set is a hard compile error (see [Not supported](#not-yet-supported--limitations)).

| Task type | Airflow operator | Where it runs | Notes |
|---|---|---|---|
| `python` | `@task` (TaskFlow) **or** `PythonOperator` | agent in a pod (Pro) / subprocess (Lite) | The general-purpose escape hatch — any provider library you `pip install` is callable from inside a `@task`. |
| `bash` | `BashOperator` | agent | Executes a shell command. |
| `http_api` | `HttpOperator` (`airflow.providers.http`) | **inline in the control plane** (no pod, no agent) | Synchronous outbound HTTP. Use it for lightweight webhooks/probes; for anything with retries, throughput, or auth headers you control, prefer `@task` + `requests` in `python`. |

- **Trigger rules**: `all_success`, `all_failed`, `all_done`, `one_success`,
  `one_failed`.
- **XCom**: TaskFlow data-flow (`transform(extract())`) is resolved automatically
  into typed inputs (`xcom_input`).
- **Schedule**: cron strings and presets (`@daily`, `0 * * * *`).
- **Dependencies**: linear ordering via TaskFlow calls or `a >> b`.

> Connector cookbooks (postgres, mysql, sqlite, redis, http) are all `python`
> tasks that read a managed Connection (`AIRFLOW_CONN_*`) injected as an env
> var — they are **not** new operator types.

### Not (yet) supported — limitations

- **Branching** (`BranchPythonOperator`, `@task.branch`), **short-circuit**
  (`@task.short_circuit`, `ShortCircuitOperator`), **virtualenv operators**
  (`PythonVirtualenvOperator`), **sensors** — `leoflow compile` **rejects** any
  of these with a clear error naming the construct ([#225](https://github.com/neochaotic/leoflow/issues/225)).
  Refusing silent mistranslation is the contract: every "skipped" branch
  would otherwise actually execute at runtime.
- **Dynamic task mapping** (`.expand` / `.partial`),
  **KubernetesPodOperator**, **datasets/assets** triggers, **Jinja templating**.
- **Provider operators** (S3, Postgres, …): do the work inside a `@task` instead
  (your image already has the libraries).
- **Per-task `default_args` in `dag.py`** are ignored; use `leoflow.yaml`.

> Anything not translated is a **hard compile error** (no silent drop).

## leoflow.yaml — deploy config

These are Leoflow concerns, **not** Airflow operator attributes (you cannot invent
kwargs on an operator — the parser imports real Airflow and would raise).

```yaml
schema_version: "1.0"
dag_id: my_pipeline
description: Daily ETL.
owner: data-eng
tags: [etl]
python_version: "3.11"
dependencies:           # pip packages baked into the image
  - pandas==2.1.0
defaults:               # DAG-level defaults (applied to every task)
  retries: 1
  retry_delay_seconds: 30
staging:                # opt-in shared per-run RWX volume (ADR 0022)
  enabled: false
tasks:                  # per-task overrides, keyed by task_id (ADR 0023)
  transform:
    retries: 3
    resources:
      requests: { cpu: "2", memory: 4Gi }
```

### Binding + override layers (ADR 0023)

Config binds to the DAG by `dag_id` and to tasks by `task_id`. Three layers,
**most specific wins**:

```
task override (tasks.<id>)  >  DAG default (defaults)  >  platform default (server)
```

- `tasks.<id>` is merged at **compile** time onto the task in `dag.json`.
- Platform defaults are applied at **dispatch** time, filling only gaps the
  artifact left empty (keeps the artifact portable across clusters).
- **`staging` is DAG-level only** — one RWX volume is shared atomically by the
  whole run, so it cannot be per-task.

### Guardrails (fail loudly, never silently)

- A `tasks:` entry naming a `task_id` absent from the DAG → **compile error**.
- A duplicate `task_id` key in the YAML → **parse error**.
- Across a monorepo, a duplicate `dag_id` is a CI-gate concern (one image per DAG).

## The development → deploy lifecycle

```mermaid
flowchart LR
  I[leoflow init] --> D[leoflow lite<br/>hot-reload loop]
  D -->|save & iterate| D
  D --> G[git push]
  G --> CI[CI: leoflow compile --build --push]
  CI --> REG[leoflow push dag.json]
  REG --> PROD[(control plane<br/>immutable artifact)]
```

### 1 · Develop (fast, isolated)

```bash
leoflow init dags/my_pipeline          # scaffold dag.py + leoflow.yaml
leoflow lite dags/my_pipeline           # cluster-mode: real pods on an isolated k3d
# or: leoflow lite --executor=subprocess dags/my_pipeline   (fastest, host venv)
```

Open <http://localhost:8088> — the UI is marked **Leoflow Lite** (silver
edition badge); log in with the admin password generated by `leoflow setup`
(or run `sudo leoflow lite reset-password` if you misplaced it). Edit
`dag.py`/`leoflow.yaml` and **save**; Leoflow recompiles, re-runs the guardrails,
and re-registers. A bad binding prints an error in the terminal **immediately**:

```text
✗ leoflow.yaml tasks: unknown task_id "transfrom"; the DAG defines [extract load transform]
```

Dev is fully isolated from Demo/Production (own database, cluster, and ports) —
**no split brain**. See [Operating modes](operating-modes.md).

### 2 · Deploy (authoritative, immutable)

On `git push`, CI compiles + builds + pushes the artifact. The **same** parser,
overlay, and guardrails run as a gate, so what you tested in Dev is what ships:

```bash
leoflow compile dags/my_pipeline --image ghcr.io/org/my_pipeline:$GIT_SHA --build --push
leoflow push dag.json
```

Full, copy-pasteable pipelines for **GitHub Actions, GitLab CI, Google Cloud
Build/Run, and generic runners** are in **[CI/CD & deploy examples](deploy.md)**.

---

See also: [Concepts & glossary](concepts.md) · [Operating modes](operating-modes.md)
· [HTTP API](api-reference.html) · [ADR 0023 — binding & overrides](adr/0023-dag-authoring-config-binding.md).
