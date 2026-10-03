DELETE FROM role_permissions WHERE permission_code IN ('ratelimit:read', 'ratelimit:update');
DELETE FROM permissions WHERE code IN ('ratelimit:read', 'ratelimit:update');
DROP TABLE IF EXISTS rate_limits;
