# Developer entry points. Run `make help` for the list.

SHELL := /bin/bash
.DEFAULT_GOAL := help

# Local defaults; override via environment or a gitignored .env file.
-include .env
POSTGRES_PORT     ?= 54320
DATABASE_URL      ?= postgres://email_platform:email_platform@localhost:$(POSTGRES_PORT)/email_platform?sslmode=disable
TEST_DATABASE_URL ?= postgres://email_platform:email_platform@localhost:$(POSTGRES_PORT)/postgres?sslmode=disable
TEST_MAILPIT_URL  ?= http://127.0.0.1:8025
TEST_SMTP_ADDR    ?= 127.0.0.1:1025
LOG_FORMAT        ?= text
export NEXT_TELEMETRY_DISABLED := 1
export POSTGRES_PORT DATABASE_URL TEST_DATABASE_URL TEST_MAILPIT_URL TEST_SMTP_ADDR LOG_FORMAT APP_ENV HTTP_ADDR LOG_LEVEL SHUTDOWN_TIMEOUT

PROMETHEUS_IMAGE   := prom/prometheus:v3.13.5@sha256:aafa9d694e6a883c02db516d9f6e5b84e14195f49a2a6456a4cca815edda72b9
ALERTMANAGER_IMAGE := prom/alertmanager:v0.34.1@sha256:e9733bafb1bdef9b00e25a21f8f99dc26a22224bf16641ad754d1649f4c3357a

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

.PHONY: admin
admin: ## Admin CLI, e.g. make admin ARGS="project create -name my-app"
	go run ./cmd/admin $(ARGS)

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
lint: lint-go lint-web lint-ci sqlc-check lint-awslive lint-pins ## Run all linters

.PHONY: sqlc
sqlc: ## Regenerate internal/store from db/queries + db/migrations
	go tool -modfile=tools/sqlc/go.mod sqlc generate

