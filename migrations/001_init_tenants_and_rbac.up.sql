-- 001_init_tenants_and_rbac.up.sql
-- Initial schema: tenants, users, roles, permissions.
-- All tables include tenant_id for future multi-tenancy.
-- In the MVP, a single 'default' tenant is auto-created.

BEGIN;


-- ─────────────────────────────────────────────────────────────────────────
-- Tenants
-- ─────────────────────────────────────────────────────────────────────────
CREATE TABLE tenants (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL UNIQUE,
    display_name TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Bootstrap the default tenant. All MVP operations belong to this tenant.
INSERT INTO tenants (name, display_name) VALUES ('default', 'Default Tenant');

-- ─────────────────────────────────────────────────────────────────────────
-- Users
-- ─────────────────────────────────────────────────────────────────────────
CREATE TABLE users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    email TEXT NOT NULL,
    password_hash TEXT,                       -- bcrypt, nullable for OIDC-only users
    oidc_subject TEXT,                        -- 'sub' claim from OIDC provider
    oidc_provider TEXT,                       -- 'google', 'azure', 'keycloak', etc.
    is_active BOOLEAN NOT NULL DEFAULT true,
    last_login_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT users_email_per_tenant UNIQUE (tenant_id, email),
    CONSTRAINT users_oidc_unique UNIQUE (oidc_provider, oidc_subject),
    CONSTRAINT users_has_auth CHECK (password_hash IS NOT NULL OR oidc_subject IS NOT NULL)
);

CREATE INDEX idx_users_tenant ON users(tenant_id);
CREATE INDEX idx_users_email ON users(email);

-- ─────────────────────────────────────────────────────────────────────────
-- Roles
-- ─────────────────────────────────────────────────────────────────────────
CREATE TABLE roles (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    description TEXT,
    is_system BOOLEAN NOT NULL DEFAULT false, -- built-in roles cannot be deleted
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT roles_name_per_tenant UNIQUE (tenant_id, name)
);

CREATE INDEX idx_roles_tenant ON roles(tenant_id);

-- ─────────────────────────────────────────────────────────────────────────
-- Permissions (system-wide, not per-tenant)
-- ─────────────────────────────────────────────────────────────────────────
CREATE TABLE permissions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    action TEXT NOT NULL,                     -- 'read', 'write', 'execute', 'admin'
    resource TEXT NOT NULL,                   -- 'dag', 'dag_run', 'task_instance', 'xcom', '*'
    description TEXT,
    CONSTRAINT permissions_unique UNIQUE (action, resource)
);

CREATE TABLE role_permissions (
    role_id UUID NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    permission_id UUID NOT NULL REFERENCES permissions(id) ON DELETE CASCADE,
    granted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (role_id, permission_id)
);

CREATE TABLE user_roles (
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role_id UUID NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    granted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, role_id)
);

-- ─────────────────────────────────────────────────────────────────────────
-- Bootstrap permissions and admin role
-- ─────────────────────────────────────────────────────────────────────────
INSERT INTO permissions (action, resource, description) VALUES
    ('admin',   '*',              'Full administrative access'),
    ('read',    'dag',            'Read DAG definitions'),
    ('write',   'dag',            'Create, update, delete DAGs'),
    ('execute', 'dag',            'Trigger DAG runs'),
    ('read',    'dag_run',        'Read DAG runs'),
    ('write',   'dag_run',        'Modify DAG runs (clear, mark)'),
    ('read',    'task_instance',  'Read task instances and logs'),
    ('write',   'task_instance',  'Clear or re-run task instances'),
    ('read',    'xcom',           'Read XCom values'),
    ('admin',   'tenant',         'Manage tenant settings');

INSERT INTO roles (tenant_id, name, description, is_system)
SELECT id, 'admin', 'Full administrative access within tenant', true FROM tenants WHERE name = 'default';

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r CROSS JOIN permissions p
WHERE r.name = 'admin' AND r.tenant_id = (SELECT id FROM tenants WHERE name = 'default');

COMMIT;
