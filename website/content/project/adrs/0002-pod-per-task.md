---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /adr/0002-pod-per-task.html
# --- end AUTO redirect aliases ---
title: "ADR 0002: Pod-per-Task Execution Model"
linkTitle: 0002 · Pod-per-Task Execution Model
weight: 20
description: "ADR 0002: Pod-per-Task Execution Model"
---

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
- **No `pool` or `queue` abstraction in the API.** These concepts from Celery-era orchestrators do not map to K8s and would mislead users. The Airflow-compatible API translates accordingly. **[SUPERSEDED 2026-08 — see Amendment below. This clause is withdrawn: under N:1 a `pool` is a placement + concurrency abstraction, not a worker queue, and `max_active_tasks` is a `PlanRun` admission gate. The abstraction is now first-class; see ADR 0053.]**
- **HTTP API operator has two execution modes (hybrid).** By default, `type=http_api` tasks execute inline as goroutines in the Control Plane, paying no pod startup cost. This is the right choice for short-lived calls (webhooks, notifications, lightweight API triggers). For longer-running HTTP tasks (paginated fetches, batch endpoints, long-polling), the DAG author can opt into pod-based execution via the per-task `execution_mode` field. The Control Plane enforces a server-side maximum duration (`LEOFLOW_INLINE_HTTP_MAX_DURATION`, default 300s) on inline tasks; tasks that declare a longer `execution_timeout_seconds` and do not set `execution_mode: pod` fail validation at DAG push time with a clear error message pointing to the fix.
- **Tasks must declare resources.** CPU and memory requests are mandatory in the DAG spec for K8s placement to work.

## Revision History

**2026-05-22:** The original ADR exempted `http_api` from the pod model entirely, based on the assumption that all HTTP calls would be short (sub-second to a few seconds). Real-world cases require timeouts up to one hour (long-polling APIs, paginated data fetches, batch endpoints). A pure-goroutine implementation for such long tasks creates several serious problems: the Control Plane accumulates long-lived goroutines holding I/O state; restarts of the Control Plane (deploys, leader failover, crashes) abort all in-flight HTTP calls with no recovery; native resource limits (memory, CPU) cannot be applied to in-process goroutines; observability metrics like `leoflow_pods_running` become misleading.

The hybrid model resolves this without forcing every HTTP call to pay pod startup cost. Inline (goroutine) is the default and remains fast for the common short-call case. Pod mode is opt-in for the long-call case and inherits all the robustness, isolation, and observability of the standard pod-per-task model. A server-side cap on inline duration prevents misuse of the inline path for tasks that should be pod-based.

**2026-08-27:** The "cold start matters more" consequence is addressed by three deliberate levers, each scoped to a real situation, so pod-per-task is not over-engineering for the cases where the per-pod overhead would otherwise dominate: (1) the **subprocess executor** for the local dev loop (`leoflow lite --executor subprocess`) runs tasks as host processes with no pod and no image build; (2) **`dbt_group()`** (ADR 0042/0043) runs a whole dbt project as one grouped task whose models dbt orchestrates internally, so N models are not N pods; (3) **warm worker pools** (ADR 0058, operator-set) reuse one pod across attempts of a DAG version. A *generic* "fuse arbitrary operators into one pod" capability was evaluated and **deliberately deferred** — see ADR 0043's decision log for the rationale. The pod-per-task default therefore stands unchanged; these levers relieve the cold-start cost without eroding the per-task isolation/retry/secret-scope the model exists to provide. The author-facing explanation lives in [Core concepts → When you pay for a pod](/concepts/core-concepts/#when-you-pay-for-a-pod-and-when-you-dont).

## Alternatives Rejected

- **Persistent worker pool:** rejected because it reintroduces every problem Leoflow is trying to solve.
- **Hybrid pool + ephemeral:** rejected as added complexity without clear benefit.

## Amendment (2026-08) — pool/queue abstraction superseded

**Amends:** exactly one consequence of this ADR. Everything else stands.
**Relates:** ADR 0051 (separate the orchestration and execution state machines), ADR 0052 (durable task outcome), ADR 0053 (admission + placement — the layer this amendment clears the ground for).

