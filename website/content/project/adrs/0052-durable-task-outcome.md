---
title: "ADR 0052: Durable task outcome — decouple the task result from report delivery"
linkTitle: 0052 · Durable task outcome — decouple the task result from report delivery
weight: 520
description: "ADR 0052: Durable task outcome — decouple the task result from report delivery"
---

**Status:** Proposed
**Date:** 2026-08-13
**Relates:** ADR 0051 (separate the orchestration and execution state machines — this ADR is its Phase 2), ADR 0031 (scheduler reconciliation loop + two-layer reaping), ADR 0015 (Kubernetes-only container execution), ADR 0004 (thin agent), ADR 0002 (pod-per-task — an assumption this ADR is careful not to deepen)
**Issues:** #543 (agent exit code conflates task outcome with report delivery); follow-up to #542 (in-process report retry)

## Context

A task pod's true outcome and the *delivery* of that outcome are tangled into one
value: the agent process's exit code.

The agent runs the user process, then delivers the result over gRPC. If the
terminal report — or one of the pre-report pushes (return value, extra links,
XCom) — fails, the agent routes to `fail` and exits non-zero
(`internal/agent/runner.go:408-420`). So a pod **killed mid-report** (OOM,
eviction) shows Kubernetes phase `Failed` **even when the user task itself
succeeded**.

The reconciler is the backstop for a pod that never delivered, but it can only
read **pod phase** (`classifyPod`, `internal/executor/reconcile.go:39-55`, invoked
in the loop at `:103-116`): a `Failed` pod is settled as a task failure via
`reportFailure`. It has no way to recover a success
from phase alone, because the only durable signal — the pod — says `Failed`.

### This is a classic distributed-systems problem, and there are two schools

At bottom this is the *atomicity of a side effect and its record*: the work
happened, but the record of it did not survive the worker's death. The industry
answers it in one of two ways.

- **School A — recover the lost outcome.** Extract the truth from a durable,
  per-worker artifact left behind at exit. Best-effort: it only works while the
  node/kubelet survives to surface the artifact. It cannot survive node loss.
- **School B — the outcome does not exist until the orchestrator durably records
  it; on ambiguity, re-drive.** The source of truth is a store the orchestrator
  owns; a result is real only once written there; a lost report means *not done*,
  so the task is retried. Correctness comes from **at-least-once execution +
  idempotency**, not from autopsying the worker. Correct under *any* failure,
  node loss included. This is the durable-execution lineage (Temporal/Cadence)
  and the Borg/Spanner "durable intent + idempotent re-drive" pattern.

Apache Airflow — our UI/API compatibility target — is worse than School A here:
its KubernetesExecutor does not even try to recover. A pod that dies without
reporting becomes a **zombie task**, is marked `failed`, and is re-run; the whole
"tasks must be idempotent" doctrine exists precisely because the framework
re-drives on ambiguity. It accepts the false-negative and pushes the burden to
the user.

**leoflow is already on School B.** The reconciliation loop and two-layer reaper
(ADR 0031) re-drive on ambiguity, and ADR 0051 Phase 1 re-places an infra fault
*without consuming the user's retry budget*. That is the correctness foundation,
and it holds under node loss. This ADR does **not** replace it. This ADR adds a
**School-A optimization on top of the School-B floor**: when the kubelet survives,
recover a lost *success* so an expensive or non-idempotent task is not needlessly
re-run.

> "Have the reconciler settle a `Succeeded` pod as success" is nearly a no-op:
> when the success report fails the agent exits non-zero, so the pod is `Failed`,
> never `Succeeded`. The bug is the conflation, not the reconciler's reading of a
> `Succeeded` phase.

#542 adds an in-process retry of the report, which fixes the common transient
blip while the pod is still alive. It cannot fix the case where the pod is gone
before the retry budget is spent. That surviving-kubelet backstop is this ADR.

## Decision

