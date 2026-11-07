DROP INDEX IF EXISTS drivers_dispatcher_idx;
ALTER TABLE drivers DROP CONSTRAINT IF EXISTS drivers_dispatcher_not_self;
ALTER TABLE drivers DROP CONSTRAINT IF EXISTS drivers_dispatcher_company_fkey;
ALTER TABLE drivers DROP COLUMN IF EXISTS dispatcher_id;
