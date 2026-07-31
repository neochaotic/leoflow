-- Dispatch-failure backoff (ADR 0031 Amendment A). A synchronous dispatch
-- failure (kube-apiserver unreachable, RBAC denied, quota, admission reject) is
-- no longer retried every tick: dispatch_attempts counts consecutive failures
-- and next_dispatch_at is the earliest time the scheduler may re-attempt, exactly
-- mirroring reschedule_at (migration 017). dispatch_attempts is separate from
-- try_number on purpose — a dispatch failure is infrastructure, not a task
-- failure, and must not consume the user's retry budget.
ALTER TABLE task_instances
    ADD COLUMN IF NOT EXISTS dispatch_attempts INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS next_dispatch_at TIMESTAMPTZ;
