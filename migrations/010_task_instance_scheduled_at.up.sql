-- Records when a task instance first entered the 'scheduled' state, exposed as
-- Airflow's scheduled_when. (queued_at / queued_when already exists.)
ALTER TABLE task_instances ADD COLUMN scheduled_at TIMESTAMPTZ;
