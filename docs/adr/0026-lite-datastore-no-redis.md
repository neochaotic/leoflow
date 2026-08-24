# ADR 0026: Lite Datastore — XCom on Postgres, No Redis

**Status:** Accepted
**Date:** 2026-05-27
**Deciders:** Project founder
**Refines:** ADR 0006 (XCom Backend in Redis)

## Context

ADR 0006 established Redis as the XCom backend: small (≤256 KB) typed payloads
with a TTL, expired natively by Redis. In the control plane, Redis ends up
serving exactly two things:

1. **XCom** — the `xcom.Backend` (`RedisBackend`).
2. **Live log tailing** — the `logs.Tailer` (`RedisTailer`) fans task log lines
   over Redis pub/sub so the UI streams them without polling.

Notably, the one *correctness-critical* primitive — scheduler leadership — does
**not** use Redis: it uses Postgres advisory locks (`pg_advisory_lock`), which
auto-release on session loss and avoid the well-known distributed-lock pitfalls
of Redis (Redlock, fencing tokens). Redis was only ever a throughput/transport
optimization, never a consensus store.

The two editions deploy very differently (see ADR 0027 for the full editions
contract — executors and delivery):

- **Pro** — the main product — ships **only** as Kubernetes + the Helm chart,
  runs **only** the Kubernetes executor, and is expected to point at **external
  managed Postgres and external managed Redis**. Its connection config is always
  templated by the chart; there is no hand-editing operator to misconfigure.
- **Lite** is the local `leoflow lite` binary, a single process. Requiring Docker
  just to run Redis makes Lite fragile (e.g. it broke on a macOS Docker without
  the Compose v2 plugin) and contradicts the goal of a self-contained, no-Docker
  embedded runtime. Lite may also run *light* production workloads.

## Decision

Make the Redis-backed layer pluggable per edition, keeping Redis for Pro.

1. **XCom backend is selectable.** Pro keeps `RedisBackend` (ADR 0006 stands
   unchanged for production). Lite uses a new `PostgresBackend`: an `xcom_store`
   table keyed by the XCom key, with a `value`, metadata, and an `expires_at`
   TTL. Reads filter on `expires_at`; a periodic `DeleteExpired` sweep bounds the
   table (replacing Redis's native key expiry).

2. **Live-log tailer is selectable.** Pro keeps `RedisTailer`. Lite uses a new
   in-process `MemoryTailer` (Go channels) — valid because Lite is a single
   process (server + scheduler + agent gRPC together).

3. **The switch signal is the presence of a Redis URL.** Empty `LEOFLOW_REDIS_URL`
   ⇒ embedded backends (Lite); a configured URL ⇒ the production Redis backends.
   No new config knob. This is safe precisely because Pro is always Helm-
   configured with an external Redis: there is no manual production config to
   "forget." The "Redis is required for production" guarantee lives at the **Helm
   chart values-validation layer**, not as a Go runtime fail-loud check.

4. **Scheduler leadership is unchanged** — Postgres advisory locks, both editions.

Lite therefore needs **no Redis at all**, and with the managed relocatable
Postgres (ADR/Fase 2) it runs **fully without Docker**.

## Consequences

- **Lite is self-contained:** managed Postgres only, no Redis, no Docker. The
  macOS/Compose fragility disappears.
- **Postgres XCom is durable** (survives restart), unlike in-memory Redis — a
  plus for light production on Lite. The trade-off is throughput under high
  XCom fan-out, which is exactly the case the Pro/Redis profile is for. The
  graduation point ("move to the Pro profile when XCom becomes a bottleneck")
  is documented for operators.
- **`MemoryTailer` is single-process only.** It must never back a multi-replica
  deployment. This is structurally guaranteed: multi-replica = Pro = Redis
  configured = `RedisTailer`.
- **ADR 0006 is untouched for production.** Redis remains the production XCom
  backend; this ADR adds an edition-scoped alternative, it does not replace it.
- A small `xcom_store` cleanup sweep is now part of the embedded server's
  lifecycle (alongside the existing XCom-index/log janitor).
- The Helm chart should default to external Postgres + Redis and validate both
  URLs are set (tracked separately), rather than bundling them in-cluster.
