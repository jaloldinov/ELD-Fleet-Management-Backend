-- Seed data for RBAC: every permission the code checks, the system roles, and
-- which role has which permission.
--
-- This lives in a migration (not a script) because the application cannot work
-- without it: every environment (dev, test, staging, prod) must have exactly
-- this data. New permissions later = a new seed migration.
--
-- Keep in sync with internal/model/permission.go (a test checks this).

INSERT INTO permissions (code, description) VALUES
    ('company:create', 'Create companies'),
    ('company:read',   'View companies'),
    ('company:update', 'Edit companies'),
    ('company:delete', 'Delete companies'),

    ('user:create',    'Create users'),
    ('user:read',      'View users'),
    ('user:update',    'Edit users and their roles'),
    ('user:delete',    'Delete users'),

    ('driver:create',  'Create drivers'),
    ('driver:read',    'View drivers'),
    ('driver:update',  'Edit drivers'),
    ('driver:delete',  'Delete drivers'),

    ('truck:create',   'Create trucks'),
    ('truck:read',     'View trucks'),
    ('truck:update',   'Edit trucks'),
    ('truck:delete',   'Delete trucks'),

    ('eld:create',     'Register ELD devices'),
    ('eld:read',       'View ELD devices'),
    ('eld:update',     'Edit ELD devices'),
    ('eld:delete',     'Delete ELD devices');

INSERT INTO roles (name, scope, description) VALUES
    ('platform_admin', 'platform', 'Platform staff: manages all companies'),
    ('admin',          'company',  'Company administrator: manages users and fleet'),
    ('manager',        'company',  'Fleet manager: manages drivers, trucks and ELDs'),
    ('dispatcher',     'company',  'Dispatcher: views fleet, assigns work'),
    ('driver',         'company',  'Driver: uses the driver app (permissions added in Phase 5)');

-- platform_admin gets every permission.
INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code
FROM roles r
CROSS JOIN permissions p
WHERE r.company_id IS NULL AND r.name = 'platform_admin';

-- Company roles: an explicit, reviewable matrix.
-- (Tenant scope, "only your own company", is enforced by the service layer.)
INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, m.permission_code
FROM (VALUES
    ('admin', 'company:read'), ('admin', 'company:update'),
    ('admin', 'user:create'),  ('admin', 'user:read'), ('admin', 'user:update'), ('admin', 'user:delete'),
    ('admin', 'driver:create'), ('admin', 'driver:read'), ('admin', 'driver:update'), ('admin', 'driver:delete'),
    ('admin', 'truck:create'),  ('admin', 'truck:read'),  ('admin', 'truck:update'),  ('admin', 'truck:delete'),
    ('admin', 'eld:create'),    ('admin', 'eld:read'),    ('admin', 'eld:update'),    ('admin', 'eld:delete'),

    ('manager', 'company:read'), ('manager', 'user:read'),
    ('manager', 'driver:create'), ('manager', 'driver:read'), ('manager', 'driver:update'), ('manager', 'driver:delete'),
    ('manager', 'truck:create'),  ('manager', 'truck:read'),  ('manager', 'truck:update'),  ('manager', 'truck:delete'),
    ('manager', 'eld:create'),    ('manager', 'eld:read'),    ('manager', 'eld:update'),    ('manager', 'eld:delete'),

    ('dispatcher', 'company:read'),
    ('dispatcher', 'driver:read'), ('dispatcher', 'truck:read'), ('dispatcher', 'eld:read')
) AS m (role_name, permission_code)
JOIN roles r ON r.company_id IS NULL AND r.name = m.role_name;
