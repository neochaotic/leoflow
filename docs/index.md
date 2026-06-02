---
hide:
  - navigation
  - toc
---

# Leoflow { .home-hero-title }

<div class="home-hero" markdown>
<div class="home-hero__text" markdown>

<p class="home-hero__lead">
The workflow orchestrator that ate Apache Airflow's lunch.<br>
<strong>Same UI. Same vocabulary. A Go control plane instead of Python's. Zero of the pain.</strong><br>
<em>Native map-reduce for ML/AI — fan-out + reduce as a Python list comprehension.</em>
</p>

[Get started](quickstart.md){ .md-button .md-button--primary }
[DAG authoring](dag-authoring.md){ .md-button }
[GitHub](https://github.com/neochaotic/leoflow){ .md-button }

</div>
<div class="home-hero__media" markdown>
<div class="home-hero__window" markdown>
<span class="home-hero__chrome"><i></i><i></i><i></i><em>Leoflow Lite — localhost:8088</em></span>
![Leoflow Lite, the ETL graph (extract, transform, load) running on a local cluster](assets/screenshots/dev-graph.png){ .home-hero__shot }
</div>
</div>
</div>

A **Go control plane** that keeps Airflow's proven **pod-per-task** model and its
**UI**, and throws away the Python control plane that makes Airflow slow.

## Author a DAG, ship a container

A DAG is a `leoflow.yaml` (config, bindings, packaging) plus a `dag.py` (Airflow
SDK). They compile to one immutable artifact — `dag.json` + a container image.

=== "leoflow.yaml"

    ```yaml
    schema_version: "1.0"
    dag_id: etl_daily
    description: Daily ETL — extract, transform, load.
    owner: data-eng
    tags: [etl]
    python_version: "3.12"
    dependencies:            # pip packages baked into the DAG's own image
      - pandas==2.2.2
    ```

=== "dag.py"

    ```python
    """etl_daily — a three-task ETL on the Airflow SDK."""
    from airflow.sdk import DAG, task

    @task
    def extract() -> list[int]:
        return list(range(100))

    @task
    def transform(rows: list[int]) -> int:
        return sum(rows)

    @task
    def load(total: int) -> None:
        print("loaded:", total)

    with DAG("etl_daily", schedule="0 6 * * *", catchup=False, tags=["etl"]):
        load(transform(extract()))
    ```

<div class="grid cards" markdown>

- :material-cube-outline: **DAGs are immutable artifacts**

    A `dag.json` + a container image, versioned together. No re-parsing `/dags`,
    no drift. [Concepts →](concepts.md)

- :material-package-variant-closed: **One image per DAG**

    Each DAG carries its own dependencies. No shared filesystem, no dependency
    hell. [Architecture →](architecture.md)

- :material-rocket-launch-outline: **A real dev loop**

    `leoflow lite` — isolated cluster, hot reload, silver Lite badge. Edit, save,
    see it run. [Operating modes →](operating-modes.md)

- :material-api: **Airflow-compatible API & UI**

    `/api/v2/*` and `/ui/*`, pinned to Airflow 3.2.x. [HTTP API →](api-reference.html)

- :material-graph-outline: **Native map-reduce for ML/AI**

    Fan-out + reduce as a Python list comprehension. No XCom plumbing, no
    broker, no special operator. [Map-reduce for ML →](cookbook/map-reduce.md)

</div>

## Map-reduce in two lines of Python

Every parallel ML workload — hyperparameter search, k-fold CV, ensemble
training, batch inference, Monte Carlo — is the same pattern. Leoflow expresses
it as a list comprehension:

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

The parser captures the list shape at compile time; the runtime assembles the
upstream XComs in declaration order and delivers them as a real Python list —
with per-trial isolation, per-trial retry, and a `null` slot for any upstream
that legitimately produced no result. See **[Map-reduce for ML →](cookbook/map-reduce.md)**
for the guarantees, limits, and the `dag.json` shape.

## The dev loop

```bash
leoflow lite provision            # check + provision host deps (dev-only)
leoflow init dags/my_dag     # scaffold a project
leoflow lite dags/my_dag      # hot-reload at http://localhost:8088 (Lite edition)
```

**Lite** ships today (pre-alpha, local on a trusted network); **Pro**
(the Kubernetes edition) is a near-term goal — see the
[roadmap](roadmap-to-release.md) and [Editions](editions.md) for the split.
