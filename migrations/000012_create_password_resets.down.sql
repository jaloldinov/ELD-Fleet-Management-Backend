DELETE FROM rate_limits WHERE name = 'password_reset_requests';
DROP TABLE IF EXISTS password_resets;
