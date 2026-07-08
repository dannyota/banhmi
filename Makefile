.PHONY: help build test vet lint fmt generate migrate dev-up dev-down dev-reset stack-up stack-down pipeline-dev eval eval-cpu eval-onnx mcp-local mcp-onnx

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

## ── In-process OpenVINO (native host build) ──────────────
OV_DIR  := $(shell python3 -c "import openvino,os;print(os.path.dirname(openvino.__file__))" 2>/dev/null)
OV_CGO   = CGO_ENABLED=1 CGO_CFLAGS="-I$(OV_DIR)/include" CGO_LDFLAGS="-L$(OV_DIR)/libs -L/tmp/lt -lopenvino_c -Wl,-rpath,$(OV_DIR)/libs"
OV_ENV   = LD_LIBRARY_PATH=$(OV_DIR)/libs BANHMI_EMBED_QUERY=openvino BANHMI_OV_MODEL_DIR=$(HOME)/.cache/banhmi/bge-m3 BANHMI_OV_TOKENIZER=$(HOME)/.cache/banhmi/bge-m3/tokenizer.json BANHMI_OV_DEVICE=AUTO

## ── Evaluation ────────────────────────────────────────────
eval: ## Run eval with in-process OpenVINO (GPU auto)
	@$(OV_ENV) $(OV_CGO) go run -tags openvino ./cmd/eval

eval-cpu: ## Run eval (CPU only, no GPU)
	@$(OV_ENV) BANHMI_OV_DEVICE=CPU $(OV_CGO) go run -tags openvino ./cmd/eval

mcp-local: ## Run local MCP server with in-process OpenVINO (GPU auto, :8088)
	@$(OV_ENV) $(OV_CGO) go run -tags openvino ./cmd/server

## ── In-process ONNX Runtime (native host build) ─────────
ONNX_CGO  = CGO_ENABLED=1 CGO_LDFLAGS="-L$(HOME)/.local/lib"
ONNX_ENV  = LD_LIBRARY_PATH=$(HOME)/.local/lib BANHMI_EMBED_QUERY=onnx BANHMI_ONNX_MODEL=$(HOME)/.cache/banhmi/qwen3-embedding/model_int8.onnx BANHMI_ONNX_TOKENIZER=$(HOME)/.cache/banhmi/qwen3-embedding/tokenizer.json BANHMI_ONNX_LIB=$(HOME)/.local/lib/libonnxruntime.so

eval-onnx: ## Run eval with in-process ONNX Runtime
	@$(ONNX_ENV) $(ONNX_CGO) go run -tags onnx ./cmd/eval

mcp-onnx: ## Run local MCP server with in-process ONNX Runtime (:8088)
	@$(ONNX_ENV) $(ONNX_CGO) go run -tags onnx ./cmd/server

.DEFAULT_GOAL := help
