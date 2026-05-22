-- 005_xcom_index.up.sql
-- Lightweight index of XCom keys for UI listing and auditing.
-- The actual XCom payloads live in Redis (see ADR 0006).
-- This table holds only metadata, never the value.

BEGIN;

CREATE TABLE xcom_index (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    dag_run_id UUID NOT NULL REFERENCES dag_runs(id) ON DELETE CASCADE,
    task_id TEXT NOT NULL,
    key TEXT NOT NULL,                        -- xcom key name, default 'return_value'
    redis_key TEXT NOT NULL,                  -- the full key in Redis
    size_bytes INT NOT NULL,
    content_type TEXT NOT NULL DEFAULT 'application/json',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT xcom_index_unique UNIQUE (dag_run_id, task_id, key)
);

CREATE INDEX idx_xcom_run ON xcom_index(dag_run_id);
CREATE INDEX idx_xcom_expires ON xcom_index(expires_at);

-- ─────────────────────────────────────────────────────────────────────────
-- Audit log (for compliance and debugging)
-- ─────────────────────────────────────────────────────────────────────────
CREATE TABLE audit_log (
    id BIGSERIAL PRIMARY KEY,
    tenant_id UUID REFERENCES tenants(id) ON DELETE CASCADE,
    user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    action TEXT NOT NULL,                     -- 'dag.create', 'dag_run.trigger', 'auth.login', ...
    resource_type TEXT,                       -- 'dag', 'dag_run', 'user'
    resource_id TEXT,
    metadata JSONB DEFAULT '{}',
    ip_address INET,
    user_agent TEXT,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_audit_tenant ON audit_log(tenant_id, occurred_at DESC);
CREATE INDEX idx_audit_user ON audit_log(user_id, occurred_at DESC);
CREATE INDEX idx_audit_action ON audit_log(action, occurred_at DESC);

COMMIT;
