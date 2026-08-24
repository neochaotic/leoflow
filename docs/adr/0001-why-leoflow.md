# ADR 0001: Why Leoflow and Not Apache Airflow's KubernetesExecutor

**Status:** Accepted
**Date:** 2026-05-21
**Deciders:** Project founder

## Context

Apache Airflow already supports running each task as an ephemeral Kubernetes pod via the `KubernetesExecutor`. This raises an obvious question: why build Leoflow at all? Why not just use what already exists?

This ADR records the reasoning that justifies the existence of this project, so that future contributors do not waste energy reinventing what Airflow already does well.

## Decision

Leoflow inherits the **execution model** of Airflow's `KubernetesExecutor` (one ephemeral pod per task instance). It does not reinvent that. What it replaces is the **Python control plane** and the **monolithic worker image**.

## Rationale

Five concrete pains motivate a new implementation:

### 1. Cold start of the worker pod

The official Airflow image is roughly 1.5 GB. Every task creates a pod that must pull this image, boot Python, import the full Airflow framework with every installed provider, connect to Postgres, parse the DAG, and only then run the user code. Typical cold start: 15 to 45 seconds.

Leoflow's worker is the user's own image (typically 200 MB) plus a static Go binary `leoflow-agent` of about 15 MB. No framework to import; the agent is a thin gRPC runner. Target cold start: 2 to 5 seconds.

### 2. Scheduler latency

Airflow's scheduler is Python with the GIL. With one thousand DAGs and frequent re-parsing of `.py` files plus heavy SQLAlchemy traffic, the latency between one task ending and the next starting is typically 3 to 10 seconds.

Leoflow's scheduler is native Go, processes serialized DAGs in memory, and uses goroutines for concurrency. Target latency: under 200 milliseconds.

### 3. Triggerer scalability

Airflow's `Triggerer` uses Python `asyncio` and starts choking above roughly five hundred concurrent triggers. Each trigger holds memory in the same Python process.

Leoflow's sensors are goroutines, roughly 2 KB each. A single instance can carry one hundred thousand sensors comfortably.

### 4. Dependency hell

`KubernetesExecutor` runs all pods from the same Airflow image. If DAG A needs `pandas==1.0` and DAG B needs `pandas==2.0`, all options are bad: install everything globally (conflicts), use `KubernetesPodOperator` (abandons the executor model), or maintain multiple Airflow base images (operational nightmare).

Leoflow's DAG-as-Image model makes per-DAG isolation the default, not a workaround.

### 5. Architectural coherence

Airflow was designed in 2014 around Celery and persistent workers. `KubernetesExecutor` was added in 2018 as an adapter on top of that. Concepts like queues, pools, and slots leak into the K8s mode where they no longer make sense.

Leoflow is K8s-first by design. The control plane uses K8s informers and watches natively. There are no queues, no pools, no slots — the K8s scheduler does the scheduling.

## Honest Comparison

| Aspect | Airflow KubernetesExecutor | Leoflow |
|---|---|---|
| Pod-per-task model | Yes | Yes |
| Worker image size | 1.5 GB+ | 200 MB typical |
| Pod cold start | 15-45 s | 2-5 s target |
| Scheduling latency | 3-10 s | <200 ms target |
| Runtime DAG parsing | Yes, inside pod | No, JSON pre-compiled |
| Dependency isolation | Workaround | Native |
| Sensor scalability | ~500 (Triggerer) | 100,000+ (goroutines) |
| Control plane language | Python (GIL) | Go (native concurrency) |
| Mental model | Celery-era with K8s adapter | K8s-native |

## Consequences

- The project must remain honest about what it inherits from Airflow. The README and marketing materials must credit the `KubernetesExecutor` model.
- The team must study the Airflow source for known bugs (pod cleanup, OOMKilled detection, `ImagePullBackOff` handling) and not redo six years of bugfix archaeology.
- The execution model is **not a differentiator** by itself. The differentiation is in the performance, the developer experience (`leoflow.yaml`), and the operational simplicity.
