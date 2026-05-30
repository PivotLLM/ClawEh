.PHONY: dev dev-frontend dev-backend build test lint clean

# This Makefile is a thin convenience wrapper around the frontend dev loop.
# Since the WebUI is now embedded into the single `claw` binary, the
# "backend" main lives at the repo root — there is no separate claw-web binary.

# Run frontend dev server alongside the merged claw binary.
dev:
	@echo "Starting claw (merged gateway+WebUI) and the Vite frontend dev server..."
	@$(MAKE) dev-backend & $(MAKE) dev-frontend

# Start the frontend dev server (Vite, proxies /api and /webui to claw).
dev-frontend:
	cd frontend && pnpm dev

# Start the merged claw binary in dev mode.
dev-backend:
	cd .. && go run .

# Build the frontend bundle into web/backend/dist so that the Go embed in the
# merged claw binary picks it up on the next `go build`.
build:
	cd frontend && pnpm build:backend

# Run all tests (frontend lint + Go vet).
test:
	cd frontend && pnpm lint
	cd .. && go test ./...

# Lint and format.
lint:
	cd .. && go vet ./...
	cd frontend && pnpm check

# Clean build artifacts.
clean:
	rm -rf frontend/dist backend/dist
	mkdir -p backend/dist && touch backend/dist/.gitkeep
