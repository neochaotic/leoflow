-- 017_reschedule_state.down.sql
-- Drop the reschedule_at column. The 'up_for_reschedule' enum value is left in
-- place: PostgreSQL cannot remove an enum value without rebuilding the type and
-- rewriting every column that uses it. As with 006_reserve_deferred_state,
-- re-applying the up migration is idempotent (ADD VALUE IF NOT EXISTS), so
-- leaving the value is safe across down/up cycles.

BEGIN;

ALTER TABLE task_instances DROP COLUMN IF EXISTS reschedule_at;

COMMIT;
