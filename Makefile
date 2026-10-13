# Developer entry points. Every target here is exercised in CI or documented
# as verified in README.md. Run `make help` for the list.

SHELL := /bin/bash
.DEFAULT_GOAL := help

# Local defaults; override via environment or a gitignored .env file.
-include .env
POSTGRES_PORT     ?= 54320
DATABASE_URL      ?= postgres://email_platform:email_platform@localhost:$(POSTGRES_PORT)/email_platform?sslmode=disable
TEST_DATABASE_URL ?= postgres://email_platform:email_platform@localhost:$(POSTGRES_PORT)/postgres?sslmode=disable
LOG_FORMAT        ?= text
export POSTGRES_PORT DATABASE_URL TEST_DATABASE_URL LOG_FORMAT APP_ENV HTTP_ADDR LOG_LEVEL SHUTDOWN_TIMEOUT

GOLANGCI_LINT := go tool -modfile=tools/go.mod golangci-lint
PNPM          := pnpm

.PHONY: help
help: ## List targets
	@grep -hE '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-18s %s\n", $$1, $$2}'

## ---- Local infrastructure ----------------------------------------------

.PHONY: up
up: ## Start PostgreSQL + Mailpit and wait until healthy
	docker compose up -d --wait postgres mailpit

.PHONY: down
down: ## Stop local infrastructure (keeps data volumes)
	docker compose down

.PHONY: migrate
migrate: ## Apply database migrations
	go run ./cmd/migrate up

.PHONY: migrate-status
migrate-status: ## Show migration status
	go run ./cmd/migrate status

.PHONY: run-api
run-api: ## Run the API on the host (needs `make up migrate`)
	go run ./cmd/api

.PHONY: run-worker
run-worker: ## Run the worker on the host (needs `make up migrate`)
	go run ./cmd/worker

.PHONY: run-dashboard
run-dashboard: ## Run the Next.js dashboard dev server
	$(PNPM) --filter dashboard dev

## ---- Quality gates -------------------------------------------------------

.PHONY: fmt
fmt: ## Format Go and TypeScript sources
	gofmt -w cmd internal db
	$(PNPM) run format

.PHONY: fmt-check
fmt-check: ## Fail if any file is not formatted
	@out=$$(gofmt -l cmd internal db); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	$(PNPM) run format:check

.PHONY: lint
lint: lint-go lint-web ## Run all linters

.PHONY: lint-go
lint-go: ## golangci-lint (incl. integration-tagged files)
	$(GOLANGCI_LINT) run ./...

.PHONY: lint-web
lint-web: ## ESLint + TypeScript typecheck for the dashboard
	$(PNPM) --filter dashboard lint
	$(PNPM) --filter dashboard typecheck

.PHONY: test
test: ## Go unit tests (no external dependencies)
	go test -race ./...

.PHONY: test-integration
test-integration: ## Go integration tests against Compose PostgreSQL (needs `make up`)
	go test -race -tags=integration -count=1 ./...

.PHONY: build
build: ## Build Go binaries into ./bin and the dashboard
	go build -trimpath -o bin/ ./cmd/...
	$(PNPM) --filter dashboard build

.PHONY: check
check: fmt-check lint test build ## Fast full gate (no Docker needed)

.PHONY: ci
ci: check test-integration ## Everything CI runs (needs `make up`)

.PHONY: smoke
smoke: ## Start API + worker binaries, probe health endpoints, stop them
	./scripts/smoke.sh
