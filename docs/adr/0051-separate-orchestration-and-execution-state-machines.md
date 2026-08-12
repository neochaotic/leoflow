# ADR 0051: Separate the orchestration and execution state machines

**Status:** Proposed
**Date:** 2026-08-12
**Relates:** ADR 0031 (scheduler architecture — reconciliation loop, two-phase dispatch, two-layer reaping), ADR 0027 (editions: executors + delivery), ADR 0015 (Kubernetes-only container execution), ADR 0002 (pod-per-task), ADR 0004 (thin agent), ADR 0010 (observability), ADR 0049 (split API/scheduler roles)
**Issues:** #543 (agent exit code conflates task outcome with report delivery), infra-vs-task retry conflation (this ADR)

## Context

Leoflow runs **one** state machine — the DAG/task reconciliation loop in
`internal/scheduler` (ADR 0031) — but that machine has quietly absorbed a
**second, distinct** one: the pod/execution lifecycle. The execution state
machine has no first-class representation; it is reconstructed asynchronously
from agent gRPC `ReportState` calls, or inferred from pod phase by the reapers.
The two machines answer different questions:

- **Orchestration:** given the DAG's edges, trigger rules, and a task's retry
  policy, *what should happen next?* It knows tasks and dependencies.
- **Execution:** given one unit of work, *place it on a substrate, watch it, and
  return exactly one outcome.* It knows pods, subprocesses, and node loss.

Today the second machine leaks through the first. Four pieces of evidence:

1. **The `Executor` interface returns only a dispatch error, not a task
   outcome.** `Executor.Execute` (`internal/executor/executor.go:100`) returns a
   single `error` that "reflects dispatch, and the agent reports the final state
   over gRPC" (its own doc comment, `executor.go:97-99`). There is no
   `ExecutionOutcome` type. The real task result is reconstructed later from the
   agent's `ReportState`, or — when the agent never reports — *inferred* from pod
   phase by a reaper. The seam that should carry an outcome carries only "did the
   dispatch call error."

