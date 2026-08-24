---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /python-api.html
# --- end AUTO redirect aliases ---
title: Python runtime API
linkTitle: Python runtime
weight: 40
description: The leoflow_runtime package that runs your task callable inside the container and bridges its return value to XCom.
---

The `leoflow_runtime` package runs **your task callable** inside the container and
bridges its return value to XCom. It is installed in the DAG image (and the dev
venv); your `dag.py` uses the **Apache Airflow Task SDK** (`from airflow.sdk import
DAG, task`), and the agent invokes `leoflow_runtime` to execute the callable.

{{% alert title="Open the rendered reference" color="primary" %}}
The full docstring reference (every module, class, and function) is rendered from
source by [`pdoc`](https://pdoc.dev/) and published as a self-contained subsite:

**[→ Open the Python runtime API reference](/python-api/)**
{{% /alert %}}

It is a **sidecar**: pdoc's output does not share this site's Docsy theming, so it
is served verbatim under `/python-api/` and linked (not embedded) from here. The
generator is `website/scripts/gen-python.sh`, run on every build so the reference
never drifts from the package.

Modules documented there:

- `leoflow_runtime` — the package entry points (`run`, `xcom_pull`).
- `leoflow_runtime.runner` — executes the task callable and captures its result.
- `leoflow_runtime.xcom` — reads and writes XCom from inside the task container.
- `leoflow_runtime.dbt` — the dbt adapter bridge.
