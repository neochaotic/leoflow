# DAG authoring

A Leoflow DAG is two files in a project directory, compiled into an **immutable
artifact** (`dag.json` + a container image, versioned together — ADR 0003).

```
dags/my_pipeline/
  dag.py         # real Apache Airflow SDK 3.2.x code
  leoflow.yaml   # Leoflow deploy config (not an Airflow file)
```

Scaffold one with `leoflow init dags/my_pipeline`.

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

- **Task types** (detected by operator class name): `Python` (incl. TaskFlow
  `@task`), `Bash`, `Http`.
- **Trigger rules**: `all_success`, `all_failed`, `all_done`, `one_success`,
  `one_failed`.
- **XCom**: TaskFlow data-flow (`transform(extract())`) is resolved automatically
  into typed inputs (`xcom_input`).
- **Schedule**: cron strings and presets (`@daily`, `0 * * * *`).
- **Dependencies**: linear ordering via TaskFlow calls or `a >> b`.

### Not (yet) supported — limitations

- **Branching** (`BranchPythonOperator`, `@task.branch`): currently treated as a
  plain `python` task, **losing the branch semantics** — avoid for now (a future
  release will make this a hard compile error rather than a silent mistranslation).
- **Dynamic task mapping** (`.expand` / `.partial`), **sensors**,
  **KubernetesPodOperator**, **datasets/assets** triggers, **Jinja templating**.
- **Provider operators** (S3, Postgres, …): do the work inside a `@task` instead
  (your image already has the libraries).
- **Per-task `default_args` in `dag.py`** are ignored; use `leoflow.yaml`.

> Anything not translated is a **hard compile error** (no silent drop) — except
> branching, noted above.

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

## The loop: dev → CI artifact

**Dev** (fast iteration, isolated — see [Operating modes](operating-modes.md)):

```bash
leoflow dev dags/my_pipeline        # hot-reload at http://localhost:8088 (marked DEV)
```

Edit `dag.py` / `leoflow.yaml`, save, and it recompiles + re-registers; the same
guardrails fail fast in the terminal.

**CI** (the authoritative path) compiles + builds + pushes the immutable artifact:

```bash
leoflow compile dags/my_pipeline --image ghcr.io/org/my_pipeline:$GIT_SHA --build
leoflow push dag.json
```

The same parser + overlay + guardrails run as a CI gate, so a bad binding never
reaches production.
