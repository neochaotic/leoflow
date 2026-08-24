---
title: Scheduler resilience
weight: 70
description: "How the scheduler survives restarts, leader loss and partial failure."
---

How Leoflow keeps the scheduler honest when something goes wrong: a process
dies, an agent goes silent, a dispatch is lost in flight. The control plane
ships **three reapers** — small, single-purpose loops that turn stuck state
back into observable terminal state, so the dashboard never lies about what's
actually running.

This applies to **both editions**: Lite (single-process) and Pro
(multi-replica with [leader election](/project/adrs/0009-leader-election/)). The
reapers run only on the leader — reaping writes state, and we want one writer
across the fleet.

## Recovery SLAs

| Failure mode | Detected by | Default SLA | What happens |
|---|---|---|---|
| **Task code wedged past its declared `execution_timeout_seconds`** | **Agent itself** ([#194](https://github.com/neochaotic/leoflow/issues/194)) | **`execution_timeout_seconds`** (per-task) | **TI failed with `execution_timeout: task exceeded N`. Retries kick in if budget remains.** |
| Agent process crashed mid-task (TI in `running`, no heartbeat) | TI heartbeat reaper ([#128](https://github.com/neochaotic/leoflow/issues/128)) | **90 s** | TI failed with `agent_lost`; the TI's pod is deleted so a partitioned-but-alive container stops. Retries kick in if budget remains. |
| Scheduler crashed before dispatching (TI stuck in `queued`) | Dispatch-lost reaper ([#202](https://github.com/neochaotic/leoflow/issues/202)) | **3 min** | TI failed with `dispatch_lost` — but only if no live pod for it exists (see below); the pod is torn down. Frees the run for the orphan reaper on the next tick. |
| Run stuck `running` with no active TIs (post-crash limbo) | Orphan-run reaper ([#120](https://github.com/neochaotic/leoflow/issues/120)) | **5 min** | Run failed with `orphaned`; any remaining active TIs flipped to `failed` and every pod of the run is deleted. |

Worst case end-to-end: a mid-tick scheduler crash that leaves TIs queued is
fully reaped within **`max(3 min, 5 min) = 5 min`** — the dispatch-lost reaper
runs first, then the orphan-run reaper picks up the now-no-active-TI run on
the next tick.

## Tuning the thresholds

Defaults are conservative. For tighter recovery on a fast-failing workload,
override via the scheduler interfaces:

```go
sched.SetAgentLostThreshold(30 * time.Second)
sched.SetDispatchLostThreshold(1 * time.Minute)
sched.SetOrphanThreshold(2 * time.Minute)
```

For most users the defaults are correct: too-tight thresholds risk reaping a
legitimately slow dispatch (Kubernetes pod-pull latency under contention) or
a busy agent.

## The "do no harm" rule

Each reaper requires a **positive observable signal** before failing
anything:

- **TI heartbeat reaper** — only fires on TIs that *did* heartbeat at least
  once and then went silent. A TI that never heartbeated (e.g. a pod that never
  started, so no agent ever reported) is left alone.
- **Dispatch-lost reaper** — requires a non-zero `queued_at` older than the
  threshold AND, on Kubernetes, confirmation that no live pod for the TI
  exists. If a pod for the TI is `Pending`/`Running`, the dispatch actually
  landed and the node is just slow to pull the image (a cold-node false
  positive, [#461](https://github.com/neochaotic/leoflow/issues/461)) — the
  reaper defers. If pod liveness can't be determined (K8s API error), it also
  defers. A TI without a `queued_at` stamp is too poorly observed to reap.
- **Orphan-run reaper** — requires `state = 'running'` AND no active TI on
  the run. A run with any TI in `scheduled`/`queued`/`running` is left alone
  (the dispatch-lost reaper unblocks this case by failing the stuck queued
  TIs first, so the next tick sees no active TIs).

## Tearing down the reaped task's pod

Failing a TI in the metadatabase is not enough on its own: a reaped task's
pod can still be running user code, which breaks at-most-once execution if
that work commits or a retry runs it again
([#474](https://github.com/neochaotic/leoflow/issues/474)). So, **after** the
durable DB transition, each reaper tears the pod down:

- The **heartbeat** and **dispatch-lost** reapers delete exactly the reaped
  TI's pod, pinned by `(run-id, task-id, try-number)` labels — a retry
  dispatches a new pod with a new try-number, so a newer live attempt can
  never be the one deleted.
- The **orphan-run** reaper deletes every pod of the abandoned run (the
  run-id is unique per run, so no other run's pod can match).
- Belt and suspenders: the control plane also answers a **stale** agent
  `ReportState`/`Heartbeat` — one whose attempt no longer matches the live
  row — with `should_terminate`, so a reaped-but-still-alive pod that we
  couldn't delete (e.g. during a K8s API outage) cancels its own work. The
  "stale" test is exactly the source-state + `try_number` guard the state
  write already uses ([#467](https://github.com/neochaotic/leoflow/issues/467)):
  the report applies for the live, matching attempt, so a live execution is
  never told to stop.

These teardown steps are best-effort and off the critical path: a delete
failure is logged and metered but never undoes the DB reap, and the pod's own
`activeDeadlineSeconds` plus the reconciler's GC remain backstops. In Lite
(subprocess executor) there are no pods, so only the DB transition and the
`should_terminate` signal apply.

## The load-bearing invariant

Recovery is bounded by the slowest reaper that applies, not the fastest —
usually fine, sometimes worth tuning
(ADR [0031](/project/adrs/0031-scheduler-architecture/)). The invariant that governs
every reap decision is **never fail or tear down the live current attempt —
only one that is genuinely stale or lost**. It is preserved end-to-end: the DB
transitions are guarded on source state (`WHERE state IN (...)`), pod deletes
are pinned to the exact `(run, task, try)` reaped, the dispatch-lost reaper
defers whenever a pod is live or its liveness is unknown, and the
`should_terminate` signal fires only when the reporting attempt has provably
moved on. When in doubt, a reaper defers rather than reap.

## Observability

Each reap action is metered as a scheduler decision. Watch these labels in
your Prometheus dashboard:

| Metric label | Meaning |
|---|---|
| `agent_lost` | TI failed by the heartbeat reaper |
| `dispatch_lost` | TI failed by the dispatch-lost reaper |
| `dispatch_lost_deferred` | Dispatch-lost skipped because the TI's pod is live (slow start, [#461](https://github.com/neochaotic/leoflow/issues/461)) — a healthy signal, not a fault |
| `orphan_reaped` | Run failed by the orphan-run reaper |
| `agent_lost_list_error`, `dispatch_lost_list_error`, `orphan_list_error` | Reaper's list query failed; next tick will retry |
| `dispatch_lost_pod_query_error` | Pod liveness could not be read (K8s API error); the reaper deferred rather than risk a false positive |
| `agent_lost_pod_delete_error`, `dispatch_lost_pod_delete_error`, `orphan_pod_delete_error` | Pod teardown after a reap failed; the DB reap stands and the pod's `activeDeadlineSeconds`/GC are backstops |

A sustained non-zero rate on any of these is worth investigating — reapers
are backstops, not the primary path; if they fire often, something upstream
is broken.

## What's NOT a scheduler concern

- **Postgres unreachable** — the scheduler's `Heartbeat()` goes unhealthy;
  the `/monitor/health` endpoint surfaces it; runs queue up and resume when
  the DB returns.
- **Agent's task container OOM-killed** — surfaces as a non-zero exit code
  through the agent (if it survived) or as `agent_lost` (if the agent went
  with it).
- **K8s API outage** — pods stay where they are; new dispatches fail at the
  executor layer (visible as `dispatch_failed` metric on the
  [BufferedDispatcher](/project/adrs/0031-scheduler-architecture/)); the
  dispatch-lost reaper does NOT fail TIs during the outage — it cannot read
  pod liveness, so it defers (`dispatch_lost_pod_query_error`) rather than
  risk a false positive. Reap-time pod teardown is likewise skipped and
  retried once the API returns; a reaped-but-alive pod stops itself via the
  `should_terminate` signal on its next heartbeat.

See also: [ADR 0009 (leader election)](/project/adrs/0009-leader-election/),
[ADR 0031 (scheduler architecture)](/project/adrs/0031-scheduler-architecture/).
