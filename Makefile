.PHONY: help build test vet lint fmt generate migrate dev-up dev-down dev-reset stack-up stack-down pipeline-dev eval-onnx mcp-onnx eval-vn eval-my eval-id

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
dev-up: ## Start dev stack (PostgreSQL+pgvector, Redis, Temporal)
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

pipeline-dev: ## Run the pipeline with hot reload (install: go install github.com/air-verse/air@latest)
	@air -c config/dev/air-pipeline.toml

## ── In-process ONNX Runtime (native host build) ─────────
ONNX_CGO  = CGO_ENABLED=1 CGO_LDFLAGS="-L$(HOME)/.local/lib"
ONNX_ENV  = LD_LIBRARY_PATH=$(HOME)/.local/lib BANHMI_EMBED_QUERY=onnx BANHMI_ONNX_MODEL=$(HOME)/.cache/banhmi/qwen3-embedding/model_fp16.onnx BANHMI_ONNX_TOKENIZER=$(HOME)/.cache/banhmi/qwen3-embedding/tokenizer.json BANHMI_ONNX_LIB=$(HOME)/.local/lib/libonnxruntime.so

eval-onnx: ## Run eval with in-process ONNX Runtime
	@$(ONNX_ENV) $(ONNX_CGO) go run -tags onnx ./cmd/eval

mcp-onnx: ## Run local MCP server with in-process ONNX Runtime (:8088)
	@$(ONNX_ENV) $(ONNX_CGO) go run -tags onnx ./cmd/server

## ── Per-jurisdiction eval (floors track the last accepted baseline in PLAN.md) ──
eval-vn: ## Run eval for Vietnam (recall>=0.79, mrr>=0.56, inforce>=0.99, abstain>=0.95)
	@BANHMI_JURISDICTION=vn $(ONNX_ENV) $(ONNX_CGO) go run -tags onnx ./cmd/eval \
		-out test/samples/eval/vn-$$(date +%Y%m%d-%H%M).json \
		-min-recall 0.79 -min-mrr 0.56 -min-inforce 0.99 -min-abstain 0.95

eval-my: ## Run eval for Malaysia (recall>=0.89, mrr>=0.69, inforce>=0.99, abstain>=0.98)
	@BANHMI_JURISDICTION=my $(ONNX_ENV) $(ONNX_CGO) go run -tags onnx ./cmd/eval \
		-out test/samples/eval/my-$$(date +%Y%m%d-%H%M).json \
		-min-recall 0.89 -min-mrr 0.69 -min-inforce 0.99 -min-abstain 0.98

eval-id: ## Run eval for Indonesia (recall>=0.70, mrr>=0.57, inforce>=0.99, abstain>=0.98)
	@BANHMI_JURISDICTION=id $(ONNX_ENV) $(ONNX_CGO) go run -tags onnx ./cmd/eval \
		-out test/samples/eval/id-$$(date +%Y%m%d-%H%M).json \
		-min-recall 0.70 -min-mrr 0.57 -min-inforce 0.99 -min-abstain 0.98

.DEFAULT_GOAL := help
