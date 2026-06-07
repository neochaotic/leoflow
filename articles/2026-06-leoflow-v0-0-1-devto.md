---
title: "We rewrote Apache Airflow's control plane in Go (and kept the UI)"
published: false
description: "Leoflow v0.0.1 just shipped. Pod-per-task, but actually fast. Native map-reduce as a Python list comprehension. Same Airflow vocabulary, none of the Python overhead."
tags: airflow, go, kubernetes, dataengineering
cover_image: https://raw.githubusercontent.com/neochaotic/leoflow/main/docs/assets/screenshots/etl-graph.png
series: "Building Leoflow"
devto_sync: false # already published manually on dev.to — excluded from auto-sync to avoid a duplicate
---

> **TL;DR** — Leoflow `v0.0.1` just shipped. It speaks the Airflow API, runs the Airflow 3.2.x UI **unmodified**, but replaces the Python control plane with Go. Pod-per-task is the only execution mode. Each DAG is its own container image. Fan-in (map-reduce) is a Python list comprehension. Install: `curl -fsSL https://raw.githubusercontent.com/neochaotic/leoflow/main/install.sh | sh`. GitHub: **[neochaotic/leoflow](https://github.com/neochaotic/leoflow)** — stars and issues warmly accepted.

---

## The 3 AM pager

You know the one. The scheduler stalled again. Or the triggerer suffocated under 500 sensors. Or a worker leaked file descriptors until Kubernetes OOMKilled it mid-run. Or someone bumped `pandas` for the new DAG and broke six legacy ones because they all share the same image.

Apache Airflow is the most widely deployed workflow orchestrator on earth. It is also the one that bleeds the most in production. None of those wounds are bugs — they are **structural consequences of running orchestration through a Python control plane**. You cannot patch the GIL. You cannot make `DagBag` reparse cheap. You cannot make Celery workers ephemeral without rewriting them.

So we did the only thing left: we kept everything Airflow got right and replaced everything that bleeds.

---

## What "kept" means

We did not invent a new model. Airflow's `KubernetesExecutor` proved years ago that **pod-per-task is correct**: each task gets its own container, its own resources, its own lifecycle. You can't leak a process that exits.

We also did not invent a new UI. The Airflow 3.2.x React SPA ships embedded inside the Leoflow server binary. Your team's muscle memory survives the migration.

What we kept:

- The pod-per-task execution model
- The Airflow 3.2.x UI (literally the same React build, served from `/`)
- The HTTP API shape (`/api/v2/dags/...`, `/api/v2/dagRuns/...`, etc.)
- The vocabulary: DAG, TaskInstance, DagRun, XCom, Trigger Rules
- The DAG-authoring dialect — `from airflow.sdk import DAG, task`, TaskFlow, classic operators

What we threw out:

- The Python scheduler. It's Go now.
- The Python triggerer. Sensors are 2 KB goroutines.
- The shared `/dags` folder. Each DAG is its own immutable container image.
- The "long-lived Celery worker" model. Every task is an ephemeral pod.

---

## What it looks like to write a DAG

Two files. That's the whole project.

```yaml
# leoflow.yaml — your deploy concerns
dag_id: etl_sales
python_version: "3.11"
dependencies:
  - pandas==2.1.0
  - requests==2.31.0
```

```python
# dag.py — your DAG, in real Airflow SDK 3.2.x
from airflow.sdk import DAG, task

@task
def fetch() -> list[dict]:
    import requests
    return requests.get("https://api.example.com/orders").json()

@task
def transform(orders: list[dict]) -> list[dict]:
    return [{"id": o["id"], "value": o["amount"] * 1.1} for o in orders]

with DAG("etl_sales", schedule="0 5 * * *", catchup=False):
    transform(fetch())
```

```bash
leoflow compile .              # generates Dockerfile, builds image, emits dag.json
leoflow push ./dag.json        # registers a new versioned DAG
```

No `Dockerfile`. No `requirements.txt`. No `Helm values.yaml` for this DAG. No `pyproject.toml`. The compiler reads `leoflow.yaml`, generates a Dockerfile against the official base image (`leoflow/python-runtime:3.11`), builds, pushes to your registry, and registers a versioned `dag.json` with the control plane. That's the whole inner loop.

For local development, `leoflow lite` provisions a managed Postgres, hot-reloads on every save, gives each DAG its own per-DAG virtualenv at `~/.leoflow/dev/venvs/<dag_id>/`, and **auto-detects [`uv`](https://github.com/astral-sh/uv) on `PATH`** for 5–10× faster cold installs. Two DAGs that pin conflicting versions of the same package coexist without interference. This is the bit that made me file the issue against Airflow for the first time, ten years ago. We finally have it.

---

## Map-reduce, as a Python list comprehension

Hyperparameter search. K-fold cross-validation. Ensemble training. Monte Carlo. Every parallel ML workload is **map-reduce**. Most orchestrators make you build it: an operator per fan-out, a broker for the intermediate values, shared storage for the artifacts, a custom reducer that knows how to find them all.

Leoflow expresses the whole pattern in **two lines of Python**:

```python
from airflow.sdk import DAG, task

@task
def trial(lr: float) -> dict:
    return train_one(lr)                            # map

@task
def select_best(trials: list[dict]) -> dict:
    return max(trials, key=lambda r: r["score"])    # reduce

with DAG("hparam_search", schedule=None):
    select_best([trial(lr) for lr in [0.001, 0.01, 0.05, 0.1, 0.5]])
```

That `[trial(lr) for lr in …]` is the whole map. `trials: list[dict]` is the whole reduce. **No XCom plumbing, no broker, no shared filesystem, no special operator.** The parser captures the list shape at compile time; the runtime assembles upstream XComs in declaration order and delivers them as a real Python list. Per-trial isolation (own pod, own process, own venv if you want). Per-trial retry. Deterministic ordering. A 256 KB cap per upstream value. A `null` slot for any upstream that legitimately produced no result.

If you have ever written a Celery chord by hand, take a moment.

---

## Architecture

```
┌───────────────────────────────────────────────────────────────┐
│                Author / CI                                    │
│  leoflow.yaml  +  dag.py  +  (auto-generated) Dockerfile      │
└───────────────────────────────┬───────────────────────────────┘
                                │  leoflow compile / push
                                ▼
┌───────────────────────────────────────────────────────────────┐
│                Control plane — Go                             │
│ ┌───────────────────────────────────────────────────────────┐ │
│ │ HTTP API  /api/v2  ·  JWT · RBAC · multi-tenant           │ │
│ ├───────────────────────────────────────────────────────────┤ │
│ │ Scheduler   ·  state machine · cron · catchup             │ │
│ │             ·  PG-advisory-lock leader election           │ │
│ │             ·  retries with backoff                       │ │
│ ├───────────────────────────────────────────────────────────┤ │
│ │ Agent gRPC service  ·  task spec · state · XCom · logs    │ │
│ └───────────────────────────────────────────────────────────┘ │
│       │                                  │                    │
│       │ Postgres (metadata)              │ Redis (XCom + log) │
└───────┼──────────────────────────────────┼────────────────────┘
        │                                  │
        │     dispatch: one pod per task   │
        ▼                                  │
┌───────────────────────────────────────┐  │
│           Kubernetes                  │  │
│  ┌─────────────────────────────────┐  │  │
│  │  Worker pod = your DAG image    │  │  │
│  │  leoflow-agent (15 MB Go bin)   │  │  │
│  │     ⇅ gRPC                      │  │  │
│  │  your Python / Bash code        │──┼──┘
│  └─────────────────────────────────┘  │
└───────────────────────────────────────┘
```

Short-lived `http_api` tasks skip the pod and run inline as goroutines (capped). Everything else runs **pod-per-task**, every time. Concurrency is goroutines and pods — no Celery, no triggerer process, no shared worker pool.

A few specifics worth calling out:

- **Leader election** is a Postgres advisory lock. No external coordinator. No ZooKeeper, no etcd, no Raft library. It is the kind of decision you can explain to a new hire in 30 seconds.
- **XCom** lives in **Postgres on Lite** (small, no Redis required for laptop dev) and **Redis on Pro**. 256 KB cap, optional schema validation, last-write-wins.
- **Connections** are encrypted at rest with AES-256-GCM and delivered to tasks via Airflow's standard `AIRFLOW_CONN_<ID>` env var. Postgres / MySQL / SQLite / MSSQL / Redis / HTTP / GCS connectors ship with chain-of-custody-tested integration tests.
- **The agent** is a static Go binary, ~15 MB. PID 1 of the task pod. Talks gRPC back to the control plane. Forks one process per task. Does not buffer Python output (`-u` plus `PYTHONUNBUFFERED=1`), because watching a SIGKILL race steal half of the user's `print()` output is its own kind of torment.

---

## The numbers (the only honest part of any orchestration README)

We are not going to claim "1000× faster" because nobody who has run real pipelines believes you. Here is what falls out of replacing the control plane:

| | Airflow today | Leoflow |
|---|---|---|
| Scheduler decision latency | 3–10 s per task | **<200 ms** — native Go, no GIL |
| Sensor concurrency | ~500 (asyncio Triggerer) | **100,000+** — each sensor is a 2 KB goroutine |
| DAG parsing cost | Re-parsed every scheduler loop | **Zero** — `dag.json` is precompiled, immutable |
| Worker lifecycle | Long-lived, leak-prone | **Ephemeral pod per task** — spawn, run, die |
| Worker image size | 1.5 GB+ Airflow base | **~200 MB typical** — each DAG is its own slim image |
| Dependency isolation | Workaround via `KubernetesPodOperator` | **Native** — every DAG is a container |
| Cold start | 15–45 s | **2–5 s target** — agent is a 15 MB static binary |
| Observability | Retrofitted with effort | **Native** — Prometheus + OpenTelemetry + structured logs from commit one |

These are the structural wins. The marketing-grade "X× faster" depends on your DAG. The scheduler latency drop is universal.

---

## What it is not (because we have all read those launch posts)

- **It is not v1.0.** Per [ADR 0037](https://github.com/neochaotic/leoflow/blob/main/docs/adr/0037-release-version-scheme.md), `v0.0.1` ends the pre-alpha series; every release after is `vX.Y.Z-rc.N → vX.Y.Z`. The HTTP API, CLI surface, and Helm values may change between minor versions until `v1.0.0` locks them.
- **The UI is still Airflow 3.2.x.** It is a tactical choice (your team's muscle memory). A purpose-built Leoflow UI is on the roadmap ([ADR 0018](https://github.com/neochaotic/leoflow/blob/main/docs/adr/0018-airflow-ui-as-mvp.md)).
- **Pro is Kubernetes-only.** Lite runs anywhere. Pro means a real cluster, external Postgres + Redis, the Helm chart. There is deliberately no Docker-Compose "Pro" path; we explained why in [ADR 0015](https://github.com/neochaotic/leoflow/blob/main/docs/adr/0015-kubernetes-only-execution.md).
- **It is not a drop-in for every Airflow plugin.** The Airflow operator catalog has 30+ years of accreted Python; we ship a closed set (`python`, `bash`, `http_api`) plus first-party connectors. ADR 0036 defines a runtime shim for `from airflow.providers.<X>.hooks.<Y> import <Z>Hook` so the common cases keep working — but if your DAG depends on three obscure providers we have not vendored, you will hit a wall today. File an issue; we are gating the next batch by demand.

---

## Lite: the zero-deploy path

Here is the part that surprises people. To run Leoflow on your laptop you do **not** need a Kubernetes cluster. You do not need Docker. You do not need a container registry, a `compile`, a `push`, or a single line of deploy YAML. You need one shell command:

```bash
curl -fsSL https://raw.githubusercontent.com/neochaotic/leoflow/main/install.sh | sh
leoflow lite                # → http://localhost:8088  (LITE badge, top-center)
```

The installer is a single shell script: three static Go binaries into `~/.leoflow/bin`, then `leoflow setup` provisions a **managed CPython**, the parser, and a **managed local Postgres** — nothing touches your system Python or your global packages. Then `leoflow lite` boots a full control plane (scheduler, API, UI) against that managed Postgres. No system services, no Compose file, no cluster. Close the terminal and it's gone.

### There is no "dags/" folder — there is *your* folder

This trips up everyone coming from Airflow, so let's be explicit. Leoflow has **no magic `dags/` directory**. During `leoflow setup` you pick a **workspace folder** (default `~/leoflow`) — that folder *is* the runtime. Every subdirectory that contains a `leoflow.yaml` is a DAG project; the watcher scans them and hot-reloads on save. Your tree looks like what you'd actually keep in git:

```
~/leoflow/                     ← the workspace you chose at install
├── etl_sales/
│   ├── leoflow.yaml           ← makes this folder a DAG
│   └── dag.py
└── hparam_search/
    ├── leoflow.yaml
    └── dag.py
```

No central registry file, no `dag_folder` setting to fight, no "why isn't my DAG showing up." A folder with a `leoflow.yaml` is a DAG. That's the whole rule.

### Edit DAGs from the browser — and get examples in one click

Lite ships a small **embedded web editor** so you can go from install to a running DAG without leaving the browser. Click the `< >` **IDE** button (bottom-right of the UI) and you get a real [Monaco](https://microsoft.github.io/monaco-editor/) editor — the engine behind VS Code — scoped to your workspace:

![The Leoflow Lite web editor: a file tree on the left with leoflow.yaml and a dag.py open, Python syntax highlighting, and Download examples / New file / Save buttons](https://raw.githubusercontent.com/neochaotic/leoflow/main/docs/assets/screenshots/lite-web-editor.png)

- **Python + YAML syntax highlighting**, a workspace file tree, open/save (**⌘S**), create/rename/delete with a "create target" chip that always tells you where a new file will land, collapse/expand carets that remember their state, and a recursive folder delete that says so out loud before it nukes a tree.
- A **"Download examples"** button in the header. Click it and Leoflow materializes the bundled example DAGs straight into your workspace — fan-out/aggregate, Monte Carlo π, an HTTP-load DAG, a daily-sales ETL — so you have real, runnable DAGs in the UI in seconds instead of staring at an empty home screen.
- Every save hits disk, the watcher picks it up, and the DAG **hot-reloads**. (One gotcha: the Airflow tab doesn't auto-refresh DAG *structure* — reload it.)

It is deliberately *not* a full IDE — no extensions, no terminal, no debugger. For those, point your own editor at the same workspace folder; it's just files on disk. The editor is a Lite convenience and is never registered in Pro.

Recover the admin password any time with `leoflow lite reset-password`. The [Lite web-editor guide](https://github.com/neochaotic/leoflow/blob/main/docs/lite-web-editor.md) and the [Lite cookbook](https://github.com/neochaotic/leoflow/blob/main/docs/dev-workflow.md) cover the rest.

---

## Pro: when you outgrow the laptop

For production, the Helm chart deploys against external Postgres 13+ and Redis 6+ (Cloud SQL / RDS / Memorystore / ElastiCache / Azure Cache all work):

```bash
kubectl create namespace leoflow
helm install lf oci://ghcr.io/neochaotic/leoflow -n leoflow \
  --version v0.0.1 \
  --set database.url='postgres://USER:PASS@HOST:5432/leoflow?sslmode=verify-full' \
  --set redis.url='rediss://HOST:6380/0' \
  --set auth.jwtSecret="$(openssl rand -base64 64)" \
  --set secretKey="$(openssl rand -hex 16)"
```

Read the [chart docs](https://github.com/neochaotic/leoflow/blob/main/docs/helm-chart.md) for every value.

---

## Engineering discipline (the stuff you would actually want to know)

If you are evaluating Leoflow as a load-bearing piece of your data platform, the answers are in the repo, but the short version:

- **Strict TDD.** Every line of production code is preceded by a failing test ([ADR 0011](https://github.com/neochaotic/leoflow/blob/main/docs/adr/0011-tdd-strict.md)). Per-package coverage floors enforced in CI.
- **Go Report Card A+** as the quality floor ([ADR 0012](https://github.com/neochaotic/leoflow/blob/main/docs/adr/0012-code-quality-standards.md)). `gocyclo ≤ 15` per function. GoDocs on every exported identifier.
- **Supply chain from commit one.** `govulncheck`, `gosec`, `Trivy`, `CodeQL`, `gitleaks`, OpenSSF Scorecard, OpenSSF Best Practices badge. Releases signed with `cosign`. SBOMs published per platform.
- **Every architectural decision is an ADR.** 37 of them at the time of writing. They are the single best place to start if you want to understand the *why*.

The repo also runs an end-to-end install smoke against **seven Linux distros** (`ubuntu:24.04`, `debian:12`, `fedora:41`, `alpine:3.20`, `opensuse/leap:15`, `archlinux:latest`, `rockylinux:9`) on every release, plus a `prealpha.N → v0.0.1` upgrade smoke. The `v0.0.1` release got the green light because every one of those passed.

---

## Help us ship the next milestone

We are at `v0.0.1`. The next steps are visible — pick any:

1. **Star the repo** at [github.com/neochaotic/leoflow](https://github.com/neochaotic/leoflow). It is the cheapest way to tell us this matters to you.
2. **Open an issue** with a chronic Airflow pain we have not closed yet. Pre-1.0 is *the* time to shape the API.
3. **File a PR.** Strict TDD applies. The [CONTRIBUTING guide](https://github.com/neochaotic/leoflow/blob/main/CONTRIBUTING.md) is the entry point. We review fast.
4. **Try the Lite quick start** (60 seconds, above). If something does not work, that is an issue worth filing.
5. **Run the Helm chart** against a real cluster and tell us what bit you. Pro alpha needs real-world miles before it gets a Pro `v1.0.0`.

---

## What is on the post-`v0.0.1` table

Visible work toward `v0.1.0`:

- Optimized backfill (parallel execution with throttling)
- UI scaling for 10,000+ DAGs (caching, server-side pagination)
- Dynamic task mapping
- OIDC authentication (Google, Azure AD, Keycloak, Okta)
- Deferrable tasks ([ADR 0016](https://github.com/neochaotic/leoflow/blob/main/docs/adr/0016-deferrable-tasks.md)) — efficient dispatch + long-poll pattern, native Go, **no separate Triggerer process**
- A purpose-built Leoflow UI ([ADR 0018](https://github.com/neochaotic/leoflow/blob/main/docs/adr/0018-airflow-ui-as-mvp.md))

We are deliberately keeping the surface small until it is *boring and reliable*. Workflow orchestrators have to be boring to be useful.

---

## License & credits

Apache 2.0. We stand on the shoulders of Airflow — the team behind it defined the vocabulary, proved the architecture, and built the UI we reuse without modification today. We also studied Argo Workflows, Prefect, and Dagster carefully; each made decisions worth borrowing, and we did.

If you have ever waited five seconds for an Airflow task to start, you know why we built this.

**→ [github.com/neochaotic/leoflow](https://github.com/neochaotic/leoflow)** — the v0.0.1 release notes are at [/releases/tag/v0.0.1](https://github.com/neochaotic/leoflow/releases/tag/v0.0.1).

Thanks for reading. Tell us what hurts.
