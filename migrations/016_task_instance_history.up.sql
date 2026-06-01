-- task_instance_history captures past attempts of a task instance. When the
-- same (run, task) is cleared and re-run, the current row's per-attempt
-- fields are snapshotted into this table BEFORE the reset, then try_number is
-- bumped on the live row. The UI's /tries endpoint unions the current row
-- with all history rows for the same task instance to render one navigable
-- tab per attempt. Without this, a 3-times-cleared task only shows the latest
-- attempt's logs in the UI (Lima bug found 2026-05-31).
CREATE TABLE task_instance_history (
    history_id BIGSERIAL PRIMARY KEY,
    task_instance_id UUID NOT NULL REFERENCES task_instances(id) ON DELETE CASCADE,
    try_number INT NOT NULL,
    state task_state NOT NULL,
    queued_at TIMESTAMPTZ,
    scheduled_at TIMESTAMPTZ,
    started_at TIMESTAMPTZ,
    ended_at TIMESTAMPTZ,
    duration_seconds DOUBLE PRECISION,
    exit_code INT,
    error_message TEXT,
    hostname TEXT,
    pod_name TEXT,
    node_name TEXT,
    note TEXT,
    archived_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT task_instance_history_unique UNIQUE (task_instance_id, try_number)
);

CREATE INDEX idx_ti_history_ti ON task_instance_history(task_instance_id);
