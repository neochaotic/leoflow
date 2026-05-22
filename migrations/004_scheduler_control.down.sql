-- 004_scheduler_control.down.sql
-- Reverse scheduler control and replica bookkeeping.

BEGIN;

DROP FUNCTION IF EXISTS trim_scheduler_loops();
DROP TABLE IF EXISTS scheduler_loops;
DROP TABLE IF EXISTS replicas;

COMMIT;
