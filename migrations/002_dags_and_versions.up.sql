-- 002_dags_and_versions.up.sql
-- DAGs and their versioned snapshots. Each push of a `dag.json` creates a new dag_version row.

BEGIN;

-- ─────────────────────────────────────────────────────────────────────────
-- DAGs (the logical entity, identified by dag_id within a tenant)
-- ─────────────────────────────────────────────────────────────────────────
CREATE TABLE dags (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    dag_id TEXT NOT NULL,                     -- user-facing identifier
    description TEXT,
    is_paused BOOLEAN NOT NULL DEFAULT false,
    is_active BOOLEAN NOT NULL DEFAULT true,  -- soft-delete flag
    owner TEXT,
    tags TEXT[] DEFAULT '{}',
    schedule TEXT,                            -- cron expression or '@daily' etc; null = manual only
    schedule_timezone TEXT DEFAULT 'UTC',
    start_date TIMESTAMPTZ,
    end_date TIMESTAMPTZ,
    max_active_runs INT NOT NULL DEFAULT 16,
    catchup BOOLEAN NOT NULL DEFAULT false,
    current_version_id UUID,                  -- FK added after dag_versions creation
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT dags_unique_per_tenant UNIQUE (tenant_id, dag_id)
);

CREATE INDEX idx_dags_tenant ON dags(tenant_id);
CREATE INDEX idx_dags_paused ON dags(tenant_id, is_paused) WHERE is_active = true;

-- ─────────────────────────────────────────────────────────────────────────
-- DAG versions (immutable snapshots of dag.json)
-- ─────────────────────────────────────────────────────────────────────────
CREATE TABLE dag_versions (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    dag_id UUID NOT NULL REFERENCES dags(id) ON DELETE CASCADE,
    version TEXT NOT NULL,                    -- 'v1.2.3' or autogen 'auto-2026-05-21-abc123'
    image_reference TEXT NOT NULL,            -- 'myrepo/etl:v1.2.3'
    spec JSONB NOT NULL,                      -- full dag.json content
    spec_hash TEXT NOT NULL,                  -- sha256 of canonical spec for dedup
    created_by UUID REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT dag_versions_unique UNIQUE (dag_id, version)
);

CREATE INDEX idx_dag_versions_dag ON dag_versions(dag_id);
CREATE INDEX idx_dag_versions_hash ON dag_versions(spec_hash);
CREATE INDEX idx_dag_versions_spec ON dag_versions USING GIN (spec);

-- Now wire the FK from dags.current_version_id
ALTER TABLE dags
    ADD CONSTRAINT dags_current_version_fk
    FOREIGN KEY (current_version_id) REFERENCES dag_versions(id) ON DELETE SET NULL;

COMMIT;
