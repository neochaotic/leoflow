-- Infra-fault re-placement (ADR 0051 Phase 1). An asynchronously-detected
-- infrastructure fault — the agent, the pod, or the dispatch was lost while the
-- task was running or queued — is not the task's fault, so it must NOT consume
-- the user's retry budget (try_number). last_failure_kind='infra' marks such a
-- terminal 'failed' as infra-caused so the scheduler re-places the task from
-- 'failed' back to 'none', and infra_attempts bounds those re-placements
-- (mirroring dispatch_attempts from migration 021 for the synchronous-dispatch
-- case). infra_attempts is separate from try_number on purpose: infrastructure
-- faults and task failures are different failure modes with different budgets.
ALTER TABLE task_instances
    ADD COLUMN IF NOT EXISTS last_failure_kind TEXT,
    ADD COLUMN IF NOT EXISTS infra_attempts INT NOT NULL DEFAULT 0;
