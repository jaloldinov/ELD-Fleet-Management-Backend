# Load variables from .env if it exists and export them to commands.
-include .env
export

MIGRATIONS_DIR := migrations

.PHONY: run stripe-listen test test-integration test-e2e test-e2e-stripe test-db test-db-reset build create-admin swagger swagger-check \
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

stripe-listen:
	stripe listen --forward-to localhost:$(HTTP_PORT)/api/v1/webhooks/stripe --events $(STRIPE_EVENTS)

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
