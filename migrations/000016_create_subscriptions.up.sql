-- A company's subscription: the "contract" with Stripe. Our copy of
-- Stripe's subscription, refreshed from Stripe after every change (and,
-- from step 6e, by webhooks).

CREATE TABLE subscriptions (
    id                      UUID        PRIMARY KEY DEFAULT uuidv7(),
    company_id              UUID        NOT NULL REFERENCES companies (id),
    stripe_subscription_id  TEXT        NOT NULL,   -- sub_...

    -- Stripe's status, as is:
    --   incomplete          created, first invoice not paid yet (pay first!)
    --   incomplete_expired  first invoice never paid (Stripe gives up after 23h)
    --   active / trialing   paid up: the company may use the product
    --   past_due            a renewal failed; Stripe is retrying
    --   unpaid / canceled   Stripe gave up / the subscription ended
    --   paused              collection paused
    status                  TEXT        NOT NULL,
    -- Every item of a subscription shares one interval (a Stripe rule).
    billing_interval        TEXT        NOT NULL,
    -- The period being paid for right now. NULL while Stripe has none.
    current_period_start    TIMESTAMPTZ NULL,
    current_period_end      TIMESTAMPTZ NULL,
    -- true = ends at current_period_end (the customer cancelled; step 6f).
    cancel_at_period_end    BOOLEAN     NOT NULL DEFAULT false,
    canceled_at             TIMESTAMPTZ NULL,

    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT subscriptions_stripe_id_format CHECK (stripe_subscription_id ~ '^sub_[A-Za-z0-9]+$'),
    CONSTRAINT subscriptions_status CHECK (status IN (
        'incomplete', 'incomplete_expired', 'trialing', 'active',
        'past_due', 'unpaid', 'canceled', 'paused')),
    CONSTRAINT subscriptions_billing_interval CHECK (billing_interval IN ('month', 'year'))
);

CREATE UNIQUE INDEX subscriptions_stripe_id_uniq ON subscriptions (stripe_subscription_id);

-- At most ONE subscription per company that still counts. Ended ones
-- (canceled, never paid) stay as history. This index, not the service, is
-- what makes two racing "subscribe" clicks safe.
CREATE UNIQUE INDEX subscriptions_company_live_uniq
    ON subscriptions (company_id)
    WHERE status NOT IN ('canceled', 'incomplete_expired');

-- "Subscriptions of company X, newest first" (history included).
CREATE INDEX subscriptions_company_idx ON subscriptions (company_id, id DESC);

CREATE TRIGGER trg_subscriptions_updated_at
    BEFORE UPDATE ON subscriptions
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- What the subscription is made of: a price and how many units of it.
-- Replaced as a whole at every sync (Stripe is the source of truth).
CREATE TABLE subscription_items (
    id                           UUID        PRIMARY KEY DEFAULT uuidv7(),
    subscription_id              UUID        NOT NULL REFERENCES subscriptions (id) ON DELETE CASCADE,
    stripe_subscription_item_id  TEXT        NOT NULL,   -- si_...
    price_id                     UUID        NOT NULL REFERENCES prices (id),
    -- 1 for flat prices; the number of active trucks for per-truck ones.
    quantity                     INTEGER     NOT NULL,
    created_at                   TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT subscription_items_stripe_id_format CHECK (stripe_subscription_item_id ~ '^si_[A-Za-z0-9]+$'),
    CONSTRAINT subscription_items_quantity CHECK (quantity >= 0)
);

CREATE UNIQUE INDEX subscription_items_stripe_id_uniq ON subscription_items (stripe_subscription_item_id);
CREATE INDEX subscription_items_subscription_idx ON subscription_items (subscription_id);
