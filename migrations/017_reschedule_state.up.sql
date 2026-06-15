-- 017_reschedule_state.up.sql
-- Reschedule-mode sensor support (ADR 0040 Phase B, #380). A sensor that pokes
-- not-ready releases its pod and is re-dispatched later, instead of holding the
-- pod (poke mode) or failing. Two pieces:
--   1. the 'up_for_reschedule' task state — non-terminal: the task is waiting to
--      be re-dispatched (modeled on up_for_retry); and
--   2. reschedule_at — the earliest time the scheduler may re-dispatch the task
--      instance (the sensor's requested poke time).
-- The enum value is appended after up_for_retry; enum order is cosmetic and both
-- are non-terminal. We do not USE the new value in this transaction, so combining
-- the ADD VALUE with the column add is safe.

BEGIN;

ALTER TYPE task_state ADD VALUE IF NOT EXISTS 'up_for_reschedule';

ALTER TABLE task_instances ADD COLUMN IF NOT EXISTS reschedule_at timestamptz;

COMMIT;
