-- 019_dag_run_alerted_at.down.sql
-- Drop the alerted_at column (additive, unreferenced by other objects).

BEGIN;

ALTER TABLE dag_runs DROP COLUMN IF EXISTS alerted_at;

COMMIT;