**Correctness rests on School B — idempotent re-drive (ADR 0031 + ADR 0051 Phase
1) — which this ADR does not change. On top of it, the agent writes the true task
outcome to a durable, pod-local location *before* it attempts to deliver the
report; the reconciler reads that record as the source of truth over pod phase,
recovering a lost success so a costly or non-idempotent task is not re-run
needlessly.**

The record is an **optimization that reduces spurious re-runs**, not the
mechanism that guarantees correctness. Where the record is absent (node loss, old
agent), the School-B floor still settles the task safely by re-drive.

### The durable outcome record

A compact JSON document written to the container **termination message**
(`/dev/termination-log`, surfaced by Kubernetes on
`pod.Status.ContainerStatuses[].State.Terminated.Message`):

```json
{"v":1,"outcome":"success"}
{"v":1,"outcome":"failed","exit_code":1}
{"v":1,"outcome":"reschedule","reschedule_at":"2026-08-13T12:34:56Z"}
```

- `outcome` ∈ `success | failed | reschedule` — the *task's* result, independent
  of whether the report was delivered.
- `exit_code` — the user process exit code, for a `failed` outcome.
- `reschedule_at` — RFC3339 next-poke time, **required** for a `reschedule`
  outcome (see "Reschedule carries its next-poke time" below).
- Kept well under the Kubernetes termination-message cap (~4 KiB); it carries the
  outcome, never logs or the return value (those keep their existing paths).

### Prerequisite: the container must pin the termination-message policy

The whole contract rests on the container surfacing `/dev/termination-log` on pod
status. Today `BuildPod` (`internal/executor/kubernetes.go:81-88`) sets **neither**
`TerminationMessagePolicy` nor `TerminationMessagePath`; it works only by the
Kubernetes API default (`File` / `/dev/termination-log`), which an admission
webhook or PodSecurity policy could mutate without notice. This ADR makes it an
**explicit, tested contract**: `BuildPod` pins
`TerminationMessagePolicy: corev1.TerminationMessageReadFile` and
`TerminationMessagePath: "/dev/termination-log"` on the task container, with a
unit test asserting both. The k3d/chaos step additionally asserts the agent can
*write* the file under the operator-configurable `RunAsNonRoot` +
`ReadOnlyRootFilesystem` security context (`buildSecurityContext`,
`kubernetes.go:221-234`). We do **not** set `FallbackToLogsOnError`: it would let
a failed pod populate the message from the log tail, which the reader would then
try (and safely fail) to decode — avoided outright.

### Write ordering: the success record follows the pushes

The record is written **path-specifically at the point the outcome becomes true**,
not on a single "user process exited" event. To avoid missing a terminal sink, the
write is keyed off the `state` the agent is about to report inside the terminal
path — not bolted onto individual call sites:

- The `success` record is written **only after every pre-report push (return
  value, extra links, custom XComs) has been accepted** — immediately before
  `report(SUCCESS)`. A kill *during* the pushes therefore leaves **no** success
  record → fallback → failure → idempotent re-drive.
- The `failed` record must cover **every** failure sink: the `fail(...)` calls
  (`runner.go:409-418`) **and** the execution-timeout path `failWithReason(...)`
  (`runner.go:396, 426-431`), which reports `FAILED` without going through
  `fail()`. Keying the write off the reported state (rather than enumerating call
  sites) is what guarantees the timeout sink is not missed.
- The `reschedule` record is written immediately before `reportReschedule(...)`,
  carrying the parsed next-poke time.

**This ordering is forward-looking, not a present-day fix.** Return-value,
extra-links, and custom-XCom persistence is **not implemented today** — those
pushes short-circuit on `codes.Unimplemented` and store nothing
(`runner.go:463-468`; XCom lands in a later phase). So "success only after the
pushes" currently guards a gap that **cannot occur yet** (the pushes always
"succeed" by being skipped). The constraint hardens the write site now so that
when persistence lands, a recovered success cannot have missing downstream data;
it buys nothing until then. Called out so the win is not mistaken for a
present-day correctness improvement.

### The reconciler reads it as source of truth (attempt-guarded)

