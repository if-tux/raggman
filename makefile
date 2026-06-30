DB_URL := "postgres://raggman:secretpassword@localhost:5432/raggman_db?sslmode=disable"
MIGRATION_PATH := "internal/postgres/migrations"

.PHONY: infra-up infra-down infra-logs ollama-start

infra-up:
	@echo "Spinning up Postgres (pgvector) and Jaeger..."
	docker compose up -d
	@echo "Waiting for Postgres to be ready to apply pgvector..."
	@until docker exec raggman_db pg_isready -U raggman -d raggman_db > /dev/null 2>&1; do sleep 1; done
	@docker exec raggman_db psql -U raggman -d raggman_db -c "CREATE EXTENSION IF NOT EXISTS vector;"
	@echo "Infrastructure is up and pgvector is enabled!"

infra-down:
	@echo "Destroying infrastructure"
	docker compose down -v

infra-logs:
	docker compose logs -f

ollama-start:
	@echo "Starting native Ollama with qwen2.5:7b..."
	ollama run qwen2.5:7b

.PHONY: db-create-migration db-up db-down db-force pgvector-setup

db-create-migration:
	@echo "Creating migration $(name)"
	migrate create -ext sql -dir $(MIGRATION_PATH) -seq $(name)

db-up:
	@echo "Running migrations (Up)..."
	migrate -path $(MIGRATION_PATH) -database $(DB_URL) -verbose up

db-down:
	@echo "Reverting migration (Down 1)..."
	migrate -path $(MIGRATION_PATH) -database $(DB_URL) -verbose down 1

pgvector-setup:
	@echo "Enabling the pgvector extension on the raggman_db database..."
	docker exec -it raggman_db psql -U raggman -d raggman_db -c "CREATE EXTENSION IF NOT EXISTS vector;"

.PHONY: sqlc dev build build-prod test clean

sqlc:
	@echo "Generating sqlc code..."
	sqlc generate

dev:
	go run cmd/api/main.go

build:
	@echo "Compiling Raggman"
	go build -o bin/api cmd/api/main.go

build-prod:
	@echo "Compiling raggman optimized for production..."
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/api cmd/api/main.go

test:
	go test -v -cover ./...

clean:
	go clean -modcache
	rm -rf bin/

.PHONY: wire

wire:
	@echo "Generating dependency injection code (google/wire)..."
	wire ./cmd/api