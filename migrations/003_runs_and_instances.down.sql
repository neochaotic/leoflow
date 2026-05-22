-- 003_runs_and_instances.down.sql
-- Reverse DAG runs, task instances, and state history.

BEGIN;

DROP TABLE IF EXISTS task_state_history;
DROP TABLE IF EXISTS task_instances;
DROP TABLE IF EXISTS dag_runs;

DROP TYPE IF EXISTS task_state;
DROP TYPE IF EXISTS dag_run_trigger;
DROP TYPE IF EXISTS dag_run_state;

COMMIT;
