-- A free-text note on a task instance. Surfaces operational context in the UI's
-- task panel — e.g. why a task is queued but not running (no executor). Airflow
-- exposes a task-instance note field; the UI renders it.
ALTER TABLE task_instances ADD COLUMN note TEXT;
