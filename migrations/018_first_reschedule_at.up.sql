-- 018_first_reschedule_at.up.sql
-- Reschedule-mode sensor cumulative timeout (ADR 0040 Phase B, #380 follow-up).
-- first_reschedule_at records WHEN a task instance first entered reschedule, set
-- once and preserved across re-dispatches. The control plane delivers it to each
-- re-dispatched pod so the sensor's get_first_reschedule_date returns the real
-- first time — making BaseSensorOperator measure run_duration cumulatively and
-- honor `timeout` across pokes (otherwise a never-ready reschedule sensor would
-- poll forever, since started_at resets to now() every pod). Distinct from
-- reschedule_at, which is the NEXT poke time and is overwritten each reschedule.

BEGIN;

ALTER TABLE task_instances ADD COLUMN IF NOT EXISTS first_reschedule_at timestamptz;

COMMIT;
