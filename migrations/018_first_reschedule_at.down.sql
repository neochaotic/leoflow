-- 018_first_reschedule_at.down.sql
-- Drop the first_reschedule_at column (additive, unreferenced by other objects).

BEGIN;

ALTER TABLE task_instances DROP COLUMN IF EXISTS first_reschedule_at;

COMMIT;
