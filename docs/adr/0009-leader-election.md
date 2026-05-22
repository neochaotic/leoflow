# ADR 0009: Leader Election via Postgres Advisory Locks

**Status:** Accepted
**Date:** 2026-05-21

## Context

The Leoflow Control Plane is the brain of the system. If it crashes, scheduling stops. If it runs in HA mode (multiple replicas), they will compete to schedule the same tasks and cause duplicate executions, which is catastrophic.

Two functions must be guarded:

1. **The Scheduler loop.** Only one replica should be making scheduling decisions at a time.
2. **Background workers** (log shippers, cleanup jobs). Some can run on all replicas, some must be singleton.

## Decision

Leoflow uses **Postgres advisory locks** for leader election. No new infrastructure (etcd, ZooKeeper, Consul) is required.

The mechanism:

1. Each replica, on startup, attempts `SELECT pg_try_advisory_lock(<scheduler_lock_id>)`.
2. The replica that wins becomes the **leader**. It runs the scheduler loop.
3. Followers run the API server (stateless) and idle the scheduler.
4. The leader heartbeats by holding the lock for the duration of its life.
5. If the leader dies, the lock is released by Postgres at session end. A follower wins the next attempt.

Followers poll every 5 seconds to detect leader death.

## Why Postgres Advisory Locks

- **Already a dependency.** Postgres is required for metadata anyway. No new component.
- **Atomic and reliable.** Postgres handles the locking semantics. Tested by decades of production use.
- **Cheap.** A single `pg_try_advisory_lock` call is microseconds.
- **Automatic release.** No need for TTL management; lock dies with the connection.

## What Runs Where

| Component | Leader only | All replicas |
|---|---|---|
| HTTP API server (`/api/v2/...`) | | ✅ |
| Scheduler loop | ✅ | |
| Executor (creates pods) | ✅ | |
| Metrics endpoint (`/metrics`) | | ✅ |
| Health checks | | ✅ |
| Log shipper (background) | ✅ | |

This means the API is **horizontally scalable** from day one, while the scheduler is single-leader.

## High Availability Behavior

```
Time   Replica A          Replica B
─────  ─────────────────  ─────────────────
T0     Starts, takes      Starts, fails to
       lock. Leader.      take lock. Follower.
T1     Running scheduler  Idle scheduler,
                          serving API
T2     Crashes            Polls every 5s
T3                        Detects lock free,
                          takes lock. Leader.
T4                        Resumes scheduling
```

Worst case: 5 seconds of scheduling pause during a failover. Acceptable for the MVP.

## Standalone Mode

In standalone mode (single-machine, no replicas), the lock is taken once at startup and held forever. Same code path, same Postgres call. No special branch.

## Consequences

- The Postgres connection holding the lock must remain healthy. The leader runs a background "keepalive" query every 30 seconds.
- Network partitions between the leader and Postgres force a re-election. This is correct behavior.
- The scheduler must be **idempotent in its decisions** so that a brief overlap during failover does not cause duplicate task creation. The K8s executor already enforces this via `metadata.name` uniqueness on pods.
- Future feature: in v1.x, the followers run "warm" schedulers that continuously read state, allowing failover in under 1 second.

## Alternatives Rejected

- **Redis-based locking (Redlock):** Redis is already used for XCom. Tempting to reuse, but the Redlock algorithm has well-documented edge cases under network partition. Postgres advisory locks are simpler and safer.
- **etcd / Consul:** rejected as a new infrastructure dependency.
- **Active-active scheduler with task partitioning:** rejected as too complex for the MVP. Considered for v2.
