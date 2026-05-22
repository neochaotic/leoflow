-- 006_reserve_deferred_state.up.sql
-- Reserve the 'deferred' task state for the future deferrable tasks feature
-- (see ADR 0016). No code in v0.1 transitions a task to 'deferred'; the value is
-- added now so that shipping deferrable support later requires no breaking
-- schema change. It is ordered before 'success' to keep the terminal states
-- grouped at the end of the enum.

BEGIN;

ALTER TYPE task_state ADD VALUE IF NOT EXISTS 'deferred' BEFORE 'success';

COMMIT;
