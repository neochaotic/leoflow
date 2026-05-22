# ADR 0002: Pod-per-Task Execution Model

**Status:** Accepted
**Date:** 2026-05-21

## Context

Workflow orchestrators have historically used three execution models:

1. **Persistent worker pool (Celery).** Long-running workers consume tasks from a queue. Mature, but workers idle when there is no work, leak memory over time, and force dependency conflicts because all tasks share one Python process.
2. **Per-task process on a fixed host.** A scheduler forks subprocesses on a known machine. Simple, but does not scale horizontally and has zero isolation.
3. **Ephemeral container per task.** Each task starts a fresh container, runs, exits. Maximum isolation, zero idle cost, natural fit for Kubernetes.

## Decision

Leoflow uses **ephemeral container per task** as its only execution model. There is no persistent worker pool in any deployment mode.

In Kubernetes mode, each task is a `Pod` created via `client-go`. The Kubernetes scheduler decides placement based on `nodeSelector`, `tolerations`, and resource requests declared in the task spec.

In standalone mode, each task is either a Docker container (default) or a subprocess (dev mode only, with a runtime warning). A semaphore limits concurrency to a configurable maximum.

## Rationale

- **Zero idle cost.** No workers sitting around waiting for work. Especially important for sparse workloads.
- **Native isolation.** Memory leaks, file descriptor leaks, and stale state are impossible across tasks because the container is fresh every time.
- **Per-DAG dependencies.** Combined with ADR 0003 (DAG-as-image), this gives every DAG its own Python environment with no workaround.
- **Free K8s scheduling.** Bin packing, autoscaling, spot instances, affinity rules — all inherited from the Kubernetes scheduler.

## Consequences

- **Cold start matters more.** Because every task pays the container startup cost, the base images must stay small and the agent must be a tiny static binary. See ADR 0004.
- **No `pool` or `queue` abstraction in the API.** These concepts from Celery-era orchestrators do not map to K8s and would mislead users. The Airflow-compatible API translates accordingly.
- **HTTP API operator does not create a pod.** For tasks that are pure outbound HTTP calls, the control plane fires them directly as goroutines. No pod cost.
- **Tasks must declare resources.** CPU and memory requests are mandatory in the DAG spec for K8s placement to work.

## Alternatives Rejected

- **Persistent worker pool:** rejected because it reintroduces every problem Leoflow is trying to solve.
- **Hybrid pool + ephemeral:** rejected as added complexity without clear benefit.
