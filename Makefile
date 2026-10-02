# Load variables from .env if it exists and export them to commands.
-include .env
export

MIGRATIONS_DIR := migrations

.PHONY: run test test-integration build \
	db-up db-down db-psql \
	migrate-create migrate-up migrate-down migrate-version migrate-force

## ---------- App ----------

run:
	go run ./cmd/api

test:
	go test ./... -race -cover

# Also runs tests that need a real database.
test-integration:
	TEST_DATABASE_URL="$(DATABASE_URL)" go test ./... -race -cover

build:
	go build -o bin/api ./cmd/api

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
