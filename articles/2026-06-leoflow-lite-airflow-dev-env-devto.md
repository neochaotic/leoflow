---
title: 'Leoflow Lite: a local development environment for Apache Airflow'
published: true
description: 'Develop and test Apache Airflow DAGs locally with Leoflow Lite — no Docker, no Kubernetes, no heavy stack. Real Airflow 3.2 operators, sensors, connections and variables, hot-reload on save, the Airflow UI, and your local GCP/AWS/Azure credentials just work.'
tags: 'airflow, dataengineering, python, devtools'
series: Building Leoflow
id: 4014551
date: '2026-07-01T11:54:06Z'
---

> **TL;DR** — **Leoflow Lite is a local development environment for Apache Airflow.**
> Run `leoflow lite` and you get a Docker-free, Kubernetes-free local loop that
> compiles and runs **standard Airflow 3.2 DAGs** — real provider operators, sensors,
> connections, variables, XCom — with **hot-reload on save** and the **Airflow UI**,
> and it picks up your **local GCP / AWS / Azure credentials** automatically. Install,
> point it at a DAG, watch it run.

![Leoflow Lite — the real Apache Airflow 3.2 UI running locally: the LITE badge, an all-green pipeline run, and an in-browser IDE button, with no Docker and no Kubernetes](https://raw.githubusercontent.com/neochaotic/leoflow/main/articles/assets/lite-ui.png)

---

## What it is

If you write Apache Airflow DAGs, you know the local-dev pain: a `docker-compose` with
a scheduler, a webserver, a metadata DB, a Redis, and a worker — minutes to boot,
heavy to keep running, awkward to iterate.

**Leoflow Lite is the opposite.** One command, no containers required, and you're
editing a DAG and watching it run against the **real Airflow UI**:

```bash
# install (pin the version on the sh side of the pipe)
curl -fsSL https://raw.githubusercontent.com/neochaotic/leoflow/main/install.sh | sh

leoflow lite --postgres managed     # embedded Postgres, no Docker needed
# → scaffolds a starter DAG, serves the Airflow 3.2 UI at http://localhost:8088,
#   and hot-reloads on every save.
```

These are **standard `airflow.sdk` DAGs** — no new DSL:

```python
from airflow.sdk import DAG, task
from airflow.providers.standard.operators.bash import BashOperator

with DAG("hello", schedule="@daily"):
    greet = BashOperator(task_id="greet", bash_command="echo 'hi {{ ds }}'")

    @task
    def count() -> int:
        return 42

    greet >> count()
```

Save the file and the DAG reloads in the UI in a couple of seconds.

## What's supported

Lite runs the same Airflow-compatible engine as Leoflow's production mode:

- **Airflow 3.2 DAGs** via `airflow.sdk` — `@task`, `>>`, schedules, trigger rules,
  fan-in/fan-out.
- **Operators & sensors** — `BashOperator`/`PythonOperator` natively, and **any
  provider operator** (e.g. `SQLExecuteQueryOperator`, cloud transfer operators) runs
  for real; sensors too, including `mode='reschedule'`.
- **Connections** — create them in the UI; they're delivered to the task as
  `AIRFLOW_CONN_<ID>`.
- **Variables** — `{{ var.value.x }}` and the Admin → Variables UI.
- **XCom** between tasks, Jinja templating, the run context (`{{ ds }}`, …).

## Your local cloud credentials just work

Lite's subprocess executor runs each task as a **local process under your user**,
inheriting your environment and `$HOME`. So whatever you've already signed into with
your cloud's normal CLI is exactly what the task uses — authenticate once:

- **GCP** — `gcloud auth application-default login`
  ([Application Default Credentials](https://cloud.google.com/docs/authentication/application-default-credentials)),
  or set `GOOGLE_APPLICATION_CREDENTIALS`.
- **AWS** — `aws configure` or `aws sso login`
  ([AWS CLI configuration](https://docs.aws.amazon.com/cli/latest/userguide/cli-configure-files.html)),
  or `AWS_PROFILE` / `AWS_ACCESS_KEY_ID` in the env.
- **Azure** — `az login`
  ([sign in with the Azure CLI](https://learn.microsoft.com/cli/azure/authenticate-azure-cli)),
  or `AZURE_*` in the env.

Then a task just uses the provider SDK — **no Connection needed for local dev**:

```python
from airflow.sdk import DAG, task

with DAG("gcs_peek", schedule=None):
    @task
    def list_buckets() -> list[str]:
        from google.cloud import storage              # leoflow.yaml: dependencies: [google-cloud-storage]
        return [b.name for b in storage.Client().list_buckets()]   # uses your local ADC
    list_buckets()
```

`storage.Client()` resolves your `gcloud` Application Default Credentials through
Google's normal chain, exactly as it would in a script you run by hand — same idea
for `boto3` (AWS) and the Azure SDKs. Author a DAG that hits BigQuery, S3, or Blob
Storage and run it against your **real** accounts, locally, in the dev loop.

> In production/cluster mode the same DAG gets its credentials from managed
> **Connections** instead — pods don't inherit your laptop's env.

## Leoflow Lite vs the usual local-Airflow setups

| | `docker-compose` Airflow | `astro dev` | **Leoflow Lite** |
|---|---|---|---|
| Docker required | yes | yes | **no** (`--postgres managed`) |
| Boot time | minutes | ~a minute | **seconds** |
| Hot-reload | restart-ish | restart-ish | **on save** |
| Real provider operators | yes | yes | **yes** |
| Local cloud creds | manual mounts | manual | **inherited automatically** |
| Airflow UI | yes | yes | **yes (3.2)** |

## FAQ

**Can I use Leoflow to develop Apache Airflow DAGs locally?**
Yes. `leoflow lite` is a local development environment for Airflow: write a standard
`airflow.sdk` DAG, run it, and iterate with hot-reload — no Docker or Kubernetes.

**Does it run real Airflow operators and sensors?**
Yes. Native operators (`bash`, `python`) run directly; any other provider operator or
sensor runs the genuine Airflow class in the task. Reschedule-mode sensors are
supported.

**Which Airflow version is it compatible with?**
Apache Airflow 3.2 — including the Airflow 3.2 web UI, which Lite serves locally.

**Do I need Docker?**
No. `leoflow lite --postgres managed` uses an embedded Postgres and the subprocess
executor. (If Docker is present it can use it; if Docker is wedged, Lite falls back to
the managed Postgres automatically.)

**Can it use my existing GCP/AWS/Azure credentials?**
Yes — the local task inherits your environment and `$HOME`, so the provider SDKs find
your credentials through their normal default chains.

**Is it production-ready / what about deploying?**
Lite is for local development; the same DAGs deploy to Leoflow's Kubernetes mode (one
pod per task) for production. Same authoring, two runtimes.

## Try it

```bash
curl -fsSL https://raw.githubusercontent.com/neochaotic/leoflow/main/install.sh | sh
leoflow lite --postgres managed
```

Open source, Apache 2.0: **[github.com/neochaotic/leoflow](https://github.com/neochaotic/leoflow)**.
If you've been running Airflow under `docker-compose` just to develop a DAG, this is
the lighter loop.