`classifyPod` (`internal/executor/reconcile.go:39`) is extended: when a terminated
container carries a decodable outcome record, it is trusted over the pod phase.

**This is a reconciler-seam expansion, not merely a read.** Today `classifyPod`
returns a 3-valued phase enum (`podPending | podFailed | podSucceeded`,
`reconcile.go:14-25`) and `Reconcile` only ever *settles* a `podFailed` pod, via a
single `FailureReporter.FailTask` (`reconcile.go:67-69, 113-117`) — a `Succeeded`
pod is merely GC'd on age, with no success- or reschedule-settle path at all.
Consuming the record therefore requires: (a) a richer `classifyPod` return that
carries the outcome, `exit_code`, and `reschedule_at`; (b) new settle methods
beyond `FailTask` for the success and reschedule paths; (c) **two new
`try_number`-guarded queries** — a succeed and a reschedule settle — alongside the
existing `FailTaskInstanceIfActive`; and (d) new branches in the `Reconcile` loop.
This is scoped in Step 2 below.

- Record says `success` on a `Failed` pod → settle the task **succeeded** (the
  report was lost, the work was not).
- Record says `failed` → settle failed with the recorded `exit_code` (now
  authoritative rather than inferred).
- Record says `reschedule` → route to `up_for_reschedule` using the record's
  `reschedule_at`; the reconciler **never settles a reschedule as a failure**
  (excludes the bare exit-75 path, #386).
- **No record** (old agent, non-graceful kill before the write, node loss) → fall
  back to today's phase-based behavior; the School-B floor re-drives safely.

**Idempotent, attempt-guarded settle.** The reconciler's settle must not clobber a
*different attempt*. The current reconciler path — `FailTaskInstanceIfActive`
(`internal/storage/queries/runs.sql:341-344`) — guards on `id AND state IN
(...)`, with **no `try_number`**, unlike the agent path `ReportTaskResult`
(`runs.sql:346+`), which guards on state **and** try_number precisely because
retries bump `try_number` **in place** on the same row. Because the pod annotation
carries a specific attempt, a stale reconciler acting on a previous attempt's
lingering `Failed` pod could match `id AND state='running'` on the **new** running
attempt. Today that only fails a live retry (recoverable); once the reconciler also
writes **success**, the same stale match could mark a live retry *succeeded* and
fire downstream on incomplete work — strictly worse than the bug being fixed.
This ADR therefore specifies a **new try_number-guarded settle for the reconciler**
(both the success and failure paths): `WHERE id=$1 AND try_number=$2 AND state IN
(...)`, threading the `leoflow.io/try-number` pod label (already set in `BuildPod`)
into the settle. Whichever of the reconciler and a late agent report writes first
wins; the other is a no-op. `on_failure_callback` (#424) should fire via the
standard `failed`-state handling for a reconciler-driven failure, exactly as for
an agent-driven one — Step 2 must assert this, since the settle is a bare `UPDATE`
and the callback dispatch keys off the transition, not the query. (The claim in
the earlier draft that we could "reuse the existing guard" was wrong — that guard
is not on the reconciler path.)

## Key properties

- **Correctness is School B; this is an optimization.** Absent the record, tasks
  still settle safely by idempotent re-drive; the record only spares needless
  re-runs when the kubelet survived.
- **Recovers surviving-kubelet kills only.** OOM kill, node-pressure eviction, and
  `ActiveDeadline` — where the kubelet lives to flush the termination message — are
  recovered. **Node loss / non-graceful shutdown successes remain unrecoverable**
  by this mechanism (dead kubelet → no ContainerStatuses update → no record); they
  degrade to phase → failure → idempotent re-drive. Stated honestly rather than
  over-claimed.
- **Kubernetes-only.** Lite (subprocess, no pod, in-process delivery) gains
  nothing and is unchanged — #542's in-process retry is Lite's primary fix.
- **Additive and back-compatible.** No schema change; an agent that does not write
  the record degrades to phase-based behavior.
- **Transport is bound to the dedicated-pod case; the contract is not.** The
  termination message is a per-container-*exit* artifact: it can carry a task's
  outcome only because today the pod runs exactly one task and exits at that task's
  end (ADR 0002). The planned model keeps a task **atomic to one pod** but lets that
  pod be **dedicated** (exits at the task's end — termination message still works)
  **or shared** with sibling tasks (a warm worker that does *not* exit per task —
  the termination message cannot carry a per-task outcome). The orchestration side
  consumes the outcome through the execution seam (`ExecutionStore`), **never pod
  phase or the transport directly**, so a shared pod simply adds a *second*
  transport — a long-lived worker reporting each task-attempt's outcome in-band and
  durably, which is precisely Temporal's worker model — without touching the
  orchestration side. This ADR is careful not to deepen the pod-per-task assumption.

## Consequences

- The most damaging false-negative in the execution path — a task that succeeded
  but lost its report — is closed for the common kill classes; the residue is
  safe re-drive. This is the concrete answer to Airflow's zombie-task false
  negative.
- Contained but not trivial. On the execution side: a write in the agent's
  terminal path and a policy pin in `BuildPod`. On the orchestration side: a richer
  `classifyPod` return, new success/reschedule settle methods, two new
  `try_number`-guarded queries, and new `Reconcile` branches. No new coordination
  substrate and no control-plane schema change — but this **expands the reconciler
  seam**, it is not "just a read".
- The termination message becomes a **contract** between agent and reconciler,
  versioned by `v` so the format can evolve.
- The reconciler grows one dependency on a Kubernetes-surfaced field; the fallback
  keeps it correct where the field is absent.

## Alternatives considered

- **Trust pod phase (the naive backstop).** Rejected: a lost success report makes
  the pod `Failed`, so phase can never recover the success. This is the no-op the
  issue calls out.
- **A file in a shared `emptyDir`.** Viable, but needs a shared mount and a reader
  with pod-filesystem access (a sidecar or exec), plus cleanup. The termination
  message is Kubernetes-native, rides the pod status the reconciler already lists,
  and needs no extra mount.
- **A pod annotation/label patch written by the agent (downward API / API write).**
  Rejected: it needs a live kubelet and a Kubernetes API write, and the task pod
  sets `AutomountServiceAccountToken: false` (`kubernetes.go:80`) to deny the
  agent API access by design. Same node-loss limit as the termination message,
  with more privilege.
- **Push the outcome to the control plane before exit.** That *is* the report —
  circular; the whole problem is that this delivery can fail.
- **Temporal / Cadence durable execution (School B, pure).** The Temporal Service
  owns all durable state as an event-sourced history in a database
  (Cassandra/MySQL/PostgreSQL); the worker holds no authoritative state. An
  activity is complete **only once the Service durably records the completion** the
  worker reports (`RespondActivityTaskCompleted`, keyed by a task token), not when
  the worker's code returns. A worker that finishes the work but dies before that
  record is written is simply invisible to the Service: Temporal **does not detect
  or salvage the lost success** — it relies on the Start-To-Close / Heartbeat
  timeout to notice the missing signal and **retries the activity** per policy.
  Execution is **at-least-once**; safety of the repeat is the developer's job via
  **idempotency keys** (there is no exactly-once *side-effect* primitive — the docs
  call it "effectively exactly-once"). We do not adopt this wholesale: leoflow is
  Airflow-compatible — a task is an **opaque user process in a pod**, not an
  activity in a durable-execution SDK that owns the code's structure and can replay
  it. But we adopt its **principle as our correctness floor** (School B: re-drive on
  ambiguity + idempotent tasks), which we already have via ADR 0031 + ADR 0051. Its
  long-lived worker (one process polling and reporting many attempts in-band) is
  exactly the *shared-pod* transport our N:1 future points at — further reason the
  outcome must be transport-abstract, not pinned to the termination message.
  (Sources: docs.temporal.io — architecture, activity-execution, detecting-activity-failures; temporal.io/blog/idempotency-and-durable-execution.)
- **Argo Workflows wait/emissary sidecar.** Argo's emissary executor runs the user
  command as a sub-process and writes its exit code to a file on a shared `emptyDir`
  (`/var/run/argo/ctr/<container>/exitcode`); a `wait` sidecar reads it, uploads
  artifacts, and reports outputs via a `WorkflowTaskResult` resource, while the
  controller reads **pod status from an informer** as the success/fail source of
  truth. This is an *intra-pod* artifact plus an extra container per task, and it
  has the **same node-loss blind spot** as ours: if the whole pod/node dies the
  sidecar dies too, no exitcode is written, and the controller marks the node
  **Error ("pod deleted")** — it never reconstructs a success; recovery is left to
  `retryStrategy` (notably `retryPolicy: OnError`), a full re-run assuming
  idempotency. We reject it because the termination message rides the pod status we
  **already `List`**, needing no sidecar, shared mount, or extra container — and it
  buys us nothing Argo's sidecar would past the same kubelet-alive boundary.
  (Sources: argoproj/argo-workflows — workflow-executors.md, emissary.go, operator.go; argo-workflows.readthedocs.io — tolerating-pod-deletion, retries.)
- **Apache Airflow KubernetesExecutor zombie handling (the anti-pattern we beat).**
  Airflow's source of truth is `TaskInstance.state` in the metadata DB, written by
  the **in-pod process itself**. A pod that dies before committing `success`
  becomes a **zombie**: the scheduler notices missing heartbeats (the "task
  instance heartbeat timeout", historically `scheduler_zombie_task_threshold`,
  default 300s) and **marks it failed or retries it — it never reconstructs a
  success from a dead pod**. `adopt_or_reset_orphaned_tasks` covers a dead
  *scheduler*, not a dead worker. This is exactly the false-negative this ADR
  closes for the surviving-kubelet case, and the basis of the "kill the zombie
  task" story: leoflow settles a succeeded-but-unreported task correctly instead of
  failing-and-re-running it. (Sources: airflow.apache.org — core-concepts/tasks,
  administration-and-deployment/scheduler.)

## Phased path

1. **This ADR** — the durable-outcome mechanism, the seam contract, and the
   School-B-floor / School-A-optimization framing.
2. **Core (no cluster).** Execution side: `BuildPod` pins the termination-message
   policy (tested); the agent writes the record keyed off the reported state,
   covering `fail(...)` **and** the `failWithReason(...)` timeout sink, the success
   record after the pushes. Orchestration side: a richer `classifyPod` return; new
   success/reschedule settle methods; two new `try_number`-guarded queries (succeed
   + reschedule) beside `FailTaskInstanceIfActive`; new `Reconcile` branches;
   reschedule-exclusion with `reschedule_at`; idempotent double-settle; and an
   assertion that `on_failure_callback` fires on the reconciler-driven `failed`
   transition. Unit-tested with a fake clientset and synthetic pod statuses.
3. **E2E / chaos (k3d).** Inject a pod kill mid-report on the operator/split E2E
   and the runtime chaos harness (#524); assert the correct terminal state, and
   assert the agent can write the termination file under the hardened security
   context.

## References

- ADR 0051 — Separate the orchestration and execution state machines (this is its Phase 2)
- ADR 0031 — Scheduler reconciliation loop + two-layer reaping (the School-B re-drive floor)
- #543 — Pro: durable task-outcome signal read by the reconciler
- #542 — in-process report retry (the primary, Lite-and-Pro fix); #541 — settle idempotency groundwork
- #424 — native on_failure_callback gating
- `internal/agent/runner.go` (terminal path), `internal/executor/reconcile.go` (`classifyPod`, `reportFailure`), `internal/executor/kubernetes.go` (container spec / policy prerequisite), `internal/storage/queries/runs.sql` (the `ReportTaskResult` vs `FailTaskInstanceIfActive` guard asymmetry)
