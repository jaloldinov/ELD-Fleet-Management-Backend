-- The billing catalog: what companies can subscribe to, and for how much.
-- Stripe is the source of truth; these rows are our copy, written after the
-- Stripe call succeeds (and, from step 6e, kept in sync by webhooks).

CREATE TABLE products (
    id                 UUID        PRIMARY KEY DEFAULT uuidv7(),
    -- Stripe's id (prod_...). Our id is also stored in Stripe's metadata
    -- (eld_product_id), so each side can find the other.
    stripe_product_id  TEXT        NOT NULL,

    name               TEXT        NOT NULL,   -- shown on invoices
    description        TEXT        NOT NULL DEFAULT '',

    -- Archived products are never deleted (old subscriptions and invoices
    -- point to them); they just cannot be chosen any more.
    is_active          BOOLEAN     NOT NULL DEFAULT true,

    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT products_stripe_id_format CHECK (stripe_product_id ~ '^prod_[A-Za-z0-9]+$'),
    CONSTRAINT products_name_length CHECK (char_length(name) BETWEEN 1 AND 200),
    CONSTRAINT products_description_length CHECK (char_length(description) <= 1000)
);

CREATE UNIQUE INDEX products_stripe_id_uniq ON products (stripe_product_id);

CREATE TRIGGER trg_products_updated_at
    BEFORE UPDATE ON products
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- A price: how much a product costs and how often. In Stripe the amount,
-- currency and interval of a price can never change: a new amount is a new
-- price (and the old one is archived).
CREATE TABLE prices (
    id                 UUID        PRIMARY KEY DEFAULT uuidv7(),
    product_id         UUID        NOT NULL REFERENCES products (id),
    stripe_price_id    TEXT        NOT NULL,   -- price_...

    nickname           TEXT        NOT NULL DEFAULT '',   -- "Monthly", "Yearly"
    currency           TEXT        NOT NULL DEFAULT 'usd',
    -- Smallest currency unit (cents): 2500 = $25.00. Integers, never
    -- floats: 0.1 + 0.2 is not 0.3 in floating point.
    unit_amount        BIGINT      NOT NULL,
    -- "interval" alone is an SQL type name, hence billing_interval.
    billing_interval   TEXT        NOT NULL,
    -- What one unit is: 'company' = a flat amount per company,
    -- 'truck' = per active truck (the quantity follows the fleet size).
    billing_unit       TEXT        NOT NULL,

    is_active          BOOLEAN     NOT NULL DEFAULT true,

    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT prices_stripe_id_format CHECK (stripe_price_id ~ '^price_[A-Za-z0-9]+$'),
    CONSTRAINT prices_nickname_length CHECK (char_length(nickname) <= 100),
    -- US market only, for now.
    CONSTRAINT prices_currency CHECK (currency = 'usd'),
    -- Stripe's maximum for USD is 99,999,999 cents ($999,999.99).
    CONSTRAINT prices_unit_amount_range CHECK (unit_amount BETWEEN 0 AND 99999999),
    CONSTRAINT prices_billing_interval CHECK (billing_interval IN ('month', 'year')),
    CONSTRAINT prices_billing_unit CHECK (billing_unit IN ('company', 'truck'))
);

CREATE UNIQUE INDEX prices_stripe_id_uniq ON prices (stripe_price_id);
-- "prices of product X" (every product read loads its prices).
CREATE INDEX prices_product_idx ON prices (product_id);

CREATE TRIGGER trg_prices_updated_at
    BEFORE UPDATE ON prices
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
