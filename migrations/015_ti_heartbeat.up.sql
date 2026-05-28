-- 015_ti_heartbeat.up.sql
-- Per-task-instance liveness signal for the agent heartbeat reaper (#128).
-- The scheduler's run-level reaper (#120) intentionally leaves any run with an
-- active TI alone to avoid killing legitimately-slow tasks; this column closes
-- the companion gap "the agent went silent mid-task".

BEGIN;

ALTER TABLE task_instances
    ADD COLUMN last_heartbeat_at TIMESTAMPTZ;

-- Partial index: the reaper only ever scans TIs in `running` whose heartbeat
-- is stale, so the index is narrow and write traffic on settled TIs is
-- unaffected.
CREATE INDEX idx_ti_running_heartbeat ON task_instances(last_heartbeat_at)
    WHERE state = 'running';

COMMIT;