2. **Three of the four reapers are pure pod/execution concerns living inside the
   scheduler.** `internal/scheduler` holds four reapers; only `reap.go`
   (run-finalization, #120) is orchestration. The other three are about the
   substrate: `heartbeat_reap.go` (agent-lost, #128),
   `pod_lost_reap.go` (pod-lost — and explicitly **Kubernetes-only, a no-op with
   no PodManager**, i.e. on Lite/subprocess: `pod_lost_reap.go:63-64, 92-93`),
   and `stale_queued_reap.go` (dispatch-lost, detected via pod liveness). Three
   execution-layer sweeps are wired into the orchestration loop because there is
   nowhere else for them to live.

3. **Infrastructure faults consume the task's retry budget — the money finding.**
   All three execution-fault reapers write a plain `failed`, which lands on the same
   `retriable()` rail as an application exception
   (`internal/scheduler/plan.go:104-107` — `run.Tries[taskID] < run.MaxTries[taskID]`).
   So an eviction, an OOM-kill, or a node loss burns one of the user's declared
   `retries` attempts, exactly as if the user's code had thrown. This is a bug:
   an infra fault is not a task failure.

   The striking part is that **Leoflow already does the right thing on two
   adjacent paths** — the split below *generalizes existing logic, it does not
   invent it*:

   - **Synchronous dispatch failure** uses a *separate* counter,
     `DispatchAttempts` (`scheduler.go:77-81`), and its handler
     (`handleDispatchFailure`, `scheduler.go:898-920`) is documented "A dispatch
     failure is infrastructure, not a task failure, so it never consumes the
     task's `try_number`" (ADR 0031 Amendment A). It backs off, and on exhaustion
     fails as the *distinct* terminal state `dispatch_failed`.
   - **Reschedule** (sensor `up_for_reschedule`) preserves `try_number` outright
     — "Re-dispatch once reschedule_at passes, WITHOUT consuming retry budget"
     (`plan.go:85-95`).

   Two of the four ways a task can be re-placed already refuse to bill the user's
   retry budget. The three execution-fault reapers are the outlier. The seam this ADR draws
   is the one that already exists in two places, made uniform.

4. **#543: the agent's exit code conflates "task outcome" with "report
   delivery."** The agent process exit status is used as both "did the task
   succeed" and "did the report get delivered." A pod killed mid-report shows
   `Failed` even when the task itself succeeded (`internal/agent/runner.go:408-420`
   — a non-zero exit or run error routes to `fail`, ahead of the terminal
   `report(...SUCCESS)`), and the reconciler cannot recover a success from pod
   phase because a failed pod is the only durable signal it has to retry the
   report from (`internal/executor/reconcile.go:105-116`). The outcome and its
   delivery are tangled into one value.

None of this is a design failure — it is the natural accretion of a second
concern onto a machine that never named it. This ADR names it.

## Decision

Introduce a clean seam between two layers that already exist implicitly, and make
the seam a first-class contract.

### The two layers

- **Orchestration state machine** — stays in `internal/scheduler`. Owns
  dependency planning, trigger rules, **task-retry policy** (`retriable`,
  `readyToRetry`, `MaxTries`), run finalization (`reap.go`), scheduling,
  catchup/cron, native alerting (`AlertsConfig`), and leader election (ADR 0009).
  It knows **tasks and edges. It never knows about pods.**

- **Execution state machine** — a new/expanded execution layer, seeded by
  today's `internal/executor`. Owns the **substrate**: it places work, owns the
  pod lifecycle, the `PodManager`, the reconciler (`reconcile.go`), and the three
  execution-fault reapers (`heartbeat_reap.go`, `pod_lost_reap.go`, `stale_queued_reap.go`).
  It performs **infra-retry** — bounded, backed off, and it **does not burn the
  task-retry budget** (exactly as `DispatchAttempts` already does). It delivers
  **exactly one outcome** per work item.

### The seam contract

Down the seam goes a **`WorkItem`**:

- the substrate payload — image, args, env, resources;
- plus an **opaque `Correlation` bag**: `{dag_id, run_id, task_id, try_number,
  tenant_id, trace_id}`.

Up the seam comes an **`ExecutionOutcome`**, one of:

- **`Succeeded`** — the task ran and finished cleanly.
- **`Failed(appReason)`** — the task's own code failed (the exception rail;
  *consumes* `try_number` via `retriable()`).
- **`Unexecutable(infraReason)`** — the substrate could not run it, or lost it
  (eviction, OOM, node loss, dispatch-lost). Routes to the **infra-retry**
  counter; **never** consumes `try_number`.
- **`Rescheduled`** — a deferral (sensor reschedule); preserves `try_number`,
  exactly as `plan.go:85-95` does today.

The execution layer treats `Correlation` as **opaque**: it propagates it to pod
labels, task env, and trace spans, and **never interprets it**. The orchestration
layer is the only reader of those coordinates. This is the invariant that keeps
the two machines decoupled — the substrate carries the DAG's coordinates as
metadata without ever understanding the DAG.

`Unexecutable` is the type that fixes finding #3: the three execution-fault reapers stop
writing plain `failed` and instead surface `Unexecutable`, which the
orchestration layer routes to a bounded, no-budget re-place counter — the
generalization of `DispatchAttempts` from "dispatch call failed" to "the substrate
lost the work at any point." `dispatch_failed` becomes one member of a small,
named family of infra-terminal states rather than a special case.

### What this is (and is not)

This **refines** the existing design. The `Executor` interface with its
subprocess and Kubernetes implementations (ADR 0027) is the seed of the execution
layer; this ADR **re-homes** the reapers/reconciler behind it and **hardens the
interface** to return an outcome instead of a bare dispatch error. It is **not a
rewrite** and **not a new coordination substrate.**

## Key properties

**Substrate-agnostic — Lite-safe, Postgres-safe, no CRD.** The contract is a
`WorkItem` down and an `ExecutionOutcome` up; a subprocess has no CRD but it still
has an outcome. **Postgres stays the single source of truth** (ADR 0031); the only
schema change is **one additive migration**: a `last_failure_kind` column on
`task_instances` that records app-vs-infra so the orchestration layer can route
without re-deriving. No new datastore, no etcd, no per-task custom resource. This
is a hard requirement, not a preference — see the rejected CRD alternative, whose
Lite blocker is dispositive.

**Best tool per problem, one contract.** Each execution adapter uses the best
substrate for its edition: a **subprocess** for Lite (µs fork, zero isolation,
dev-only per ADR 0027), **k8s-native pods** driven by a controller for Pro
(pod-per-task, real isolation, ADR 0002/0015), and the seam stays open to future
adapters. All of them satisfy the same `WorkItem`/`ExecutionOutcome` contract, so
the orchestration layer is written once and is edition-blind — the same property
ADR 0031 already guarantees for the state machine, now extended to the outcome.

**Observability is preserved and improved.** The DAG coordinates ride as metadata
the execution layer *propagates but does not interpret*. Leoflow already stamps
them at every layer:

- **pod labels** `leoflow.io/{dag-id,task-id,run-id,try-number,tenant-id}`
  (`internal/executor/kubernetes.go:65-71`);
- **structured JSONL log fields** keyed on `tenant_id/dag_id/run_id/task_id`
  (`internal/logs/logs.go:188-195`);
- **OTLP tracing** (`internal/observability`, ADR 0010).

The target the naming unlocks: a **run → task → pod OpenTelemetry span tree**, and
three-pillars correlation — Prometheus metrics with **low-cardinality**
`dag_id`/`task_id` labels, Loki logs, Tempo traces, joined by the shared IDs and
by exemplars. **Market precedent:** Argo Workflows' pods also do not understand
the DAG; they carry workflow-coordinate labels the controller reads. Carrying the
DAG's coordinates as opaque metadata across an execution boundary is the standard
shape, not an invention.

**The at-most-once guards are preserved verbatim.** Crash-consistency across the
seam is non-negotiable. The existing guards — a source-state CAS on reaper writes
(`WHERE state = 'running'` for the pod/agent reapers, `WHERE state = 'queued'` for
the dispatch-lost reaper), `ON CONFLICT DO NOTHING` on the durable-outcome path,
and `ErrStaleReport` on out-of-order agent reports — stay exactly as they are (ADR 0031's
leader-overlap correctness section). The seam changes *who* writes the outcome and
*what type* it is, not the concurrency discipline that makes the write safe.

## Consequences

**Fixes two live bugs.**

- The **infra-vs-task retry conflation** (finding #3): an eviction/OOM/node-loss
  stops billing the user's `retries`. A `retries: 0` task no longer dies on a
  node blip.
- **#543**: separating the outcome from its delivery lets a success survive a pod
  killed mid-report, and gives the reconciler a defined path to recover it.

**Materially simplifies the scheduler.** Three execution-concern reapers, the
reconciler, and the `PodManager` leave `internal/scheduler`. What remains there is
purely orchestration: plan, finalize, schedule, alert, elect. The scheduler stops
reasoning about pods.

**Orthogonal to multi-tenancy and to scale — be explicit.** This ADR does **not**
touch tenant isolation (#508 / #209): `tenant_id` rides the `Correlation` bag as
one more opaque coordinate, unchanged. It does **not** touch horizontal scale
(#525): the split is a layering seam, not a topology change, and it composes
cleanly with the API/scheduler process split (ADR 0049) without depending on it.
Anyone reading this expecting a tenancy or scale decision should look elsewhere.

**Honest hard parts.** Naming the seam does not make these free:

- **App-vs-infra classification.** Deciding whether a given failure is
  `Failed(appReason)` or `Unexecutable(infraReason)` is the crux. Pod phase +
  termination reason (`Evicted`, `OOMKilled`, `DeadlineExceeded`, node
  `NotReady`) is the signal; misclassifying an app failure as infra would let a
  genuinely-broken task retry forever.
- **Bounded infra-retry policy — the poison-placement guard.** Infra-retry must
  be bounded and backed off exactly like `DispatchAttempts`, or a task that is
  *unplaceable* (bad image, unsatisfiable resources) becomes an infinite re-place
  loop. Exhaustion must terminate in a named infra-terminal state, visible to the
  operator.
- **The durable outcome signal is best-effort on node loss.** The pod termination
  log is capped at **4 KB**, and an `emptyDir`-backed outcome file is
  **unreadable once the node is gone**. So `Succeeded` recovery from a lost node
  cannot be guaranteed; **node-loss stays best-effort** and falls back to
  `Unexecutable` — which is the correct conservative default (re-place without
  billing the user), but it means a task that *actually* succeeded on a node that
  then vanished may be re-placed. This is an accepted limitation, called out so it
  is not mistaken for solved.
- **Crash-consistency across the new boundary.** Every guard listed above must
  hold when the outcome write and the state transition straddle the seam and a
  crash lands between them. The reconciliation model (idempotent, DB-derived
  every tick) is what makes this tractable, but each phase's PR must prove it.

## Alternatives considered

1. **Full CRD-native control plane** (each task/run a custom resource; the
   controller reconciles etcd). Rejected. It is the etcd worst-case workload
   (high-churn, short-lived objects); **Argo itself had to add a SQL backend**
   after hitting exactly this; and it has a **hard Lite blocker — Lite has no
   Kubernetes API server** (ADR 0027: Lite is a self-contained binary, and its
   `subprocess` executor has no cluster at all). A contract that cannot be
   satisfied on Lite fails the editions requirement outright. Postgres-as-truth
   (ADR 0031) already gives durable, queryable state without etcd.

2. **Pro-only read-only CRD *projection*** (mirror runs/tasks as CRs for
   `kubectl`/GitOps *visibility*, DB stays the truth). **Viable later** and worth
   its own ADR — it buys operator ergonomics on Pro — but it is **orthogonal** to
   this decision: it is a read model, not the execution seam. Not adopted here,
   not foreclosed.

3. **Status quo** (leave the two machines fused). Rejected: it is the direct cause
   of #543 and of the retry-budget bug, and it keeps three execution-concern
   reapers and the `PodManager` welded into the scheduler.

## Phased path

Each phase ships **independently**, **failing-test-first** (ADR 0011), and is
**ADR-gated where it changes observable behavior**. Ordered by value-over-blast:

- **Phase 0 — name the outcome.** Introduce `ExecutionOutcome` and the additive
  `last_failure_kind` column. Purely additive; nothing routes on it yet. No
  behavior change.
- **Phase 1 — fix the retry conflation** (highest value, lowest blast). Route
  `Unexecutable` faults from the three execution-fault reapers to a bounded, no-budget
  re-place counter — the generalization of `DispatchAttempts`. This is the bug
  fix users feel: infra faults stop eating `retries`.
- **Phase 2 — durable outcome + #543.** Separate the task outcome from its report
  delivery; give the reconciler a defined success-recovery path within the
  best-effort limits stated above.
- **Phase 3 — re-home the reapers.** Move `heartbeat_reap.go`,
  `pod_lost_reap.go`, `stale_queued_reap.go`, the reconciler, and `PodManager`
  behind the execution layer, leaving `internal/scheduler` orchestration-only.

## References

- ADR 0031 — Scheduler architecture (reconciliation loop; Amendment A:
  `DispatchAttempts`, `dispatch_failed`, "infra, not a task failure").
- ADR 0027 — Editions: `subprocess` (Lite) vs Kubernetes (Pro) executors.
- ADR 0015 / ADR 0002 — Kubernetes-only, pod-per-task execution.
- ADR 0004 — Thin agent (reports state over gRPC).
- ADR 0010 — Observability (OTLP tracing).
- `internal/executor/executor.go:100` — the `Executor` interface returning only a
  dispatch error.
- `internal/scheduler/plan.go:85-95, 104-107` — reschedule preserving
  `try_number`; the `retriable()` rail.
- `internal/scheduler/scheduler.go:77-81, 898-920` — the `DispatchAttempts`
  precedent this ADR generalizes.
- `internal/scheduler/pod_lost_reap.go:63-64, 92-93` — the Kubernetes-only,
  Lite-no-op pod reaper.
- `internal/agent/runner.go:408-420` / `internal/executor/reconcile.go:105-116` —
  #543: outcome vs report-delivery conflation.
- `internal/executor/kubernetes.go:65-71` / `internal/logs/logs.go:188-195` — the
  DAG-coordinate labels and JSONL fields the execution layer propagates.
- Argo Workflows — pods carry workflow-coordinate labels the controller reads; the
  project added a SQL backend after etcd pressure.
