-- 004_scheduler_control.up.sql
-- Tables for scheduler leader tracking and replica heartbeats.
-- Note: the actual leader election uses pg_try_advisory_lock(),
-- not these tables. These exist purely for observability and UI display.

BEGIN;

-- ─────────────────────────────────────────────────────────────────────────
-- Replica registry
-- ─────────────────────────────────────────────────────────────────────────
CREATE TABLE replicas (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    hostname TEXT NOT NULL,
    process_id INT NOT NULL,
    version TEXT NOT NULL,                    -- build version of the binary
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    is_leader BOOLEAN NOT NULL DEFAULT false,
    role TEXT NOT NULL,                       -- 'control_plane', 'standalone'
    metadata JSONB DEFAULT '{}'
);

CREATE INDEX idx_replicas_heartbeat ON replicas(last_heartbeat_at DESC);
CREATE INDEX idx_replicas_leader ON replicas(is_leader) WHERE is_leader = true;

-- ─────────────────────────────────────────────────────────────────────────
-- Scheduler bookkeeping
-- ─────────────────────────────────────────────────────────────────────────
CREATE TABLE scheduler_loops (
    id BIGSERIAL PRIMARY KEY,
    replica_id UUID NOT NULL REFERENCES replicas(id) ON DELETE CASCADE,
    started_at TIMESTAMPTZ NOT NULL,
    ended_at TIMESTAMPTZ NOT NULL,
    duration_ms INT NOT NULL,
    dags_examined INT NOT NULL DEFAULT 0,
    runs_created INT NOT NULL DEFAULT 0,
    tasks_scheduled INT NOT NULL DEFAULT 0,
    errors INT NOT NULL DEFAULT 0
);

CREATE INDEX idx_scheduler_loops_replica ON scheduler_loops(replica_id, started_at DESC);

-- Keep only the last 24h of loop history to avoid bloat
CREATE OR REPLACE FUNCTION trim_scheduler_loops() RETURNS void AS $$
BEGIN
    DELETE FROM scheduler_loops WHERE started_at < now() - INTERVAL '24 hours';
END;
$$ LANGUAGE plpgsql;

COMMIT;
