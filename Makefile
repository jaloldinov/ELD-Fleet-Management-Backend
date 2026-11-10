# Load variables from .env if it exists and export them to commands.
-include .env
export

MIGRATIONS_DIR := migrations

.PHONY: run sim stripe-listen tunnel stripe-webhook-endpoint stripe-webhook-endpoints stripe-webhook-delete stripe-test-key test test-integration test-e2e test-e2e-stripe test-db test-db-reset build create-admin swagger swagger-check \
	db-up db-down db-psql \
	migrate-create migrate-up migrate-down migrate-version migrate-force

## ---------- App ----------

run:
	go run ./cmd/api

# Forwards Stripe's webhooks (test mode) to the local API. Run it in a second
# terminal next to `make run`. Only the events the API handles
# (internal/service/webhook.go); the CLI's signing secret goes in .env as
# STRIPE_WEBHOOK_SECRET (`stripe listen --print-secret` shows it).
STRIPE_EVENTS := customer.subscription.created,customer.subscription.updated,customer.subscription.deleted,customer.subscription.paused,customer.subscription.resumed,invoice.created,invoice.finalized,invoice.updated,invoice.voided,invoice.paid,invoice.payment_failed,invoice.payment_action_required,invoice.marked_uncollectible

# ELD simulator: drives a truck and reports to the API like a real device.
# Key from POST /api/v1/eld-devices/{id}/credentials, via the environment
# (never a flag). Example:
#   ELD_DEVICE_KEY=eldk_... make sim args="-route dallas-loop -speedup 120 -offline 10m-15m"
sim:
	go run ./cmd/eldsim -api http://localhost:$(HTTP_PORT) $(args)

stripe-listen:
	stripe listen --forward-to localhost:$(HTTP_PORT)/api/v1/webhooks/stripe --events $(STRIPE_EVENTS)

## ---------- Real Stripe webhooks through ngrok (docs/stripe-webhooks.md) ----------
# stripe-listen above forwards events through the Stripe CLI. These targets
# rehearse production instead: Stripe itself delivers to a public HTTPS
# address, registered as a webhook endpoint with its own signing secret.

comma := ,

# A public HTTPS address for the local API. Needs ngrok (brew install ngrok)
# and a free account (ngrok config add-authtoken <token>). Every request is
# shown in the inspector at http://127.0.0.1:4040. With your account's static
# domain the address never changes, so the Stripe endpoint is made once:
#   make tunnel url=https://<name>.ngrok-free.dev   (or NGROK_URL in .env)
NGROK_URL ?= $(url)
tunnel:
	@command -v ngrok >/dev/null || (echo "ngrok is not installed: brew install ngrok" && exit 1)
	ngrok http $(HTTP_PORT) $(if $(NGROK_URL),--url $(NGROK_URL))

# Refuses anything but a TEST key: these targets create and delete webhook
# endpoints, which must never touch the live account. The key is read from
# the environment by the shell ($$STRIPE_SECRET_KEY), never written into a
# command line (visible to other users in `ps`) and never printed.
stripe-test-key:
	@command -v stripe >/dev/null || (echo "the Stripe CLI is not installed: brew install stripe/stripe-cli/stripe" && exit 1)
	@case "$$STRIPE_SECRET_KEY" in sk_test_*|rk_test_*) ;; \
	  *) echo "STRIPE_SECRET_KEY in .env must be a TEST key (sk_test_...)"; exit 1;; esac

# Registers <url>/api/v1/webhooks/stripe in Stripe TEST mode with exactly
# the events the API handles (STRIPE_EVENTS, nothing to tick by hand), and
# prints the endpoint's signing secret: put it in .env as
# STRIPE_WEBHOOK_SECRET, then restart the API.
# Usage: make stripe-webhook-endpoint url=https://abc123.ngrok-free.app
stripe-webhook-endpoint: stripe-test-key
	@test -n "$(url)" || (echo "usage: make stripe-webhook-endpoint url=https://<your tunnel address>" && exit 1)
	@STRIPE_API_KEY="$$STRIPE_SECRET_KEY" stripe webhook_endpoints create \
		--url "$(url)/api/v1/webhooks/stripe" --description "local API via ngrok" \
		$(foreach e,$(subst $(comma), ,$(STRIPE_EVENTS)),--enabled-events $(e)) \
		| grep -E '"(id|url|secret)":'

