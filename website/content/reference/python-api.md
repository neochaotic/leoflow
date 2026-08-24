---
title: Python runtime API
linkTitle: Python runtime
weight: 40
description: The leoflow_runtime package that runs your task callable inside the container and bridges its return value to XCom.
---

The `leoflow_runtime` package runs **your task callable** inside the container and
bridges its return value to XCom. It is installed in the DAG image (and the dev
venv); your `dag.py` uses the **Apache Airflow Task SDK** (`from airflow.sdk import
DAG, task`), and the agent invokes `leoflow_runtime` to execute the callable.

{{% alert title="Generated content — later phase" color="info" %}}
The rendered docstring reference for `leoflow_runtime.runner` and
`leoflow_runtime.xcom` is produced by a Python docstring generator (the MkDocs site
used `mkdocstrings`). Wiring an equivalent generator into the Hugo build — the
Python API "sidecar" — is a **later migration phase**; this page is the section stub
that reserves the URL and structure.
{{% /alert %}}

Modules that will be documented here:

- `leoflow_runtime.runner` — executes the task callable and captures its result.
- `leoflow_runtime.xcom` — reads and writes XCom from inside the task container.
