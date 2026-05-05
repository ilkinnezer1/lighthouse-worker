.PHONY: env dev run build tidy fmt vet schema sqlc

# Dumps the live dev DB schema into internal/db/schema.sql.
# Run after Nest adds a migration. Requires the dev compose to be up
# (the postgres container is what runs pg_dump — no host dep needed).
COMPOSE_FILE := ../docker-compose.dev.yml
PG_USER      := lighthouse
PG_DB        := lighthouse_dev
PG_CONTAINER := postgres

schema:
	@mkdir -p internal/db
	@docker compose -f $(COMPOSE_FILE) exec -T $(PG_CONTAINER) \
		pg_dump --schema-only --no-owner --no-privileges --no-comments \
		-U $(PG_USER) -d $(PG_DB) > internal/db/schema.sql
	@echo "Wrote internal/db/schema.sql"

# Generates Go code from the queries in internal/db/queries/, typed against
# internal/db/schema.sql. Output: internal/db/gen/. Commit the generated code
# so CI doesn't need sqlc installed.
sqlc:
	sqlc generate

env:
	@test -f .env || (cp .env.example .env && echo "Created .env (edit MASTER_ENCRYPTION_KEY)")

dev: env
	go run ./cmd/worker

run: build
	./bin/worker

build:
	go build -o bin/worker ./cmd/worker

tidy:
	go mod tidy

fmt:
	go fmt ./...

vet:
	go vet ./...