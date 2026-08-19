DELETE FROM role_permissions
WHERE permission_id IN (
    SELECT id FROM permissions WHERE resource = 'user' AND action IN ('read', 'write')
);

DELETE FROM permissions WHERE resource = 'user' AND action IN ('read', 'write');
