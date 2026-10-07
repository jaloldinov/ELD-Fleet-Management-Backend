DROP INDEX IF EXISTS companies_stripe_customer_id_uniq;
ALTER TABLE companies DROP CONSTRAINT IF EXISTS companies_stripe_customer_id_format;
ALTER TABLE companies DROP COLUMN IF EXISTS stripe_customer_id;
