.PHONY: help db migrate run test clean

help:
	@echo "Prospector — Makefile targets"
	@echo "  make db       - Create and migrate database"
	@echo "  make migrate  - Run migrations only"
	@echo "  make run      - Start the server (requires DATABASE_URL, REDIS_URL)"
	@echo "  make test     - Run the Go test suite (DB/Redis-backed tests skip if unset)"
	@echo "  make clean    - Clean build artifacts"

db:
	@echo "Create the database named by DATABASE_URL first; inspect existing databases before migration."
	go run ./cmd/prospector-migrate -command apply

migrate:
	go run ./cmd/prospector-migrate -command apply $(if $(PROOF),-proof "$(PROOF)")

run:
	go run ./cmd/prospector

test:
	go test ./...

clean:
	rm -rf bin/
	go clean
