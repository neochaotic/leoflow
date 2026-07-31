ALTER TABLE task_instances
    DROP COLUMN IF EXISTS next_dispatch_at,
    DROP COLUMN IF EXISTS dispatch_attempts;