# The test-mode endpoints that exist (a free ngrok address changes on every
# restart, so old endpoints pile up: delete them).
stripe-webhook-endpoints: stripe-test-key
	@STRIPE_API_KEY="$$STRIPE_SECRET_KEY" stripe webhook_endpoints list --limit 100 | grep -E '"id": "we_|"url": "https|"status":' \
		|| echo "no webhook endpoints in test mode"

# Usage: make stripe-webhook-delete id=we_...
stripe-webhook-delete: stripe-test-key
	@test -n "$(id)" || (echo "usage: make stripe-webhook-delete id=we_..." && exit 1)
	@STRIPE_API_KEY="$$STRIPE_SECRET_KEY" stripe webhook_endpoints delete $(id) --confirm | grep -E '"(id|deleted)":'

# Fast unit tests only. TEST_DATABASE_URL, TEST_REDIS_URL and STRIPE_MOCK_URL
# are cleared (they are exported from .env), so tests that need Postgres,
# Redis or stripe-mock are skipped.
test:
	TEST_DATABASE_URL= TEST_REDIS_URL= STRIPE_MOCK_URL= go test ./... -race -cover

# Also runs tests that need a real database and Redis (TEST_DATABASE_URL and
# TEST_REDIS_URL, never DATABASE_URL/REDIS_URL).
# -p 1 runs packages one at a time, because they share the same test database.
# -count=1 disables the test cache: these tests depend on the database (and
# e2e on the cmd/api binary it builds), which Go's cache cannot see.
test-integration: test-db
	go test ./... -race -cover -p 1 -count=1

# Only the end-to-end tests: builds and starts the real API against eld_test
# and drives it over HTTP. -count=1: always rerun (the binary may have changed
# even when no test file did, and Go's test cache would not notice).
test-e2e: test-db
	go test ./e2e/... -count=1 -v

# Billing end to end against REAL Stripe in TEST mode (keys from .env;
# live keys are refused). Slow (Stripe round trips, test clocks) and needs
# the network, so it is a separate, opt-in target.
test-e2e-stripe: test-db
	E2E_STRIPE=1 go test ./e2e/... -run Stripe -count=1 -v -timeout 15m

# Creates the eld_test database if missing and applies all migrations to it.
test-db:
	@docker compose exec -T postgres psql -U eld -d postgres -tAc \
		"SELECT 1 FROM pg_database WHERE datname = 'eld_test'" | grep -q 1 \
		|| docker compose exec -T postgres createdb -U eld eld_test
	migrate -path $(MIGRATIONS_DIR) -database "$(TEST_DATABASE_URL)" up

# Drops and recreates the test database from scratch (all migrations re-run).
test-db-reset:
	docker compose exec -T postgres dropdb -U eld --if-exists eld_test
	$(MAKE) test-db

build:
	go build -o bin/api ./cmd/api

# Creates a platform admin; asks for the password (hidden input).
# Usage: make create-admin email=admin@example.com first=Ada last=Admin
create-admin:
	go run ./cmd/createadmin -email="$(email)" -first-name="$(first)" -last-name="$(last)"

## ---------- Swagger ----------

# Pinned version via `go run`: everyone generates identical docs, no global install needed.
SWAG := go run github.com/swaggo/swag/cmd/swag@v1.16.6

# Regenerates docs/ from the annotations. Run after changing a handler or DTO.
swagger:
	$(SWAG) fmt
	$(SWAG) init -g cmd/api/main.go -o docs --parseInternal

# Fails if docs/ is out of date (use in CI).
swagger-check: swagger
	@git diff --exit-code --stat -- docs/ \
		|| (echo "docs/ is out of date: run 'make swagger' and commit the result" && exit 1)

## ---------- Database (Docker) ----------

db-up:
	docker compose up -d --wait

db-down:
	docker compose down

db-psql:
	docker compose exec postgres psql -U eld -d eld

## ---------- Migrations (golang-migrate CLI) ----------

# Usage: make migrate-create name=create_companies
migrate-create:
	@test -n "$(name)" || (echo "usage: make migrate-create name=<snake_case_name>" && exit 1)
	migrate create -ext sql -dir $(MIGRATIONS_DIR) -seq $(name)

migrate-up:
	migrate -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" up

# Rolls back ONLY the last migration (safer than rolling back everything).
migrate-down:
	migrate -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" down 1

migrate-version:
	migrate -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" version

# Use only after a failed migration left the database "dirty".
# Usage: make migrate-force version=1
migrate-force:
	@test -n "$(version)" || (echo "usage: make migrate-force version=<N>" && exit 1)
	migrate -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" force $(version)
