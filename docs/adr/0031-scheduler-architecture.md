# ADR 0031: Scheduler Architecture — Reconciliation Loop, Two-Phase Dispatch, Two-Layer Reaping

**Status:** Proposed
**Date:** 2026-05-28

## Context

The Leoflow control plane's scheduler is the single most critical service in
the system. Two properties are non-negotiable:

1. **It must never die.** A panic, slow query, poison run, or dispatcher
   crash cannot kill the scheduling goroutine. If the scheduler dies, the
   only way state advances is manual intervention — unacceptable.
2. **It must trigger close to schedule.** A scheduler that is technically
   "up" but ticking every 30s instead of every 1s, or hanging on a slow K8s
   API call, has the same operational effect as being down: tasks fire late,
   SLAs miss, the user notices.

The MVP shipped with a working scheduler (reconciliation loop, leader-elected
via Postgres advisory locks — ADR 0009), but live testing of pre-alpha .17
surfaced two real-world failure shapes the original design did not cover:

- A `dag_run` left in `state='running'` after a server crash mid-finalization.
  All its task instances are terminal, but the run-level finalizer never
  ran, so the dashboard counter keeps showing "1 em execução" with nothing
  actually running (#120).
- A `task_instance` left in `state='running'` because its agent died (pod
  evicted, network partition, agent crash). The TI is "active" in the DB but
  nothing in the world is updating it; the run never finalizes (#128).

Adjacent to these, a structural risk surfaced on review of PR #126:

- Task dispatch is **synchronous** inside the scheduler tick
  (`scheduler.launchQueued` → `dispatcher.Dispatch` → `executor.Execute`).
  For K8s, this is a `kube-apiserver` pod-create call (typically 100–500 ms,
  occasionally seconds). With N tasks transitioning to `queued` in the same
  tick, the tick takes N × dispatch latency — a 50-task spike behind a slow
  kube-apiserver stretches the tick from 1 s to 25 s, silently degrading
  scheduling accuracy (#127).

This ADR sets the canonical scheduler architecture for both editions.

## Decision

Leoflow's scheduler is a **leader-elected reconciliation loop** with
**two-phase work** (planning is synchronous in the tick; dispatch is
asynchronous via a bounded worker pool) and a **two-layer safety net**
(run-level reaper + task-instance heartbeat reaper) — all behind a single
DB-as-truth model. The architecture is the same across editions; the only
difference is the dispatcher's pool size (Lite=1/passthrough, Pro=N).

```
                                ┌──────────────────────────────────────────┐
                                │             scheduler.Step               │
   ┌────────────┐               │                                          │
   │ tick (1s)  │──────────────►│  Phase 1: Plan (sync)                    │
   └────────────┘               │    ├ ActiveRuns                          │
                                │    ├ advanceSafely / per-run isolation   │
                                │    ├ FinalizeRun                         │
                                │    └ createDueRuns                       │
                                │                                          │
                                │  Phase 2: Reap (sync, leader-only)       │
                                │    ├ ListReapCandidates (LIMIT 100)      │
                                │    ├ IsOrphaned filter                   │
                                │    └ ReapRun (transactional)             │
                                │                                          │
                                │  Phase 3: Heartbeat reap (sync, leader)  │
                                │    └ TI agent_lost detection (#128)      │
                                └──────────┬───────────────────────────────┘
                                           │
                                  enqueue ▼
                                ┌──────────────────────────────────────────┐
                                │  Dispatch worker pool (async, leader)    │
                                │    ├ bounded channel (backpressure)      │
                                │    ├ N goroutines                        │
                                │    └ Dispatcher.Dispatch → executor      │
                                └──────────────────────────────────────────┘
                                           │
                                  writes   ▼
                                ┌──────────────────────────────────────────┐
                                │  Postgres (single source of truth)       │
                                │    └ dag_runs / task_instances           │
                                └──────────────────────────────────────────┘
```

### Why these three patterns together

**Reconciliation loop** (Kubernetes / Argo / Tekton family). The scheduler
holds no critical state in memory: every tick re-derives "what should
happen next" from the DB. Crash recovery is "next tick fires" — there is
no log to replay, no in-flight workflow to resume. Every decision is
idempotent so a brief leader-overlap during failover (#9) does not cause
duplicate state changes.

**Two-phase work**. The tick does *planning* (cheap: SQL reads + Go state
machine). Dispatch (expensive: a remote API call) is handed off to a
bounded pool that drains independently. The tick rate is decoupled from
the executor's latency — a 200 ms kube-apiserver call no longer stretches
the tick. Backpressure is explicit: when the pool's queue is full, the
TI stays in `scheduled` and the next tick re-attempts. This matches
Airflow ≥2.6's split between the scheduler job and the executor's `sync()`.

**Two-layer reaping**. The run-level reaper (#120, PR #126) targets runs
whose finalizer missed — i.e. **every TI is settled** but the dag_run is
still `running`. The TI-level reaper (#128) targets task instances whose
agent went silent — **a single TI is stuck `running`** while its agent's
heartbeat has gone stale. Each reaper has a precise "do no harm" guard
that prevents it from firing on legitimately-active state.

### The "do no harm" rule

This is the most important design constraint and the rule that makes the
reapers safe to enable by default:

> A reaper may only transition state from `running` → `failed` when there
> is a positive, *observable* signal that nothing in the world is going
> to update it again.

Concretely:
- **Run reaper**: a positive signal exists when every TI is in a terminal
  or `none` state — the runtime has nothing left to report on this run.
  If even one TI is `scheduled`/`queued`/`running`, the agent may still
  deliver an update, so the run reaper refuses to touch the run.
- **TI heartbeat reaper**: a positive signal exists when the heartbeat
  has not arrived for > `threshold` (default 90 s, well above the gRPC
  keepalive interval). The TI is in `running` *and* the connection that
  was supposed to update it is provably gone.

Anything weaker than these signals would risk killing a legitimately-slow
task. We choose two narrower reapers over one broader, more aggressive
one for exactly this reason.

### Lite vs Pro

The architecture is the same; the parameters differ.

| Lever | Lite (subprocess) | Pro (k8s) | Why |
|---|---|---|---|
| Tick interval | 1 s | 1 s | Same. |
| Dispatch path | Pool size 1, queue size 1 (effectively sync) | Pool size 16, queue size 256 | Subprocess fork is µs; K8s API is ms-to-s. |
| Run-orphan threshold | 5 min | 5 min | Same. |
| TI heartbeat threshold | 90 s | 90 s | Same. |
| Leader election | Postgres advisory lock | Postgres advisory lock | ADR 0009. |
| Heartbeat carrier | Subprocess exit observable via `os.Wait` (synthesise heartbeat when child is alive) | gRPC stream keepalive | Match the runtime. |

Crucially: the **state machine**, the **SQL**, the **safety rules**, and
the **dashboard counter** are all identical. A test asserting the state
contract is valid for both editions.

### Leader-overlap correctness

The leadership watchdog (5 s polls, ADR 0009) bounds the worst-case
overlap between an old leader losing its lock and a new leader starting
to write. The scheduler is designed to be correct across this window:

- **Planning writes** (`ApplyTransition`, `SetRunState`) are guarded by
  enum transitions in the state machine: only legal transitions land. A
  duplicate transition (same `to`) is idempotent at the SQL level (the
  current row already has that value).
- **Reaper writes** (`MarkRunOrphanedRun`) are guarded by
  `WHERE state = 'running'`. A second writer that sees the run already
  failed updates zero rows and the tx aborts cleanly — no mixed states.
- **Dispatch enqueues** are not state changes themselves; the worker pool
  only writes via the same guarded `ApplyTransition` calls. A duplicate
  dispatch (rare, only possible during overlap) is collapsed by the
  executor's idempotency key (K8s `metadata.name` per attempt).

### Failure-mode coverage matrix

| Failure | Detected by | Recovery |
|---|---|---|
| Scheduler crash mid-tick | next tick (defer recover) | Resume from DB. |
| Scheduler crash mid-finalization | run reaper (#120) | Run flipped to `failed`. |
| Agent dies mid-task | TI heartbeat reaper (#128) | TI flipped to `failed`. |
| Slow K8s API | dispatch pool (async) | Tick stays fast; dispatch drains. |
| DB hiccup on `createDueRuns` | reaper still runs (P0-1 fix) | Orphans observable next tick. |
| Poison run (malformed spec) | `advanceSafely` recover | Other runs unaffected. |
| Poison reap candidate | per-candidate isolated catch | Other candidates unaffected. |
| Leader connection blip | advisory lock released → re-campaign | New leader resumes. |
| Leader-overlap window | WHERE-guarded writes + enum transitions | At-most-once effect. |

### What the scheduler will *not* do (scope guard)

- **No event-sourcing.** Workflows are reconciled, not replayed. This
  preserves Airflow API compatibility (the public contract is a `dag_run`
  row with a state, not a log of events).
- **No active-active scheduling.** Active-active partitioning is the
  natural v2 escalation (sharding by tenant or DAG bucket); the MVP
  stays single-leader. Until the system is genuinely saturated on a
  single leader, partitioning adds complexity without payoff.
- **No external coordinator** (etcd, ZooKeeper, Consul). Postgres
  advisory locks suffice for single-leader; if/when sharding is added,
  we will revisit (and write a new ADR, not modify this one).
- **No in-process queue persistence.** The dispatch queue is in-memory.
  A crash forfeits the in-flight enqueues; the next tick re-enqueues
  whatever is still `scheduled` in the DB. The DB remains the truth.

## Consequences

- **Implementation:** PR #126 lands the run reaper with all the guards
  above. Issues #127 (async dispatch) and #128 (TI heartbeat reaper)
  complete the architecture; both are scoped, scheduled in that order.
- **Observability:** new metrics — `orphan_reaped`, `orphan_reap_error`,
  `orphan_list_error`, `orphan_panic`, `dispatch_queue_depth`,
  `dispatch_at_capacity`, `dispatch_latency_seconds`, `agent_lost_tasks_total`.
  Each one tells the operator a different story.
- **Testing:** the contract tests (unit + integration) are the same for
  both editions. Edition-specific tests only verify the dispatcher pool's
  Lite-passthrough vs Pro-pool wiring.
- **Operational tuning:** the four exposed knobs (tick interval, pool
  size, run-orphan threshold, TI heartbeat threshold) are documented per
  edition. Default values are conservative; operators can tighten.
- **Future:** sharding (active-active by tenant) and richer dispatch
  scheduling (priority queues, per-DAG fairness) are explicit v2 levers,
  not changes to this ADR.

## Alternatives Rejected

- **Move to Temporal / Cadence event-sourcing.** Cleaner long-term
  semantics, but: (a) breaks Airflow API compat, (b) requires a
  full-rewrite, (c) adds new infra (Temporal cluster). Net negative for
  the MVP and the editions story.
- **Make dispatch sync but use a per-tick goroutine.** Tempting and
  smaller diff, but a goroutine per dispatch with no pool bound is how
  fork-bomb-shaped tickle bugs are born. The bounded pool is the same
  amount of code with a backpressure story.
- **Push reaping into a separate sidecar service.** Sidecar adds another
  binary to ship, deploy, and monitor. In-loop reaping reuses the
  existing leader-election + isolation + metric surface for zero
  operational cost.
- **Use a single broad reaper (any stuck `running` for > N min).** Higher
  recall, but the cost of one false-positive (killing a live 1-hour job)
  is too high. Two narrow reapers with strong positive signals win on
  user trust.

## References

- ADR 0009 — Leader election via Postgres advisory locks.
- Issue #120 — Scheduler: no reaper for orphaned dagRuns.
- PR #126 — Run reaper implementation.
- Issue #127 — Async task dispatch via bounded worker pool.
- Issue #128 — TI-level heartbeat reaper.
- Airflow 2.6 release notes — KubernetesExecutor `sync()` split.
- Kubernetes API conventions — reconciliation loop pattern.
