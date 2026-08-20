.PHONY: help db migrate run test clean

help:
	@echo "Prospector — Makefile targets"
	@echo "  make db       - Create and migrate database"
	@echo "  make migrate  - Run migrations only"
	@echo "  make run      - Start the server (requires DATABASE_URL, REDIS_URL)"
	@echo "  make test     - Run the Go test suite (DB/Redis-backed tests skip if unset)"
	@echo "  make clean    - Clean build artifacts"

db:
	@echo "Creating database and running migrations..."
	createdb prospector || true
	@for f in internal/store/migrations/*.sql; do \
		echo "  applying $$f"; \
		psql prospector < $$f || exit 1; \
	done
	@echo "Done. Start with: make run"

migrate:
	@for f in internal/store/migrations/*.sql; do \
		echo "  applying $$f"; \
		psql prospector < $$f || exit 1; \
	done

run:
	go run ./cmd/prospector

test:
	go test ./...

clean:
	rm -rf bin/
	go clean
