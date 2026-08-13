# ADR 0052: Durable task outcome — decouple the task result from report delivery

**Status:** Proposed
**Date:** 2026-08-13
**Relates:** ADR 0051 (separate the orchestration and execution state machines — this ADR is its Phase 2), ADR 0031 (scheduler reconciliation loop + two-layer reaping), ADR 0015 (Kubernetes-only container execution), ADR 0004 (thin agent), ADR 0002 (pod-per-task)
**Issues:** #543 (agent exit code conflates task outcome with report delivery); follow-up to #542 (in-process report retry)

## Context

A task pod's true outcome and the *delivery* of that outcome are tangled into one
value: the agent process's exit code.

The agent runs the user process, then delivers the result over gRPC. If the
terminal report — or one of the pre-report pushes (return value, extra links,
XCom) — fails, the agent routes to `fail` and exits non-zero
(`internal/agent/runner.go:408-420`). So a pod **killed mid-report** (OOM,
eviction, node loss) shows Kubernetes phase `Failed` **even when the user task
itself succeeded**.

The reconciler is the backstop for a pod that never delivered, but it can only
read **pod phase** (`internal/executor/reconcile.go:103-116`): a `Failed` pod is
settled as a task failure via `reportFailure`. It has no way to recover a success
from phase alone, because the only durable signal — the pod — says `Failed`.

#542 adds an in-process retry of the report, which fixes the common case (a
transient blip while the pod is still alive). It cannot fix the case where the
pod is gone before the retry budget is spent. That backstop is this ADR.

> "Have the reconciler settle a `Succeeded` pod as success" is nearly a no-op:
> when the success report fails the agent exits non-zero, so the pod is `Failed`,
> never `Succeeded`. The bug is the conflation, not the reconciler's reading of a
> `Succeeded` phase.

This is the execution-state-machine's outcome signal that ADR 0051 named but did
not yet make durable. This ADR makes it durable.

## Decision

**The agent writes the true task outcome to a durable, pod-local location
*before* it attempts to deliver the report; the reconciler reads that record as
the source of truth, falling back to pod phase only when it is absent.**

### The durable outcome record

A compact JSON document written to the container **termination message**
(`/dev/termination-log`, surfaced by Kubernetes on
`pod.Status.ContainerStatuses[].State.Terminated.Message`):

```json
{"v":1,"outcome":"success"}
{"v":1,"outcome":"failed","exit_code":1}
{"v":1,"outcome":"reschedule"}
```

- `outcome` ∈ `success | failed | reschedule` — the *task's* result, independent
  of whether the report was delivered.
- `exit_code` — the user process exit code, for a `failed` outcome.
- Kept well under the Kubernetes termination-message cap (~4 KiB); it carries the
  outcome, not logs or the return value (those keep their existing paths).

The agent writes this record at the moment it *decides* the outcome — before the
`report(...)` / `fail(...)` gRPC call — so a kill during delivery still leaves the
truth behind.

### The reconciler reads it as source of truth

`classifyPod` (`internal/executor/reconcile.go:39`) is extended: when a
terminated container carries a decodable outcome record, it is trusted over the
pod phase.

- Record says `success` on a `Failed` pod → settle the task **succeeded** (the
  report was lost, the work was not).
- Record says `failed` → settle failed with the recorded `exit_code` (same as
  today, now authoritative rather than inferred).
- Record says `reschedule` → route to `up_for_reschedule`; the reconciler **never
  settles a reschedule as a failure** (excludes the exit-75 path, #386).
- **No record** (old agent, non-graceful kill before the write, a crash that
  never reached the decision) → fall back to today's phase-based behavior.

### Idempotent, single-outcome settle

The reconciler's settle and a late agent report must converge to exactly one
outcome. Reuse the existing `WHERE state='running' [AND try_number=…]` guard
(the #541/#542 groundwork): whichever writes first wins; the other is a no-op.
`on_failure_callback` (#424) fires consistently for a reconciler-driven failure,
exactly as for an agent-driven one.

## Key properties

- **Exactly one outcome per attempt**, recoverable even if the report is never
  delivered.
- **Kubernetes-only.** Lite (subprocess, no pod, in-process delivery) gains
  nothing and is unchanged — #542's in-process retry is Lite's primary fix.
- **Additive and back-compatible.** An agent that does not write the record, or a
  pod killed before the write, degrades to today's phase-based behavior; no
  schema change.
- **Bounded.** The record is a few bytes; it never carries logs or return values.

## Consequences

- A task that succeeds but loses its report is no longer marked failed — the
  most damaging false-negative in the execution path is closed.
- Small, contained change: a write in the agent's terminal path and a read in
  `classifyPod`; no new coordination substrate, no control-plane schema change.
- The termination message becomes a **contract** between agent and reconciler,
  versioned by `v` so the format can evolve.
- The reconciler grows one dependency on a Kubernetes-surfaced field; the
  fallback keeps it correct where the field is absent.

## Alternatives considered

- **Trust pod phase (the naive backstop).** Rejected: a lost success report makes
  the pod `Failed`, so phase can never recover the success. This is the no-op the
  issue calls out.
- **A file in a shared `emptyDir`.** Viable, but needs a shared mount and a
  reader with pod-filesystem access (a sidecar or exec), plus cleanup. The
  termination message is Kubernetes-native, surfaced on the pod status the
  reconciler already lists, and needs no extra mount.
- **Push the outcome to the control plane before exit.** That *is* the report —
  circular; the whole problem is that this delivery can fail.

## Phased path

1. **This ADR** — the durable-outcome mechanism and the seam contract.
2. **Core (no cluster).** Agent writes the termination-message record; `classifyPod`
   + the reconciler read and settle it; reschedule-exclusion; idempotent
   double-settle. Unit-tested with a fake clientset and synthetic pod statuses.
3. **E2E / chaos (k3d).** Inject a pod kill mid-report on the operator/split E2E
   and the runtime chaos harness (#524); assert the correct terminal state.

## References

- ADR 0051 — Separate the orchestration and execution state machines (this is its Phase 2)
- #543 — Pro: durable task-outcome signal read by the reconciler
- #542 — in-process report retry (the primary, Lite-and-Pro fix); #541 — settle idempotency groundwork
- #424 — native on_failure_callback gating
- `internal/agent/runner.go` (terminal path), `internal/executor/reconcile.go` (`classifyPod`, `reportFailure`)
