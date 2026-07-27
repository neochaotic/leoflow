-- 019_dag_run_alerted_at.up.sql
-- On-failure alert dedup per failure episode (#431). alerted_at is claimed with an
-- atomic compare-and-set (set once, only when NULL) the first time a failed run
-- dispatches its on_failure alerts, so a re-tick of the same failed episode does
-- not re-page. A clear resets it to NULL (see ResetDagRunToVersion), so a genuine
-- re-failure after an operator clear re-claims and re-alerts — "once per episode".

BEGIN;

ALTER TABLE dag_runs ADD COLUMN IF NOT EXISTS alerted_at timestamptz;

COMMIT;
