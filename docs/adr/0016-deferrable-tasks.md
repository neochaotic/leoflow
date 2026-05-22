# ADR 0016: Deferrable Tasks (Deferred to v0.3)

**Status:** Deferred — Not implemented in v0.1.0
**Date:** 2026-05-22

## Context

Apache Airflow introduced "deferrable operators" to solve a specific
inefficiency: tasks that dispatch a job to an external system
(BigQuery, EMR, Spark cluster, etc.) and then poll for completion
over hours or days occupy a full worker slot the entire time,
even while doing nothing but `time.sleep` + status check. Airflow
addressed this by introducing a separate Triggerer process running
an asyncio event loop that handles the polling, allowing the worker
slot to be released during the wait.

The Leoflow architecture has an interesting property: because the
Control Plane is written in Go with native lightweight goroutines,
the same problem can be solved without introducing a separate
component. A goroutine pool inside the existing scheduler is
sufficient to manage thousands of concurrent triggers — no new
process, no new asyncio runtime, no new operational burden.

## Decision

Deferrable tasks are NOT implemented in the v0.1.0 MVP.

The MVP must first prove the core thesis (pod-per-task in Go
outperforms Airflow's Python control plane). Deferrable is an
optimization on top of that thesis, not a validation of it.
Adding it now expands the surface area to test, document, and
support before any users have validated the base offering.

The 'deferred' task state is pre-reserved in the database enum
(see migration 006_reserve_deferred_state.up.sql) so that adding
deferrable support in a future version does not require a breaking
schema change.

## Planned Design (For Future Reference)

When implemented (target v0.3 or later):

1. A new gRPC method DeferTask on AgentService allows a task pod
   to declare "I am done dispatching; here is what to poll and how
   to resume me."
2. The Control Plane scheduler runs a TriggerManager — a pool of
   goroutines polling external endpoints based on registered
   triggers.
3. Trigger state is persisted in a new triggers table; survives
   Control Plane restart (resilient by design).
4. When a trigger fires, the scheduler creates a new TaskInstance
   with try_number+1 pointing to the callback method declared by
   the task. The callback receives the trigger event payload as
   XCom input.
5. The developer surface uses a defer() helper in the Python SDK
   that wraps the gRPC call.

This design will be detailed in a full ADR when implementation
begins. The notes above are intent, not specification.

## Why This Is a Strong Story for Leoflow

The Airflow Triggerer is a workaround for Python's GIL — it had
to be a separate process running asyncio because the scheduler
itself could not handle concurrent I/O at scale. In Go, this
constraint does not exist. The Leoflow scheduler can host the
trigger goroutines directly, eliminating one full component
operators must run and monitor.

When deferrable is implemented, "deferrable tasks without a
separate Triggerer process" should become a marketing point.

## Revisit Trigger

Revisit this ADR when:

- MVP v0.1.0 has shipped and been validated by real users.
- At least three independent users have requested the dispatch+poll
  pattern in issues or discussions.
- v0.2 (pod-mode http_api) has been delivered as the prerequisite
  building block.

## Alternatives Rejected

- **Implement in v0.1:** rejected as scope creep. The MVP must
  prove the base thesis first.
- **Copy Airflow's Triggerer process model:** rejected because
  the architectural reason for a separate process (Python GIL)
  does not apply to Go. Adding a separate process would inherit
  Airflow's complexity without inheriting its constraints.
- **Never implement:** rejected because the dispatch+poll pattern
  is real and common in production data pipelines.
