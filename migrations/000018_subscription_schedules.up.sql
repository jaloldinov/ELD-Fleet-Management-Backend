-- A plan change scheduled for the next renewal ("switch to yearly from the
-- next period"), as a Stripe subscription schedule (sub_sched_...): phase 1
-- is the current plan until the period ends, phase 2 the new plan.

ALTER TABLE subscriptions ADD COLUMN stripe_schedule_id TEXT NULL;
ALTER TABLE subscriptions ADD CONSTRAINT subscriptions_schedule_id_format
    CHECK (stripe_schedule_id IS NULL OR stripe_schedule_id ~ '^sub_sched_[A-Za-z0-9]+$');
-- When the scheduled plan starts; NULL = nothing scheduled.
ALTER TABLE subscriptions ADD COLUMN scheduled_change_at TIMESTAMPTZ NULL;

-- The plan that starts at scheduled_change_at. Replaced as a whole at
-- every sync, like subscription_items.
CREATE TABLE subscription_scheduled_items (
    id               UUID        PRIMARY KEY DEFAULT uuidv7(),
    subscription_id  UUID        NOT NULL REFERENCES subscriptions (id) ON DELETE CASCADE,
    price_id         UUID        NOT NULL REFERENCES prices (id),
    quantity         INTEGER     NOT NULL,
    CONSTRAINT subscription_scheduled_items_quantity CHECK (quantity >= 0)
);

CREATE INDEX subscription_scheduled_items_subscription_idx ON subscription_scheduled_items (subscription_id);
