DROP TABLE IF EXISTS stripe_events;
ALTER TABLE companies DROP CONSTRAINT IF EXISTS companies_billing_status;
ALTER TABLE companies DROP COLUMN IF EXISTS billing_status;
