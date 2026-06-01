.PHONY: help build test vet lint fmt generate migrate dev-up dev-down dev-reset stack-up stack-down worker-dev eval

SHELL   := bash
COMPOSE := podman compose -f deploy/compose/banhmi.yaml

help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | sort | awk -F':.*## ' '{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

## ── Build & quality ───────────────────────────────────────
build: ## Compile everything (no binaries left in the tree)
	@go build ./...

test: ## Run tests
	@go test ./...

vet: ## Run go vet
	@go vet ./...

lint: ## Run golangci-lint
	@golangci-lint run ./...

fmt: ## Format code + sort imports
	@golangci-lint fmt ./... 2>/dev/null || gofmt -w .

generate: ## Generate sqlc code from sql/
	@sqlc generate

## ── Database ──────────────────────────────────────────────
migrate-gen: ## Generate migrations from sql/*/schema.sql (requires Atlas CLI + running Postgres)
	@go run ./tools/migragen $(if $(name),-name $(name))

migrate: ## Apply pending migrations (goose + atlas.sum verification)
	@go run ./cmd/migrate

## ── Dev stack (podman) ────────────────────────────────────
dev-up: ## Start dev stack (PostgreSQL+pgvector+pg_search, Redis, Temporal)
	@$(COMPOSE) up -d

dev-down: ## Stop dev stack
	@$(COMPOSE) down

dev-reset: ## Stop dev stack and remove volumes
	@$(COMPOSE) down -v

## ── Full stack in containers (podman) ─────────────────────
stack-up: ## Start the whole stack in containers (infra + app, builds images)
	@$(COMPOSE) --profile app up -d --build

stack-down: ## Stop the whole stack (infra + app)
	@$(COMPOSE) --profile app down

worker-dev: ## Run the worker with hot reload (install: go install github.com/air-verse/air@latest)
	@air -c config/dev/air-worker.toml

## ── Evaluation ────────────────────────────────────────────
eval: ## Run the RAG accuracy eval harness over the golden set (gates default changes)
	@go run ./cmd/eval

.DEFAULT_GOAL := help
