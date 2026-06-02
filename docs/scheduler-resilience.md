# Scheduler resilience

How Leoflow keeps the scheduler honest when something goes wrong: a process
dies, an agent goes silent, a dispatch is lost in flight. The control plane
ships **three reapers** — small, single-purpose loops that turn stuck state
back into observable terminal state, so the dashboard never lies about what's
actually running.

This applies to **both editions**: Lite (single-process) and Pro
(multi-replica with [leader election](adr/0009-leader-election.md)). The
reapers run only on the leader — reaping writes state, and we want one writer
across the fleet.

## Recovery SLAs

| Failure mode | Detected by | Default SLA | What happens |
|---|---|---|---|
| **Task code wedged past its declared `execution_timeout_seconds`** | **Agent itself** ([#194](https://github.com/neochaotic/leoflow/issues/194)) | **`execution_timeout_seconds`** (per-task) | **TI failed with `execution_timeout: task exceeded N`. Retries kick in if budget remains.** |
| Agent process crashed mid-task (TI in `running`, no heartbeat) | TI heartbeat reaper ([#128](https://github.com/neochaotic/leoflow/issues/128)) | **90 s** | TI failed with `agent_lost`. Retries kick in if budget remains. |
| Scheduler crashed before dispatching (TI stuck in `queued`) | Dispatch-lost reaper ([#202](https://github.com/neochaotic/leoflow/issues/202)) | **3 min** | TI failed with `dispatch_lost`. Frees the run for the orphan reaper on the next tick. |
| Run stuck `running` with no active TIs (post-crash limbo) | Orphan-run reaper ([#120](https://github.com/neochaotic/leoflow/issues/120)) | **5 min** | Run failed with `orphaned`; any remaining active TIs flipped to `failed`. |

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
  once and then went silent. A TI that never heartbeated (e.g. an inline
  http_api task with no agent) is left alone.
- **Dispatch-lost reaper** — requires a non-zero `queued_at` older than the
  threshold. A TI without that stamp is too poorly observed to reap.
- **Orphan-run reaper** — requires `state = 'running'` AND no active TI on
  the run. A run with any TI in `scheduled`/`queued`/`running` is left alone
  (the dispatch-lost reaper unblocks this case by failing the stuck queued
  TIs first, so the next tick sees no active TIs).

This rule is the load-bearing invariant: **the reapers can never kill a live
execution** (ADR [0031](adr/0031-scheduler-architecture.md)). The cost is
that recovery is bounded by the slowest reaper that applies, not the
fastest — usually fine, sometimes worth tuning.

## Observability

Each reap action is metered as a scheduler decision. Watch these labels in
your Prometheus dashboard:

| Metric label | Meaning |
|---|---|
| `agent_lost` | TI failed by the heartbeat reaper |
| `dispatch_lost` | TI failed by the dispatch-lost reaper |
| `orphan_reaped` | Run failed by the orphan-run reaper |
| `agent_lost_list_error`, `dispatch_lost_list_error`, `orphan_list_error` | Reaper's list query failed; next tick will retry |

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
  [BufferedDispatcher](adr/0031-scheduler-architecture.md)); the
  dispatch-lost reaper does NOT fire (queued_at is fresh, the issue is the
  API, not the scheduler).

See also: [ADR 0009 (leader election)](adr/0009-leader-election.md),
[ADR 0031 (scheduler architecture)](adr/0031-scheduler-architecture.md).
