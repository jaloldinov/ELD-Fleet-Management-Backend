# Testing real Stripe webhooks locally (ngrok)

Our automated tests sign webhooks themselves (`e2e/stripe_test.go`, `doorbell`).
That proves our handling, not Stripe's delivery. This runbook lets the **real
Stripe** (test mode) deliver to your laptop through a public HTTPS tunnel, the
way production will: an endpoint registered in Stripe, with its own signing
secret, receiving exactly the events we handle.

`make stripe-listen` (Stripe CLI forwarding) is the lighter alternative for
day-to-day work; use this runbook to rehearse production.

## One-time setup

1. `brew install ngrok`, create a free account at ngrok.com, then
   `ngrok config add-authtoken <your token>`.
2. `.env` has a **test** key: `STRIPE_SECRET_KEY=sk_test_...` (the targets
   below refuse anything else).

## Each session

1. **API:** `make run` (terminal 1).
2. **Tunnel:** `make tunnel` (terminal 2). Copy the `https://....ngrok-free.app`
   address. Every request shows in the inspector: http://127.0.0.1:4040.
   With your account's free static domain the address never changes:
   `make tunnel url=https://<name>.ngrok-free.dev` (or set `NGROK_URL` in `.env`),
   and the endpoint in step 3 is made only once.
3. **Endpoint:** `make stripe-webhook-endpoint url=https://....ngrok-free.app`
   registers `<url>/api/v1/webhooks/stripe` with the 13 events in
   `STRIPE_EVENTS` and prints the endpoint's `secret` (`whsec_...`).
4. **Secret:** set `STRIPE_WEBHOOK_SECRET=whsec_...` in `.env` (keep a final
   newline), then restart `make run`.

A free ngrok address changes on every restart: list old endpoints with
`make stripe-webhook-endpoints` and remove them with
`make stripe-webhook-delete id=we_...`.

## Scenarios

Use the billing page (`http://localhost:8080/app/`) as a company admin, with
Stripe's test cards (any future date, any CVC).

| # | Do | Expect (without pressing "refresh") |
| --- | --- | --- |
| 1 | Add card `4000 0000 0000 0341` (it saves, but every charge fails), make it the default, subscribe | first payment declined; webhook `invoice.payment_failed`; subscription stays `incomplete`, `billing_status` `pending`, fleet routes 402 |
| 2 | Add card `4242 4242 4242 4242`, make it the default, pay the open invoice | webhook `invoice.paid`; status `active`; fleet routes open |
| 3 | Add a truck | webhook `customer.subscription.updated`; the per-truck quantity is one higher |
| 4 | Stripe Dashboard > Developers > Webhooks > your endpoint > an event > **Resend** | 200 again, nothing changes twice (stored once in `stripe_events`) |
| 5 | Put a wrong `STRIPE_WEBHOOK_SECRET` in `.env`, restart, repeat 3 | every delivery 400 `invalid webhook signature`; Stripe shows failures and retries. Put the right secret back. |
| 6 | Card `4000 0025 0000 3155` as default, pay an invoice | 3-D Secure asked in the browser; after confirming, `invoice.paid` |

A failed **renewal** needs time to pass (Stripe test clocks); that path is
covered by `make test-e2e-stripe` (`TestStripe_RenewalFailsThenThePaymentUnblocks`).

## Where to look

- **ngrok inspector** (http://127.0.0.1:4040): each delivery and our response,
  which should be `200 {"received":true}`.
- **Stripe Dashboard** > Developers > Webhooks > the endpoint: delivery
  attempts and their status.
- **API logs:** `subscription synced from stripe event` (a `WARN` for payment
  problems).
- **Database:** `SELECT type, received_at, processed_at, attempts, last_error
  FROM stripe_events ORDER BY received_at DESC LIMIT 20;` (`make db-psql`).

## Notes

- The event's API version is not checked on purpose
  (`internal/stripegw/webhooks.go`): a webhook only says "something changed";
  we read the object from Stripe with our pinned version.
- A guard test keeps `STRIPE_EVENTS` and the events handled in
  `internal/service/webhook.go` identical
  (`TestWebhookEvents_MakefileMatchesHandler`).
- Production needs the same endpoint on the server's real HTTPS address,
  created in **live** mode in the Dashboard, with its own secret.
