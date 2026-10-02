-- Reverses 000007_create_drivers.up.sql.
DROP TABLE IF EXISTS drivers;
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_id_company_uniq;
