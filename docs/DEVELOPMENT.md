# Local development

Everything runs in **podman** containers; the only host installs are Go and a few dev CLIs. The dev
stack is the checked-in localhost config — connect to it freely. Architecture: [`ARCHITECTURE.md`](ARCHITECTURE.md);
pipeline + worker commands: [`design/PIPELINE.md`](design/PIPELINE.md).

## Prerequisites

1. **podman** + **podman-compose** (no host service installs).
2. **Go 1.26.3**.
3. **sqlc** (for `make generate`), **Atlas** + **goose** (for `make migrate-gen`). Optional: **air** (`make worker-dev` hot reload).
4. A BGE-M3 **embedder** for indexing + query. The default is a **local GPU** (OVMS container), but it's not required: **offload bulk indexing to a Kaggle GPU** (`KAGGLE_API_TOKEN`, no local GPU) and/or run the query embedder **in-process on CPU** (`-tags openvino`). Not needed for plain `build`/`test`.

## 1. Config

1. `cp config/config.example.yaml config/config.yaml` — `config.yaml` is gitignored (your local dev config).
2. `export BANHMI_DATABASE_PASSWORD=banhmi` — the local dev DB password.
3. The dev config points at the podman stack; ports live in `config/config.yaml` (Postgres `:10001`, Temporal `:10003`, embedder `:10007`, …).

## 2. Start infra + schema

1. `make dev-up` — Postgres 17 + pgvector (matches prod RDS), Redis, Temporal, Temporal UI.
2. `make migrate` — apply schema migrations (goose + `atlas.sum` verification).
3. `go run ./cmd/seed` — load operator vocabularies (scope terms, issuer codes, discovery keywords) from `deploy/seed/*.csv`.

## 3. Embedder (for index + query)

A BGE-M3 embedder is needed to **index** (chunk embeddings) and to **search** (query-time embedding).
`build`/`test` need none. Pick what fits your machine:

1. **Local GPU (default, fastest):** the OVMS BGE-M3 container, in the compose **`app` profile** — `make stack-up`, or just that service: `podman-compose -f deploy/compose/banhmi.yaml --profile app up -d embedder`.
2. **No local GPU — offload bulk indexing to Kaggle:** set `KAGGLE_API_TOKEN` and run `go run ./cmd/worker -embed-all` — bulk embedding runs on a Kaggle GPU (chunking stays local; Index writes chunks first, embeddings deferred to this batch).
3. **Query without a GPU:** build the MCP with `-tags openvino` (in-process OpenVINO BGE-M3 on CPU — the same build the cloud uses).

**Kaggle is bulk-only** — query-time search always uses the local OVMS or in-process embedder, never Kaggle.

## 4. Build the corpus (the pipeline)

The five stages are explicit — no stage auto-starts the next (the DB ledger is the handoff). Run per stage
or the whole thing. See [`design/PIPELINE.md`](design/PIPELINE.md) and `go run ./cmd/worker -h` for all flags.

1. `go run ./cmd/worker -fetch <source>` — drain discovered docs to Bronze.
2. `go run ./cmd/worker -extract-all` → `-normalize-all` → `-index-all` — Silver text → sections/validity → Gold chunks + embeddings.
3. Whole pipeline to convergence: `go run ./cmd/worker -run-all` (the `RunAll` orchestrator).
4. Optional bulk embed on Kaggle GPU: `go run ./cmd/worker -embed-all [-force]` (needs `KAGGLE_API_TOKEN`).

## 5. Serve + query the MCP

1. **stdio** (local MCP clients): `go run ./cmd/mcp`.
2. **HTTP / Streamable** (remote clients): `go run ./cmd/server -addr localhost:9099` → POST `/mcp`.
3. Drive it with curl (`initialize` → `tools/call` `search`/`document`) or a Haiku sub-agent acting as an external agent.

## 6. Everyday commands

| Command | What it does |
|---------|--------------|
| `make build` | `go build ./...` (compile check; no binaries left in the tree) |
| `make test` | `go test ./...` |
| `make fmt` | format + import sorting (run after touching Go) |
| `make generate` | regenerate sqlc after `sql/**/queries.sql` or `schema.sql` changes |
| `make migrate-gen` | Atlas diff → goose migration + `atlas.sum` after `sql/**/schema.sql` changes |
| `make lint` | golangci-lint + project linters |
| `make dev-down` / `make dev-reset` | stop infra / stop + wipe volumes (fresh DB) |
| `make worker-dev` | run the worker with hot reload (needs `air`) |

## Notes

1. **Layers communicate through the database** (Bronze → Silver → Gold), not Go imports.
2. **Don't edit generated code under `pkg/store/`** — change `sql/` and `make generate`.
3. Pre-release the DB is **not immutable**: edit `sql/**/schema.sql`, `make migrate-gen`, then reset with `make dev-reset && make migrate`.
4. Secrets live in env/file/Vault, never in YAML. The local dev password (`banhmi`) is the documented exception.
