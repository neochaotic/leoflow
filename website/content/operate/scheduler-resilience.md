---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /scheduler-resilience.html
# --- end AUTO redirect aliases ---
title: Scheduler resilience
weight: 70
description: "How the scheduler survives restarts, leader loss and partial failure."
---

How Leoflow keeps the scheduler honest when something goes wrong: a process
dies, an agent goes silent, a dispatch is lost in flight. The control plane
ships **five reapers** — small, single-purpose backstops that turn stuck state
back into observable terminal state, so the dashboard never lies about what's
actually running.

This applies to **both editions**: Lite (single-process) and Pro
(multi-replica with [leader election](/project/adrs/0009-leader-election/)). The
reapers run only on the leader — reaping writes state, and we want one writer
across the fleet. On Kubernetes they run from the leader's **maintenance loop**
(every 30 s, after the pod reconciler's sweep — see below); in Lite there are no
pods, so no pod-based reaper applies and the loop is not started.

## Recovery SLAs

| Failure mode | Detected by | Default SLA | What happens |
|---|---|---|---|
| **Task code wedged past its declared `execution_timeout_seconds`** | **Agent itself** ([#194](https://github.com/neochaotic/leoflow/issues/194)) | **`execution_timeout_seconds`** (per-task) | **TI failed with `execution_timeout: task exceeded N`. Retries kick in if budget remains.** |
| Agent process crashed mid-task (TI in `running`, no heartbeat) | TI heartbeat reaper ([#128](https://github.com/neochaotic/leoflow/issues/128)) | **90 s** | TI failed with `agent_lost`; the TI's pod is deleted so a partitioned-but-alive container stops. Retries kick in if budget remains. |
| Scheduler crashed before dispatching (TI stuck in `queued`) | Dispatch-lost reaper ([#202](https://github.com/neochaotic/leoflow/issues/202)) | **3 min** | TI failed with `dispatch_lost` — but only if no live pod for it exists (see below); the pod is torn down. Frees the run for the orphan reaper on the next maintenance cycle. |
| Task pod vanished (TI in `running`, no pod at all for its attempt) | Pod-lost reaper | **60 s** after the running transition, then a live pod read | TI failed with `pod_lost`. Only when the apiserver holds no pod for the attempt: a pod that is still there in a terminal phase is left for the reconciler to settle from its termination log (`pod_lost_terminal_pod_defer`). |
| Warm worker died holding attempts (warm pools only) | Warm-worker-lost reaper | next maintenance cycle | Each attempt bound to the dead worker is failed `pod_lost`; refill of the pool is the warm-pool reconciler's job, not the reaper's. |
| Run stuck `running` with no active TIs (post-crash limbo) | Orphan-run reaper ([#120](https://github.com/neochaotic/leoflow/issues/120)) | **5 min** | Run failed with `orphaned`; any remaining active TIs flipped to `failed` and every pod of the run is deleted. |

Every SLA above is a floor: the reapers run every **30 s**, so detection lands
up to one cycle after the threshold elapses. Worst case end-to-end: a mid-tick
scheduler crash that leaves TIs queued is fully reaped within
**`max(3 min, 5 min) + 30 s`** — the dispatch-lost reaper runs first, then the
orphan-run reaper picks up the now-no-active-TI run on a later cycle.

### Who enforces `execution_timeout` — and why the pod outlives it

A task that declares `execution_timeout_seconds` has **two** clocks that could
kill it, and only one of them can explain itself. The **agent** owns the
semantic timeout: it interrupts the user process at the declared boundary and
reports `execution_timeout: task exceeded Ns limit`. The task pod's
`activeDeadlineSeconds` is only the **backstop** for an agent that can no
longer enforce anything (crashed, wedged, partitioned); when the kubelet gets
there first, the pod is gone and the failure reason degrades to what Kubernetes
saw from outside, which cannot name a timeout.

The two clocks do not start together. The kubelet counts from the pod's
`status.startTime`, stamped **before** the image pull; the agent starts its own
only after its `RUNNING` pre-flight report, the source staging and the venv
resolution. So the pod deadline is deliberately set **longer** than the declared
timeout:

```text
activeDeadlineSeconds = execution_timeout_seconds
                      + 3 min   (startup headroom = the dispatch-lost threshold)
                      + the pod's terminationGracePeriodSeconds (default 30 s)
```

The headroom is the dispatch-lost threshold on purpose: that is already the
window in which the control plane treats a task as healthily starting up, so the
kubelet should not be spending the author's execution budget inside it. The
termination grace is added on top rather than folded in — it covers the
shutdown tail (stopping the child, delivering the report), not the startup head.

Two consequences worth knowing. A pod may outlive its declared timeout by a few
minutes when its agent is dead — that is the backstop doing its job, and the
reapers above still settle the task instance on their own schedule. And a
**pathological** startup (a cold node pulling a multi-gigabyte image, a
throttling registry, or a control-plane outage spanning the `RUNNING`
pre-flight) can still exceed any fixed headroom; the task then fails with the
kubelet's generic reason rather than the timeout diagnosis. If you see that,
the image pull is the thing to fix — and it is worth measuring
`status.startTime` → the agent's `task started` log line on a cold node.

The pod deadline of a task that declares **no** `execution_timeout` is a
different mechanism: a floor derived from `auth.max_attempt_credential_lifetime`
(see below), which takes no headroom because no clock inside the pod races it.

## Cadence: reconcile, then reap

The reapers do **not** run from the scheduler's 1 s tick. They run from the
leader's maintenance loop, which every 30 s performs **one ordered cycle**:

1. the **pod reconciler** sweeps the task pods, recovers each finished pod's
   durable outcome record (a success whose report was lost during an outage is
   recorded as a success — [ADR 0052](/project/adrs/0052-durable-task-outcome/)),
   and garbage-collects old finished pods;
2. **then** the five reapers run.

Each phase runs under its own budget of one interval (30 s): a sweep hung on a
slow apiserver cannot starve the reap, and a reap pass over a large namespace
cannot starve the sweep it depends on. Overrunning a budget is a load signal,
logged at `WARN` with the budget, not an error; a cycle can take up to two
intervals and the ticker coalesces the ticks it overruns.

The order is structural, not a matter of timers lining up. Before this, the
reapers ticked at 1 s and the reconciler at 30 s on an independent clock, so a
pod-lost verdict could land on a pod the reconciler would have recovered as
succeeded at its next sweep. The cost is up to 30 s of extra detection latency
on top of thresholds of 60 s–5 min; reaping is a backstop, never the primary
path, so that trade is the right one.

### The leader-settling gate

A control-plane restart manufactures the very signals the reapers act on:
every in-flight heartbeat looks stale (the receiver was down), a task pod that
finished during the outage looks lost (its terminal report found no server), a
run looks quiet. So after this instance acquires leadership **no reaper fires**
until the leader has **settled** — all three of:

- the **settling grace** (180 s, twice the agent-lost threshold) has elapsed
  since leadership, giving the whole fleet time to re-heartbeat;
- the **pod informer cache has synced**, so the fleet view is complete;
- **a reconciler sweep has completed under this leadership**, so every finished
  pod's true outcome has been recovered before anything is declared lost.

One gate, at the entry of the reaper pass, covers all five reapers (the
warm-worker-lost reaper too: delaying it by the grace only postpones recovering
a dead worker's attempts; pool refill is a separate loop and is not held).
Measured from leadership acquisition, so a re-election resets it. While it
holds, every cycle records `reap_settling_skip`.

**Liveness valve.** A gate that could hold forever would trade "reap wrong" for
"never reap". If the leader has not settled after **2 × grace (360 s)** — the
reconciler cannot list pods, the informer never syncs — the reapers proceed
anyway, with a `WARN` log and a `reap_settling_valve_open` decision on every
cycle the valve stays open. By then the reconciler has had at least four cycles,
so a valve that opens means the sweep really is broken; treat it as an alert.

**Why opening it is safe.** The usual reason a sweep never completes is an
apiserver that cannot be read — unreachable, unauthorized, throttled. The
reconciler's pod LIST and each reaper's own pod-presence LIST hit that same
apiserver, so that failure usually denies every pod-dependent
reaper its authorization too: pod-lost and dispatch-lost defer
(`pod_lost_pod_query_error`, `dispatch_lost_pod_query_error`) and the
warm-worker-lost reaper aborts its cycle with zero marks. They fail closed on
their own, valve or no valve. The two reads can still diverge, though — a broad
namespace-wide LIST can time out where a narrow, server-side-filtered one
succeeds, an informer that never syncs needs `watch` where the reapers need only
`list`, and API Priority and Fairness can starve the heavy request while the
cheap one gets through. And because the valve is *designed* to open, it is
never the only guard: the pod-lost reaper fails a task only when the attempt has
**no pod object at all**, a state no grace, cadence or election timing can
manufacture. A finished pod is a present pod, and stays the reconciler's.

The server validates the timing ladder these depend on at boot and refuses to
start if a constant was moved out of order:
`heartbeat (15 s) < agent-lost threshold (90 s) < settling grace (180 s) < attempt token TTL (10 min)`,
and `2 × maintenance interval (60 s) < settling grace`, so at least two whole
reconcile-then-reap cycles complete inside the grace.

## Tuning the thresholds

The thresholds and the settling grace are **build-time constants**
(`executor.DefaultReaperConfig`), not operator knobs: they sit on the ladder
above, and the boot-time validator exists precisely because moving one out of
order turns a restart into a false reap. The one operator-tunable rung is
`auth.max_attempt_credential_lifetime`, which must stay above the attempt token
TTL; boot fails naming the key if it does not. Setting it non-positive is
accepted but disables the renewal ceiling, the task-pod `activeDeadlineSeconds`
floor and the warm-pool attempt watchdog together, so boot logs a `WARN` naming
the key.

The defaults are conservative on purpose: too-tight thresholds risk reaping a
legitimately slow dispatch (Kubernetes pod-pull latency under contention) or a
busy agent.

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
- **Pod-lost reaper** — requires a `running` TI past its 60 s liveness floor
  AND a live read confirming no Pending/Running pod exists for exactly that
  attempt. A cached "pod present" defers without an API call; a cache miss is
  never trusted and falls through to the live read; a read error defers. And,
  like every reaper, it waits for the leader-settling gate — so a pod that
  finished during an outage is recovered by the reconciler, not failed.
- **Warm-worker-lost reaper** — requires a live LIST of the warm pods (not the
  cache) showing the bound worker gone; a LIST error aborts the pass with zero
  marks. It never deletes a pod: a warm worker outlives its attempts.
- **Orphan-run reaper** — requires `state = 'running'` AND no active TI on
  the run. A run with any TI in `scheduled`/`queued`/`running` is left alone
  (the dispatch-lost reaper unblocks this case by failing the stuck queued
  TIs first, so a later cycle sees no active TIs).

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
| `pod_lost` | TI failed by the pod-lost reaper (no pod at all for the attempt) |
| `pod_lost_terminal_pod_defer` | Pod-lost skipped because the attempt's pod is still there in a terminal phase — the reconciler settles it from its termination log; reaping would delete that evidence. Healthy as a *transient*. Sustained past two maintenance cycles means the reconciler is not settling and those task instances are stranded `running`, not about to settle: correlate with `reap_settling_valve_open` and `pod_lost_pod_query_error` |
| `warm_worker_lost` | TI failed by the warm-worker-lost reaper (its warm worker is gone) |
| `orphan_reaped` | Run failed by the orphan-run reaper |
| `reap_settling_skip` | The whole reaper pass was held because the leader has not settled yet (grace, informer sync, or a post-leadership reconciler sweep still pending) — expected for ~3 min after every (re-)election |
| `reap_settling_valve_open` | The leader never settled within 2 × grace and the reapers ran anyway; the reconciler sweep or the pod informer is broken — **alert on this** |
| `reap_gate_skip` | The pass was skipped because this instance is stepping down, no longer leads, or is shutting down — a healthy signal during rollouts |
| `agent_lost_list_error`, `dispatch_lost_list_error`, `orphan_list_error`, `pod_lost_list_error`, `warm_worker_lost_list_error` | Reaper's list query failed; the next cycle will retry |
| `dispatch_lost_pod_query_error`, `pod_lost_pod_query_error` | Pod liveness could not be read (K8s API error); the reaper deferred rather than risk a false positive |
| `agent_lost_pod_delete_error`, `dispatch_lost_pod_delete_error`, `orphan_pod_delete_error`, `pod_lost_pod_delete_error` | Pod teardown after a reap failed; the DB reap stands and the pod's `activeDeadlineSeconds`/GC are backstops |

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
