DROP TABLE IF EXISTS subscription_scheduled_items;
ALTER TABLE subscriptions DROP COLUMN IF EXISTS scheduled_change_at;
ALTER TABLE subscriptions DROP CONSTRAINT IF EXISTS subscriptions_schedule_id_format;
ALTER TABLE subscriptions DROP COLUMN IF EXISTS stripe_schedule_id;
