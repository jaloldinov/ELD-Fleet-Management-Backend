-- Our copy of companies' Stripe invoices (renewals, plan changes, one-off
-- charges), kept current by webhooks. Lists and billing metrics read it;
-- the lines of one invoice are read from Stripe when it is opened.
CREATE TABLE invoices (
    id                      UUID        PRIMARY KEY DEFAULT uuidv7(),
    company_id              UUID        NOT NULL REFERENCES companies (id),
    stripe_invoice_id       TEXT        NOT NULL,   -- in_...
    stripe_subscription_id  TEXT        NULL,       -- NULL = not a subscription's (one-off charge)

    number                  TEXT        NULL,       -- "ABCD-0001": set when finalized
    -- draft (being prepared) -> open (sent, waiting for payment) -> paid
    -- open -> void (cancelled) / uncollectible (given up)
    status                  TEXT        NOT NULL,
    -- subscription_create, subscription_cycle (renewal), subscription_update
    -- (plan change), manual (one-off charge), ...
    billing_reason          TEXT        NOT NULL DEFAULT '',
    description             TEXT        NOT NULL DEFAULT '',
    currency                TEXT        NOT NULL DEFAULT 'usd',
    -- Cents. total = what the invoice is worth; amount_due = what the card
    -- is asked for after credit; amount_paid / amount_remaining follow it.
    total                   BIGINT      NOT NULL,
    amount_due              BIGINT      NOT NULL,
    amount_paid             BIGINT      NOT NULL,
    amount_remaining        BIGINT      NOT NULL,
    -- Stripe-hosted page and PDF for the customer.
    hosted_invoice_url      TEXT        NULL,
    invoice_pdf             TEXT        NULL,

    period_start            TIMESTAMPTZ NULL,
    period_end              TIMESTAMPTZ NULL,
    stripe_created_at       TIMESTAMPTZ NOT NULL,   -- when Stripe created it
    finalized_at            TIMESTAMPTZ NULL,
    paid_at                 TIMESTAMPTZ NULL,
    voided_at               TIMESTAMPTZ NULL,

    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT invoices_stripe_id_format CHECK (stripe_invoice_id ~ '^in_[A-Za-z0-9]+$'),
    CONSTRAINT invoices_status CHECK (status IN ('draft', 'open', 'paid', 'void', 'uncollectible'))
);

CREATE UNIQUE INDEX invoices_stripe_id_uniq ON invoices (stripe_invoice_id);
-- "Invoices of company X, newest first" and "open invoices".
CREATE INDEX invoices_company_idx ON invoices (company_id, stripe_created_at DESC);
CREATE INDEX invoices_status_idx ON invoices (status, stripe_created_at DESC);
-- Revenue per month (6i): paid invoices by payment date.
CREATE INDEX invoices_paid_at_idx ON invoices (paid_at) WHERE status = 'paid';

CREATE TRIGGER trg_invoices_updated_at
    BEFORE UPDATE ON invoices
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
