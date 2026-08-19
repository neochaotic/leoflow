-- RBAC vocabulary for the user-management surface: read and write on the "user"
-- resource, so listing and creating accounts is expressed in the same
-- permission grammar as every other resource rather than relying solely on the
-- admin-role short-circuit. Permissions are system-wide (UNIQUE (action,
-- resource)); the grants are added to every tenant's built-in admin role.
INSERT INTO permissions (action, resource, description) VALUES
    ('read',  'user', 'List control-plane user accounts'),
    ('write', 'user', 'Create and manage control-plane user accounts')
ON CONFLICT (action, resource) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r CROSS JOIN permissions p
WHERE r.name = 'admin'
  AND p.resource = 'user'
  AND p.action IN ('read', 'write')
ON CONFLICT DO NOTHING;
