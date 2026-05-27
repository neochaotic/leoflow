-- 001_init_tenants_and_rbac.down.sql
-- Reverse the initial RBAC schema.

BEGIN;

DROP TABLE IF EXISTS user_roles;
DROP TABLE IF EXISTS role_permissions;
DROP TABLE IF EXISTS permissions;
DROP TABLE IF EXISTS roles;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS tenants;


COMMIT;
