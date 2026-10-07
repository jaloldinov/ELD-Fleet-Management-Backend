-- Each company is one Stripe Customer (cus_...): the "guest file" that holds
-- its cards, subscriptions and invoices. Created the first time the company
-- needs billing, so NULL = no billing yet.
ALTER TABLE companies ADD COLUMN stripe_customer_id TEXT NULL;

ALTER TABLE companies ADD CONSTRAINT companies_stripe_customer_id_format
    CHECK (stripe_customer_id IS NULL OR stripe_customer_id ~ '^cus_[A-Za-z0-9]+$');

-- One customer belongs to one company. Not partial on deleted_at: a deleted
-- company's customer still has its invoices and must not be reused.
CREATE UNIQUE INDEX companies_stripe_customer_id_uniq ON companies (stripe_customer_id);
