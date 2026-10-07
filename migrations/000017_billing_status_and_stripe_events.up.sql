-- What the company may do, decided by its subscription (kept up to date by
-- every subscription sync, in the same transaction):
--   none      no subscription (never had one, or it ended)   -> blocked
--   pending   subscribed, first invoice not paid yet          -> blocked
--   active    paid up                                         -> allowed
--   past_due  a renewal failed, Stripe is retrying (grace)    -> allowed, warned
--   blocked   Stripe gave up (unpaid) or collection paused    -> blocked
-- Separate from is_active, which the platform admin switches by hand: a
-- payment problem and an admin decision never overwrite each other.
ALTER TABLE companies ADD COLUMN billing_status TEXT NOT NULL DEFAULT 'none';
ALTER TABLE companies ADD CONSTRAINT companies_billing_status
    CHECK (billing_status IN ('none', 'pending', 'active', 'past_due', 'blocked'));

-- Every Stripe event we received. Stripe may deliver the same event more
-- than once (and retries for 3 days while we answer with an error): the id
-- makes processing happen once.
CREATE TABLE stripe_events (
    id            TEXT        PRIMARY KEY,   -- evt_...
    type          TEXT        NOT NULL,      -- invoice.payment_failed, ...
    received_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- NULL until handled successfully; a failed attempt leaves it NULL so
    -- Stripe's retry processes it again.
    processed_at  TIMESTAMPTZ NULL,
    attempts      INTEGER     NOT NULL DEFAULT 0,
    last_error    TEXT        NULL,

    CONSTRAINT stripe_events_id_format CHECK (id ~ '^evt_[A-Za-z0-9]+$')
);

-- Finding events that keep failing (an alert or a dashboard later).
CREATE INDEX stripe_events_unprocessed_idx ON stripe_events (received_at) WHERE processed_at IS NULL;
