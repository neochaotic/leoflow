-- Seed the built-in role ladder: viewer < editor < operator (admin already
-- exists from 001 as the wildcard role). Each higher role includes the lower
-- one's grants plus a few writes, so the control plane can express least-
-- privilege access on the password path without waiting for OIDC. The roles
-- ship UNASSIGNED (no user_roles rows), so no existing account — including the
-- bootstrap admin — gains or loses anything until an operator grants a role.
--
-- Permissions are system-wide (UNIQUE (action, resource)); only the rows this
-- ladder needs and that 001/024 did not already create are inserted, all
-- guarded by ON CONFLICT DO NOTHING so re-running is a no-op.
INSERT INTO permissions (action, resource, description) VALUES
    ('read',  'pool',       'Read task pools'),
    ('write', 'pool',       'Create, update, delete task pools'),
    ('read',  'connection', 'Read connections'),
    ('write', 'connection', 'Create, update, delete connections'),
    ('read',  'variable',   'Read variables'),
    ('write', 'variable',   'Create, update, delete variables'),
    ('read',  'config',     'Read configuration'),
    -- The task-detail routes gate on the "task" resource (distinct from the
    -- "task_instance" name 001 seeded); seed it so a non-admin role can open a
    -- task without a 403. Unifying the two names is tracked as RBAC-vocabulary
    -- cleanup; here the ladder simply grants both.
    ('read',  'task',       'Read task detail')
ON CONFLICT (action, resource) DO NOTHING;

-- The three built-in roles, created for the default tenant (mirrors the admin
-- role seed in 001). is_system marks them non-deletable.
INSERT INTO roles (tenant_id, name, description, is_system)
SELECT t.id, 'viewer', 'Read-only access within tenant', true
FROM tenants t WHERE t.name = 'default'
ON CONFLICT (tenant_id, name) DO NOTHING;

INSERT INTO roles (tenant_id, name, description, is_system)
SELECT t.id, 'editor', 'Read access plus authoring of DAGs, variables, and connections', true
FROM tenants t WHERE t.name = 'default'
ON CONFLICT (tenant_id, name) DO NOTHING;

INSERT INTO roles (tenant_id, name, description, is_system)
SELECT t.id, 'operator', 'Editor access plus operating DAG runs, task instances, and pools', true
FROM tenants t WHERE t.name = 'default'
ON CONFLICT (tenant_id, name) DO NOTHING;

-- Grant the permission matrix. The (role, action, resource) triples enumerate
-- each role's full closure, so the SELECT joins them against the ladder roles
-- and the existing permission rows. A triple referencing a permission that does
-- not exist is silently skipped by the JOIN — the inserts above guarantee every
-- referenced permission is present first.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
JOIN tenants t ON t.id = r.tenant_id AND t.name = 'default'
JOIN (VALUES
    -- viewer: read across the read-only surface
    ('viewer',   'read',  'dag'),
    ('viewer',   'read',  'dag_run'),
    ('viewer',   'read',  'task_instance'),
    ('viewer',   'read',  'task'),
    ('viewer',   'read',  'xcom'),
    ('viewer',   'read',  'pool'),
    ('viewer',   'read',  'connection'),
    ('viewer',   'read',  'variable'),
    ('viewer',   'read',  'config'),
    -- editor: viewer's reads + authoring writes
    ('editor',   'read',  'dag'),
    ('editor',   'read',  'dag_run'),
    ('editor',   'read',  'task_instance'),
    ('editor',   'read',  'task'),
    ('editor',   'read',  'xcom'),
    ('editor',   'read',  'pool'),
    ('editor',   'read',  'connection'),
    ('editor',   'read',  'variable'),
    ('editor',   'read',  'config'),
    ('editor',   'write', 'dag'),
    ('editor',   'write', 'variable'),
    ('editor',   'write', 'connection'),
    -- operator: editor's grants + operational writes
    ('operator', 'read',  'dag'),
    ('operator', 'read',  'dag_run'),
    ('operator', 'read',  'task_instance'),
    ('operator', 'read',  'task'),
    ('operator', 'read',  'xcom'),
    ('operator', 'read',  'pool'),
    ('operator', 'read',  'connection'),
    ('operator', 'read',  'variable'),
    ('operator', 'read',  'config'),
    ('operator', 'write', 'dag'),
    ('operator', 'write', 'variable'),
    ('operator', 'write', 'connection'),
    ('operator', 'write', 'dag_run'),
    ('operator', 'write', 'task_instance'),
    ('operator', 'write', 'pool'),
    -- operator operates DAG runs, so it can trigger them (execute:dag exists from 001).
    ('operator', 'execute', 'dag')
) AS ladder(role_name, action, resource) ON ladder.role_name = r.name
JOIN permissions p ON p.action = ladder.action AND p.resource = ladder.resource
ON CONFLICT DO NOTHING;
