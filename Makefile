# Load variables from .env if it exists and export them to commands.
-include .env
export

MIGRATIONS_DIR := migrations

.PHONY: run test test-integration test-db test-db-reset build create-admin swagger swagger-check \
	db-up db-down db-psql \
	migrate-create migrate-up migrate-down migrate-version migrate-force

## ---------- App ----------

run:
	go run ./cmd/api

# Fast unit tests only. TEST_DATABASE_URL and TEST_REDIS_URL are cleared (they
# are exported from .env), so tests that need Postgres or Redis are skipped.
test:
	TEST_DATABASE_URL= TEST_REDIS_URL= go test ./... -race -cover

# Also runs tests that need a real database and Redis (TEST_DATABASE_URL and
# TEST_REDIS_URL, never DATABASE_URL/REDIS_URL).
# -p 1 runs packages one at a time, because they share the same test database.
test-integration: test-db
	go test ./... -race -cover -p 1

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
