-- 015_ti_heartbeat.down.sql

BEGIN;

DROP INDEX IF EXISTS idx_ti_running_heartbeat;
ALTER TABLE task_instances DROP COLUMN IF EXISTS last_heartbeat_at;

COMMIT;
