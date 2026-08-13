ALTER TABLE task_instances
    DROP COLUMN IF EXISTS infra_attempts,
    DROP COLUMN IF EXISTS last_failure_kind;
