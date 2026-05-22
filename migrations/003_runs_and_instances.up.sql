-- 003_runs_and_instances.up.sql
-- DAG runs and task instances. The hot tables of the system.

BEGIN;

-- ─────────────────────────────────────────────────────────────────────────
-- DAG runs
-- ─────────────────────────────────────────────────────────────────────────
CREATE TYPE dag_run_state AS ENUM (
    'queued',
    'running',
    'success',
    'failed'
);

CREATE TYPE dag_run_trigger AS ENUM (
    'scheduled',
    'manual',
    'backfill',
    'dataset'
);

CREATE TABLE dag_runs (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    dag_id UUID NOT NULL REFERENCES dags(id) ON DELETE CASCADE,
    dag_version_id UUID NOT NULL REFERENCES dag_versions(id),
    run_id TEXT NOT NULL,                     -- user-facing, e.g. 'scheduled__2026-05-21T00:00:00'
    logical_date TIMESTAMPTZ NOT NULL,        -- the "business date"
    data_interval_start TIMESTAMPTZ,
    data_interval_end TIMESTAMPTZ,
    state dag_run_state NOT NULL DEFAULT 'queued',
    trigger dag_run_trigger NOT NULL,
    conf JSONB DEFAULT '{}',                  -- user-supplied configuration
    triggered_by UUID REFERENCES users(id),
    queued_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at TIMESTAMPTZ,
    ended_at TIMESTAMPTZ,
    note TEXT,
    CONSTRAINT dag_runs_unique UNIQUE (dag_id, run_id)
);

CREATE INDEX idx_dag_runs_tenant ON dag_runs(tenant_id);
CREATE INDEX idx_dag_runs_dag_logical ON dag_runs(dag_id, logical_date DESC);
CREATE INDEX idx_dag_runs_state ON dag_runs(state, queued_at) WHERE state IN ('queued', 'running');

-- ─────────────────────────────────────────────────────────────────────────
-- Task instances
-- ─────────────────────────────────────────────────────────────────────────
CREATE TYPE task_state AS ENUM (
    'none',                                   -- not yet considered
    'scheduled',                              -- in the scheduling queue
    'queued',                                 -- pod/container being created
    'running',                                -- executing
    'success',
    'failed',
    'skipped',
    'upstream_failed',
    'up_for_retry'
);

CREATE TABLE task_instances (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    dag_run_id UUID NOT NULL REFERENCES dag_runs(id) ON DELETE CASCADE,
    task_id TEXT NOT NULL,                    -- as declared in dag.json
    map_index INT NOT NULL DEFAULT -1,        -- reserved for future dynamic mapping; -1 = not mapped
    try_number INT NOT NULL DEFAULT 0,
    max_tries INT NOT NULL DEFAULT 1,
    state task_state NOT NULL DEFAULT 'none',
    pool TEXT,                                -- present for Airflow compatibility; ignored in MVP
    operator TEXT NOT NULL,                   -- 'python', 'bash', 'http_api'
    queued_at TIMESTAMPTZ,
    started_at TIMESTAMPTZ,
    ended_at TIMESTAMPTZ,
    duration_seconds DOUBLE PRECISION,
    pod_name TEXT,                            -- K8s pod name, if executor=k8s
    node_name TEXT,                           -- K8s node where the pod ran
    exit_code INT,
    error_message TEXT,
    log_url TEXT,                             -- pointer to log location (S3, disk path, etc.)
    hostname TEXT,                            -- executor host (for standalone)
    CONSTRAINT task_instances_unique UNIQUE (dag_run_id, task_id, map_index, try_number)
);

CREATE INDEX idx_ti_tenant ON task_instances(tenant_id);
CREATE INDEX idx_ti_state ON task_instances(state) WHERE state IN ('scheduled', 'queued', 'running');
CREATE INDEX idx_ti_run ON task_instances(dag_run_id);
CREATE INDEX idx_ti_task ON task_instances(dag_run_id, task_id);

-- ─────────────────────────────────────────────────────────────────────────
-- State transition history (audit + observability)
-- ─────────────────────────────────────────────────────────────────────────
CREATE TABLE task_state_history (
    id BIGSERIAL PRIMARY KEY,
    task_instance_id UUID NOT NULL REFERENCES task_instances(id) ON DELETE CASCADE,
    from_state task_state,
    to_state task_state NOT NULL,
    transitioned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    reason TEXT,
    actor TEXT                                -- 'scheduler', 'agent', 'user:<uuid>'
);

CREATE INDEX idx_tsh_ti ON task_state_history(task_instance_id, transitioned_at DESC);

COMMIT;
