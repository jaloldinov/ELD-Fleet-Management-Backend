-- Reverses 000005_seed_rbac.up.sql.
-- Deleting roles cascades to role_permissions AND user_roles, so rolling back
-- this migration removes role assignments from users. That is expected: the
-- roles they pointed to no longer exist.
DELETE FROM roles
WHERE company_id IS NULL
  AND name IN ('platform_admin', 'admin', 'manager', 'dispatcher', 'driver');

DELETE FROM permissions
WHERE code IN (
    'company:create', 'company:read', 'company:update', 'company:delete',
    'user:create', 'user:read', 'user:update', 'user:delete',
    'driver:create', 'driver:read', 'driver:update', 'driver:delete',
    'truck:create', 'truck:read', 'truck:update', 'truck:delete',
    'eld:create', 'eld:read', 'eld:update', 'eld:delete'
);
