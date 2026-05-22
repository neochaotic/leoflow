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
- **HTTP API operator has two execution modes (hybrid).** By default, `type=http_api` tasks execute inline as goroutines in the Control Plane, paying no pod startup cost. This is the right choice for short-lived calls (webhooks, notifications, lightweight API triggers). For longer-running HTTP tasks (paginated fetches, batch endpoints, long-polling), the DAG author can opt into pod-based execution via the per-task `execution_mode` field. The Control Plane enforces a server-side maximum duration (`LEOFLOW_INLINE_HTTP_MAX_DURATION`, default 300s) on inline tasks; tasks that declare a longer `execution_timeout_seconds` and do not set `execution_mode: pod` fail validation at DAG push time with a clear error message pointing to the fix.
- **Tasks must declare resources.** CPU and memory requests are mandatory in the DAG spec for K8s placement to work.

## Revision History

**2026-05-22:** The original ADR exempted `http_api` from the pod model entirely, based on the assumption that all HTTP calls would be short (sub-second to a few seconds). Real-world cases require timeouts up to one hour (long-polling APIs, paginated data fetches, batch endpoints). A pure-goroutine implementation for such long tasks creates several serious problems: the Control Plane accumulates long-lived goroutines holding I/O state; restarts of the Control Plane (deploys, leader failover, crashes) abort all in-flight HTTP calls with no recovery; native resource limits (memory, CPU) cannot be applied to in-process goroutines; observability metrics like `leoflow_pods_running` become misleading.

The hybrid model resolves this without forcing every HTTP call to pay pod startup cost. Inline (goroutine) is the default and remains fast for the common short-call case. Pod mode is opt-in for the long-call case and inherits all the robustness, isolation, and observability of the standard pod-per-task model. A server-side cap on inline duration prevents misuse of the inline path for tasks that should be pod-based.

## Alternatives Rejected

- **Persistent worker pool:** rejected because it reintroduces every problem Leoflow is trying to solve.
- **Hybrid pool + ephemeral:** rejected as added complexity without clear benefit.
