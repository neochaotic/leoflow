# Why Leoflow

Apache Airflow is the most deployed workflow orchestrator on earth — and the one
that bleeds most in production. Leoflow keeps everything engineers love (the UI,
the vocabulary, the pod-per-task model) and cuts out the part that hurts: the
**Python control plane**.

## The five wounds Airflow won't heal — and how Leoflow heals them

| Airflow wound | Leoflow cure |
|---|---|
| **The scheduler that stalls** — seconds between tasks; minutes-long pipelines that should take seconds. | A **Go scheduler** (goroutines, no GIL): a tight state-machine loop, no Python re-parse tax. |
| **The triggerer that suffocates** — past ~500 sensors the asyncio loop chokes and SLAs miss. | Go concurrency, not a single asyncio loop — backpressure-friendly by design. |
| **The DAG file that re-parses itself to death** — every loop opens every `.py` in `/dags`; CPU spikes for nothing. | **DAGs are immutable artifacts** (`dag.json` + image). Parsed once, at compile. The control plane never re-parses Python. |
| **The worker that leaks until it dies** — long-lived Celery workers accumulate FDs/connections, OOMKilled at 3 a.m. | **Pod-per-task**: each task is a fresh, ephemeral pod. Nothing to leak. |
| **The dependency hell with no door** — `pandas==1.0` vs `2.0`, one image, pick a side. | **One image per DAG**: each DAG carries its own dependencies. No shared `/dags`, no conflicts. |

## What you keep
- The **Airflow 3.2.x UI** (grid, graph, dashboard) — unmodified. ([UI compatibility](ui-compatibility.md))
- The **vocabulary**: DAGs, tasks, runs, XCom, trigger rules. ([Concepts](concepts.md))
- The **TaskFlow** authoring you already know. ([DAG authoring](dag-authoring.md))

## What you gain
- A **real dev loop** (`leoflow dev` — isolated cluster, hot reload). ([Operating modes](operating-modes.md))
- **GitOps**: every DAG is a versioned, immutable artifact built in CI. ([Deploy](deploy.md))
- A control plane you can actually **operate** — Go, observable, no GIL.

[Get started](quickstart.md){ .md-button .md-button--primary } [See the architecture](architecture.md){ .md-button }