ADR 0002 stands. Ephemeral-container-per-task remains the default and only *baseline*
execution model; native isolation, per-DAG dependencies (ADR 0003), free K8s
scheduling, and zero idle cost are all preserved verbatim. This is a **surgical
supersede of one consequence**, not a teardown.

### The superseded clause

The Consequences section stated:

> **No `pool` or `queue` abstraction in the API.** These concepts from Celery-era
> orchestrators do not map to K8s and would mislead users. The Airflow-compatible
> API translates accordingly.

**This clause is withdrawn.** A per-DAG `max_active_tasks` gate and real named
`pool`s are hereby admitted into the model.

### Why the clause is now wrong

The clause was sound under its own premise: when a "pool" means *a fixed set of
long-lived Celery workers draining a queue*, it genuinely does not map to
pod-per-task, and exposing it would have misled users into expecting idle
workers. That premise no longer holds.

Under the N:1 execution direction — a task lives in exactly ONE pod; a DAG spans
1..N pods; task→pod is a function, N:1 — a pod is no longer identically "one
task's ephemeral container." It is a **placement target** that is either
*dedicated* (today's ADR 0002 model, unique to one task) or *shared* (a warm
worker hosting sibling tasks of the same DAG). Once a pod can be a placement
target rather than a synonym for a task, the two Celery concepts split cleanly
and each maps:

- A **pool becomes a placement + concurrency abstraction, not a worker queue.** In
  the warm-worker case the pool literally *is* the set of warm pods for a DAG —
  membership is a placement decision, resolved by an O(1) per-task free-slot
  lookup, not a queue a worker drains. The abstraction the original clause
  rejected as unmappable is exactly the one the new topology needs a name for.
- **`max_active_tasks` becomes a scheduler admission gate, not a queue depth.** It
  is enforced in `PlanRun` *before* a task becomes `queued` — an admission
  decision on the orchestration side (ADR 0051's orchestration state machine),
  with no worker and no queue involved. It gates how many of a DAG's tasks may be
  in flight; it says nothing Celery-style about *where* they run.

So the original "does not map to K8s" premise is **invalidated by the new
execution model**, not merely re-weighed. ADR 0051 is what makes this safe to
say: the admission gate lives entirely in the orchestration state machine, and
the placement/pool lives entirely in the execution state machine, across the seam
ADR 0051 draws. The pool never leaks back into being a Celery worker because
orchestration consumes a task's outcome through the execution seam
(`ExecutionStore` / the durable record of ADR 0052), never through pod phase or
pod identity.

### Zero idle cost is preserved

The one property that made the original clause attractive is kept. Warm pools are
per-DAG with **`min-idle=0` (scale-to-zero)**: when a DAG has no ready work its
pool holds no pods, so admitting pools reintroduces **no idle cost**. ADR 0002's
zero-idle guarantee is a floor the pool abstraction sits on top of, not a promise
it breaks — a pool with zero members is indistinguishable from today's
ephemeral-only model.

### The revised consequence

Replace the withdrawn clause with:

> **`pool` and per-DAG `max_active_tasks` are first-class concurrency
> abstractions.** They are the **admission layer** specified in ADR 0053
> (admission + placement). `max_active_tasks` is a per-DAG scheduler admission
> gate applied in `PlanRun`; a named `pool` is a cross-DAG admission gate over the
> same layer. Neither is a Celery worker queue: admission is an orchestration-side
> decision (ADR 0051), and placement onto a *warm pool* — the only thing that
> resembles a "worker" — is an execution-side decision that scales to zero
> (`min-idle=0`), preserving ADR 0002's zero-idle-cost property. The
> Airflow-compatible API may now surface both rather than translating them away;
> `/api/v2/pools` graduates from stub to real.

### Lite-containment constraint

The admission gates live in `PlanRun`, which is **shared scheduler core** between
Lite (single binary, subprocess executor, in-process delivery) and Pro. Per the
Lite-containment rule, any change on this path must leave Lite's observable
behavior byte-for-byte unchanged. Therefore the `max_active_tasks` and `pool`
gates **default to today's Lite behavior behind an edition/config flag** — no gate
applied unless explicitly configured — so an unconfigured Lite install plans and
admits exactly as it does today. The warm-worker placement target this amendment
names is Pro/Kubernetes-only and out of scope for Lite; ADR 0053 carries the flag
and the containment proof.