.PHONY: sqlc-check
sqlc-check: ## Fail if internal/store is out of date (incl. orphaned files)
	go tool -modfile=tools/sqlc/go.mod sqlc diff
	@for f in internal/store/*.sql.go; do q=db/queries/$$(basename $$f .go); [ -f "$$q" ] || { echo "orphaned $$f (no $$q)"; exit 1; }; done

.PHONY: lint-ci
lint-ci: ## Lint GitHub Actions workflows (pinned actionlint)
	go tool -modfile=tools/actionlint/go.mod actionlint

.PHONY: lint-awslive
lint-awslive: ## Prove the live AWS tests are excluded from every gate
	./scripts/check-awslive.sh

.PHONY: lint-pins
lint-pins: ## Versions pinned in several files (Go, Node, Prometheus, Alertmanager) agree
	./scripts/check-pins.sh

.PHONY: vet-awslive
vet-awslive: ## Type-check the live AWS tests (never runs them, no AWS access)
	go vet -tags=awslive ./internal/awslive/

.PHONY: lint-go
lint-go: ## golangci-lint (incl. integration-tagged files)
	$(GOLANGCI_LINT) run ./...

.PHONY: lint-web
lint-web: ## ESLint + TypeScript typecheck for the dashboard
	$(PNPM) --filter dashboard lint
	$(PNPM) --filter dashboard typecheck

.PHONY: test
test: ## Go, dashboard and deploy script unit tests (no external dependencies)
	go test -race ./...
	$(PNPM) --filter dashboard test
	./deploy/bin/render-env_test.sh
	./scripts/imagecheck-eval_test.sh
	./scripts/imagecheck-os_test.sh
	./scripts/eolcheck-eval_test.sh
	./scripts/check-pins_test.sh

.PHONY: test-integration
test-integration: ## Go integration tests against Compose PostgreSQL (needs `make up`)
	go test -race -tags=integration -count=1 ./...

.PHONY: vulncheck
vulncheck: ## Known vulnerabilities: govulncheck (reachable Go code, incl. the caddy build) + pnpm audit (production deps, high+); needs network
	@want=$$(sed -n 's/^toolchain //p' go.mod); have=$$(go env GOVERSION); \
	if [ "$$have" != "$$want" ]; then echo "vulncheck: running $$have but go.mod pins $$want (the shipped standard library); retry with GOTOOLCHAIN=$$want"; exit 1; fi
	@want=$$(sed -n 's/^toolchain //p' go.mod); caddy=$$(sed -n 's/^toolchain //p' deploy/caddy/go.mod); \
	[ -n "$$caddy" ] || caddy=$$(sed -n 's/^go \(.*\)/go\1/p' deploy/caddy/go.mod); \
	if [ "$$caddy" != "$$want" ]; then echo "vulncheck: deploy/caddy/go.mod pins $$caddy, go.mod pins $$want; keep them equal"; exit 1; fi
	@# Built for the host, scanning the linux build the images ship; both
	@# scanners always run so one run shows every finding.
	@rc=0; bin=$$(go tool -modfile=tools/govulncheck/go.mod -n govulncheck) || exit 1; \
	GOOS=linux "$$bin" ./... || rc=1; \
	GOOS=linux "$$bin" -C deploy/caddy -tags nobadger,nomysql,nopgx ./... || rc=1; \
	$(PNPM) audit --prod --audit-level high || rc=1; \
	exit $$rc

.PHONY: eolcheck
eolcheck: ## End of life of pinned Go, Node, PostgreSQL, Prometheus (endoflife.date): fails at EOL, warns 90 days before; needs network
	./scripts/eolcheck.sh

.PHONY: vulncheck-dev
vulncheck-dev: ## Known vulnerabilities in dev/CI-only code: Go tools in tools/ + npm dev dependencies; reports, never fails on findings; needs network
	PNPM="$(PNPM)" ./scripts/vulncheck-dev.sh

.PHONY: imagecheck
imagecheck: ## Known vulnerabilities in the production container images (grype; High/Critical with a fix fail) + end of life of each image's OS; needs Docker + network
	./scripts/imagecheck.sh

.PHONY: build
build: ## Build Go binaries into ./bin and the dashboard
	go build -trimpath -o bin/ ./cmd/...
	$(PNPM) --filter dashboard build

.PHONY: check
check: fmt-check lint test build ## Fast full gate (no Docker needed)

.PHONY: ci
ci: check vulncheck imagecheck eolcheck test-alerts test-integration smoke restore-test deploy-check test-e2e ## Local superset of CI gates (needs `make up migrate`)

.PHONY: smoke
smoke: ## Real API + worker: health, send, idempotency, 422/429, log leaks, SIGTERM
	./scripts/smoke.sh

.PHONY: test-alerts
test-alerts: ## Alert rules and routing: promtool check + rule unit tests, amtool check-config (Docker)
	docker run --rm --network none -v "$(CURDIR)/deploy:/deploy:ro" --workdir /deploy/prometheus --entrypoint promtool $(PROMETHEUS_IMAGE) check config prometheus.yml
	docker run --rm --network none -v "$(CURDIR)/deploy:/deploy:ro" --workdir /deploy/prometheus --entrypoint promtool $(PROMETHEUS_IMAGE) test rules alerts_test.yml
	docker run --rm --network none -v "$(CURDIR)/deploy:/deploy:ro" --entrypoint amtool $(ALERTMANAGER_IMAGE) check-config /deploy/alertmanager/alertmanager.yml

.PHONY: deploy-check
deploy-check: ## Production Compose stack locally: TLS DB (verify-full), HTTPS via Caddy, send to Mailpit, monitoring (Docker, ~3 min)
	./scripts/deploy-check.sh

.PHONY: restore-test
restore-test: ## Backup + restore drill: pg_dump -> fresh DB -> checks -> api on the restored DB
	./scripts/restore-test.sh

.PHONY: test-e2e
test-e2e: ## Dashboard browser E2E (Playwright, Chromium) against real API + worker on a throwaway DB (needs `make up`)
	./scripts/e2e.sh $(ARGS)

.PHONY: load
load: ## Load measurement against real binaries (needs `make up migrate`; LOAD_ARGS="-c 20 -n 2000")
	./scripts/load.sh

.PHONY: load-broadcast
load-broadcast: ## Broadcast load measurement: 50k recipients + transactional sends (needs `make up migrate`; LOAD_ARGS="-contacts 50000")
	./scripts/load-broadcast.sh

.PHONY: resilience
resilience: ## Mailpit outage, worker SIGKILL, PostgreSQL restart and automation runs across worker kills against real binaries (needs `make up migrate`, minutes)
	./scripts/resilience.sh

.PHONY: accept-automations
accept-automations: ## Automations end to end through real binaries: trigger replay, waits across a worker SIGKILL, conditions, webhook retry, archive (needs `make up migrate`, ~2 min)
	./scripts/automations-acceptance.sh
