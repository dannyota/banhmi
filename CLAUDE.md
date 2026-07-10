# CLAUDE.md

**This is the single canonical guide for banhmi** — the working agreement and conventions for every
agent and contributor. If any other doc conflicts with this file, follow this file and fix the other
doc. (There is no separate `AGENTS.md`; this file replaces it.)

Read [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) for the system design and [`PLAN.md`](PLAN.md) for
the roadmap and current phase before making changes. Local setup is in
[`docs/DEVELOPMENT.md`](docs/DEVELOPMENT.md); generic (vendor-neutral) deployment in
[`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md). Deep-dive design docs live in
[`docs/design/`](docs/design/): [`SOURCES.md`](docs/design/SOURCES.md) (scope, discovery & per-source crawl),
[`PIPELINE.md`](docs/design/PIPELINE.md) (data flows),
[`SCHEMA.md`](docs/design/SCHEMA.md) (data model + DB-seeded config),
[`EXTRACTION.md`](docs/design/EXTRACTION.md) (deterministic extraction & the per-file OCR gate),
[`RAG.md`](docs/design/RAG.md) (chunking, retrieval evidence, gaps, and eval), and
[`jurisdictions/`](docs/design/jurisdictions/README.md) (multi-country: registry +
[playbook](docs/design/jurisdictions/PLAYBOOK.md) + per-country designs — VN/MY/ID live; SG/TH proposed).

## What banhmi is

banhmi is an **evidence-only RAG corpus + MCP server** for Southeast-Asian **banking & financial
regulation** and **cross-cutting technology law** (e.g. cybersecurity, data protection, AI, cloud,
e-transactions, payments, digital banking) — **multi-jurisdiction**: one codebase, one corpus per
country (VN/MY/ID live, SG/TH proposed). It crawls each country's official government/regulator sources, extracts and normalizes documents into a
trustworthy, citable knowledge base — exact **Điều/Khoản**, validity, amendment relations, provenance,
and coverage gaps — and exposes that evidence over an **MCP server**.

**banhmi does not answer questions.** It serves data + evidence so a **user-owned agent/model**
(Claude.ai, ChatGPT, Gemini, Grok, …) connects over MCP, retrieves exact citations, validity, relations,
and gaps, and decides the answer itself. There is **no built-in answer LLM** — answering is the user's
model, or a **separate microservice** built later, never part of this product. The product boundary is
the **database/retrieval evidence**: citations, provenance, validity, coverage gaps, confidence signals.
Optional helpers must never hide weak data behind confident prose.

The MCP surface is the deployed agent contract. Tools: `guide`, `corpus_status`, `quality_gaps`,
`search`, `document`. An agent must be able to discover corpus status, search evidence, open exact
documents, and understand gaps **through MCP alone**, with no repo files or extra local prompts.

**PROJECT PURPOSE — READ THIS BEFORE TOUCHING DISCOVERY OR EXTRACTION:** BANHMI IS FOR BANKING &
FINANCIAL REGULATION AND CROSS-CUTTING TECHNOLOGY LAW (MULTI-JURISDICTION: VN, MY, ID, TH, SG). DO NOT HARDCODE DOCUMENT IDS, ONE-OFF SOURCE EXCEPTIONS, OR
"KNOWN GOOD" SHORTCUTS TO FORCE A RESULT. SCOPE MUST COME FROM THE CONFIG VOCABULARIES AND VERIFIED
SOURCE BEHAVIOR; IF THE VOCABULARY IS WRONG, FIX THE CONFIG SEED AND RE-SEED, THEN MEASURE THE REAL ROWS.

## The target (MVP1)

**INPUT before OUTPUT.** The hard, valuable part is the data: good data + any decent model = good
answers; bad data = *confidently wrong legal answers*, which is worse than nothing.

- **INPUT** (crawl → fetch → extract → normalize → index): a *trustworthy corpus* in the DB — discovery
  in scope, faithful extraction, correct Điều/Khoản structure, real validity dates, amendment relations.
- **OUTPUT** (the MCP evidence service): retrieval + the MCP tools that expose citations, validity,
  relations, and gaps. **No answer generation** — the user brings the model.

**Deployment shape** (current prod; v0.3.0 migrates read path to AWS — see [`PLAN.md`](PLAN.md)):

- **Write path — self-terminating AWS EC2 per country, in-country IP** (`cmd/pipeline`, no Temporal):
  VN **Hanoi Local Zone** `ap-southeast-1-han-1a` (VN sources geo-lock non-VN IPs), MY
  `ap-southeast-5`, ID `ap-southeast-3`; local runs for dev. File cache in **per-region S3 buckets**;
  image via **CodeBuild → ECR**. Bulk embedding offloads to **Kaggle T4 GPU**
  (`embed.engine=kaggle`, dataset I/O, free). OCR via **Document AI** (GCS-cached). Extraction via
  **go-fitz** (zero-Python, fast).
- **DB — AWS RDS PostgreSQL 17 + pgvector** (`ap-southeast-1`), one database per country.
- **Read path (current prod) — GCP Cloud Run** + Firebase Hosting, one service per country, in-process
  query embedder. **v0.3.0:** CloudFront + ECS on EC2 (ARM64 Graviton), same VPC as RDS.
- **Retrieval — hybrid**: dense Qwen3-Embedding vectors + BM25 sparse vectors (pgvector `sparsevec`)
  fused with RRF + a deterministic query router. No ParadeDB/`pg_search` (can't run on managed RDS).
- **Testing: local stack only — never cloud.** Run pipeline, `make eval`, MCP smoke tests against
  **local Postgres** (podman) and **local MCP server** (`go run ./cmd/mcp` stdio, `go run ./cmd/server`
  HTTP `:8088`). Never connect to RDS or Cloud Run for testing.
- **Versioning:** `<semver>-<YYYYMMDD>` — code + corpus snapshot. Reported by MCP `corpus_status`.
- **Deploy secrets:** write-path secrets in **AWS SSM Parameter Store** (`/banhmi/*`: DB password,
  GCP SA key, Kaggle token); RDS password also in **GCP Secret Manager** (`banhmi-db-pw`) until the
  v0.3.0 cutover. AWS credentials (IAM user `banhmi-cli`) in `.env` (gitignored). GCP account:
  `danh.software@gmail.com`.

> **Status convention:** "coded" = code written + unit/integration tests; "validated" = checked on real
> documents. VN, MY, and ID are live and validated; new work (new sources, new countries) starts as
> coded-not-validated until proven on real rows.

## Mindset

North star: **focus on real output, step by step; accuracy of the data is the target.** We are doing
law — bad data is worse than nothing. Hold this before reaching for any rule below.

- **Data accuracy is the product; the model is the user's.** The hard work is the corpus, not a model.
- **Real output, not motion.** "Containers up / build passes / deployed" is motion, not progress —
  confirm the thing actually happened (real rows in the DB, a real cited evidence pack over MCP). A green
  pipeline over an empty database is a screenshot. "Coded" is not "validated"; say which you mean. "No
  error" is not "it worked" — verify the effect.
- **Step by step.** Discuss the design before building it (show the plan first); don't rush to "all
  green." Don't build ahead of the data.
- **Be honest — report every deferral.** Name anything you stub, skip, or postpone. Never report a slice
  as done when part is unwired; never leave a confident placeholder for a feature nothing consumes; don't
  overclaim in docs or summaries — state what works vs. what does not.
- **Build only what's asked.** Don't invent optional features, paths, or scope no one requested. Don't
  "improve" adjacent behavior while fixing a specific bug. Propose the idea; build it on a yes.
- **Do simple things in a smart way.** Prefer the smallest deterministic check or local change that
  proves the result. No complex sidecars, repair paths, broad workflows, or abstractions for cleverness.
  When complexity might help, explain the trade-off and ask first.
- **Research, recommend, own it.** When a choice has a researchable best answer, dig in and commit to one
  recommendation with its trade-offs — don't hand back a bare menu.
- **Trace the real cause; blame the environment last.** Trace the real code flow before fixing a bug;
  don't write off a failure as a "sandbox / network limitation" before confirming it is real.

## Core rules

- Docs and the plan define the target. For behavior changes, update `docs/` and `PLAN.md` before or
  alongside the code.
- Record durable project context **in the repo** (this file, `docs/`, `PLAN.md`) — do **not** rely on
  machine-local or tool-specific agent memory. The repo is the only shared source of truth.
- The user decides design choices. For new tables, schema patterns, chunking/retrieval strategies,
  source-access methods, or architectural changes, present options and trade-offs — with a clear
  recommendation, never a bare menu — instead of deciding silently.
- Start with the smallest design that solves the problem. Add abstractions only when the codebase already
  calls for them.
- Do not edit generated code under `pkg/store/`. Change `sql/` and regenerate.
- Do not commit built binaries. `go build ./...` for compile checks is fine; use `go run ./cmd/...` to
  execute. Do not `go build -o ...` into the tree.
- Preserve `.gitignore` and ignore rules. Make minimal additive edits only when explicitly asked.

## Documentation

Write docs an agent can scan in one pass — long, sprawling docs get skimmed and ideas get missed.

- **Concise & focused:** lead with the point; short sentences; one concern per doc, one idea per line.
- **Tech-focused, short, easy to understand.** Prefer **lists over paragraphs** — **numbered** for
  sequences/steps, bullets otherwise; keep any paragraph to 1–2 sentences.
- **Tables/bullets over prose;** bold the key term per line so it scans.
- **Length:** keep a doc under ~500 lines and a section under ~1 screen (~40 lines); prefer merging
  related concerns into one doc over many tiny files, and only split when a doc grows past ~500 lines.
- **How to split:** split by concern into `docs/design/`. Keep it flat; only when a topic needs ≥3
  related docs give it a subfolder `docs/design/<topic>/` with a short `README.md` index.
- **Discoverability — link or it's lost:** every doc must be reachable from `README.md` and this file.
  Add user-facing docs to README's list and every design doc to the doc list above + the `docs/README.md`
  index. No orphan docs.
- **Single source of truth:** state a fact once and link to it; never repeat it across docs.
- **Keep current:** update or delete on change — no stale content; trim as you touch a doc.
- **Diagrams:** ASCII in chat/responses; Mermaid only in committed `.md` files.

## Privacy and secrets

- Never share or leak source code from this repository to external services beyond the working session.
  Do not paste source into commit messages, PR descriptions, or external tools.
- Never commit secrets, API keys, cloud project IDs, internal hostnames, Vault material, or real document
  payloads. Secrets live in env / file / Vault via the secret provider, never in YAML or code.
- Local samples and benchmark artifacts stay out of git.

## Architecture boundaries

- Layers communicate through the database (Bronze → Silver → Gold), not Go imports.
- `pkg/base/` is the shared exception and must not contain source-specific or layer-specific behavior.
- Each source under `pkg/ingest/{source}/` is self-contained: discovery, fetch, download, metadata
  parsing. Sources are wired in the composition root (`pkg/app`).
- `pkg/fetch` is the shared browser-impersonating HTTP client: utls Chrome TLS fingerprint (h1/h2
  auto-negotiation) + chromedp cookie minters (`CloudflareMinter`, `AWSWAFMinter`). Sources that sit
  behind WAFs compose a `fetch.Client` with their minter; plain sources use `ChromeTransport()` alone
  or skip it entirely. The minter runs headed Chrome when DISPLAY is set (local worker), falling back
  to `--headless=new` on headless infra.
- Extraction, embedding, and retrieval are interfaces (`pkg/extract`, `pkg/rag/embed`,
  `pkg/rag/retrieve`) with implementations selected by config. No hardcoded vendor.
- **MCP is the primary query surface.** `cmd/mcp` serves it over **stdio** (local clients); the same
  `pkg/mcp` server is served over **Streamable HTTP** from `cmd/server` for remote hosted agents (this is
  the Cloud Run deploy path). Keep retrieval/citation/evidence logic in the shared core (`pkg/rag`, `pkg/mcp`),
  not in a surface.
- Dependency wiring uses **go.uber.org/dig** at the composition root (`pkg/app`): providers live there,
  and each `cmd` builds the container and `Invoke`s what it needs. Workflows and activities take their
  dependencies via plain constructors — no DI in business logic. Resources needing the startup context or
  cleanup (DB pool) are built eagerly in `app.New` and released by `App.Close`.
- Pipeline concurrency is stage-specific. Discover/Fetch are capped by external API/download limits;
  Extract, Normalize, and Index are capped at `cores - 2` locally.

## Multi-jurisdiction

banhmi is multi-jurisdiction: **Vietnam (live — `banhmi.danny.vn`)** + **Malaysia (`laksa`, live —
`laksa.danny.vn`)** + **Indonesia (`rendang`, live — `rendang.danny.vn`)**, with **Singapore (`kaya`)
and Thailand (`tomyum`) proposed (build order SG → TH)** — registry + per-country designs in
[`docs/design/jurisdictions/`](docs/design/jurisdictions/README.md). Each jurisdiction is a **separate
corpus / DB / deployment off ONE shared codebase**, not a branch or fork; how to add a country is the
[jurisdiction playbook](docs/design/jurisdictions/PLAYBOOK.md).

- **Jurisdiction is a config dimension** with a **single descriptor registry** (`pkg/base/jurisdiction`):
  `BANHMI_JURISDICTION` (default `vn`; `my` = laksa) resolves a `Descriptor` that selects the source set,
  scope vocabulary, structure parser, content-gate profile, validity default, chunk labels, lexical
  router profile, OCR languages, default DB name, and eval golden file. MCP brief is the one remaining
  `case` switch (irreducible large custom text). Each jurisdiction writes to its own DB (`laksa` on the
  same RDS). Adding a country means one registry entry plus its irreducible new code (sources, parser,
  brief) — see the [playbook](docs/design/jurisdictions/PLAYBOOK.md#seam-registry--shipped).

- **One main language per country (native = ground truth).** Each country's corpus is in its single main
  legal language — **VN: Vietnamese; MY: English; ID: Indonesian** — and banhmi indexes, serves, and supports search in
  **that language only**. The native text is the binding ground truth; banhmi **never translates** legal
  text (translation risks legal error). Translating a result to another language is the **user's own
  responsibility**. No multilingual/translated index, no in-corpus English/Chinese layer.
- **Share only the common; customize what differs — behind interfaces** (Go idiom: interface at the
  consumer + config-selected impl, as already done for sources/extractors/embedders). Common = pipeline,
  extract mechanics, embedding, retrieval mechanics, MCP framework. Customized = source set, provision/
  citation model, structure parser, scope signal, MCP brief/guide/language. Don't force two jurisdictions
  into one shape, and don't fork.
- **VN, MY, and ID are LIVE in production — protect every live jurisdiction.** Before changing any shared
  code, check who uses it. Default every jurisdiction switch to VN. Never change `gold.chunk.citation`
  bytes or force a live-corpus re-index/re-embed without explicit sign-off. Keep VN brief/guide/labels
  as the compiled fallback.
- **Improve VN where the generalization allows** (centralize duplicated label maps, etc.) — but as
  separate, VN-safe changes guarded by regression tests.

## Data and sources

- sqlc is the data layer. Change `sql/**/schema.sql` and `sql/**/queries.sql`, then `make generate`.
- Every table's primary key is a **single column** — a surrogate `id BIGINT GENERATED ALWAYS AS IDENTITY`
  (or a single natural-id / UUID where that *is* the identity). **Never a composite PRIMARY KEY.**
  Natural/business keys are composite `UNIQUE` constraints (which power idempotent `ON CONFLICT` upserts).
- Medallion schemas: `bronze`, `silver`, `gold`, `ingest` (cursors, queues, run state), and `config`
  (operator-tunable defaults). Pipeline state belongs in `ingest`, never in `bronze`.
- **Schema changes:** edit `sql/{schema}/schema.sql` (the single source for both sqlc and migrations),
  then run `make migrate-gen`. The generator (`tools/migragen`) runs `atlas migrate diff` per schema
  against a throwaway dev DB, post-processes to goose-format SQL, updates `atlas.sum`, and writes
  `deploy/migrations/{schema}/`. Extensions live in the hand-written
  `deploy/migrations/extensions/00001_extensions.sql` and are NOT managed by Atlas. `cmd/migrate` applies
  all dirs in order and verifies every `atlas.sum` checksum before touching the DB.
- **No hardcoded policy lists.** Tunable vocabularies — scope terms, issuer codes, discovery keywords —
  live in the `config` schema, never in Go. Defaults ship as CSVs in `deploy/seed/` and load via
  `go run ./cmd/seed`: it replaces `origin='seed'` rows and preserves operator `origin='user'` rows.
  Change a default by editing the CSV and re-seeding; code reads config at startup. Use sub-agents to
  research and grow these seed CSVs.
- Pre-release, schema and migrations are **not yet immutable**: until the first tagged release, edit
  `sql/**/schema.sql`, run `make migrate-gen`, and reset the dev DB (drop schemas + `make migrate`)
  instead of appending fix-up migrations. After release, migrations are append-only.
- RAG vectors live in PostgreSQL via pgvector (one datastore). Raw files (PDF/DOCX) and OCR page images
  live in object storage / a volume, referenced from `bronze` by path + content hash — not in Postgres.
- Queryable fields are columns; queryable arrays are child tables; non-queryable data is JSONB. bronze/
  silver rows carry a `content_hash` for idempotency + change detection.
- Don't infer a field's type or nullability from one sample — assume nullable/variant until real data
  proves otherwise and parse defensively (e.g. vbpl `effStatus`).
- Confirm writes landed: after an upsert/insert, count rows. A swallowed type-inference error (42P08) can
  report success while writing nothing — "no error" is not "it worked".
- Source text strategy: the VN sources — congbao, vbpl, sbv_hanoi, and vanban — are all
  **authoritative government sources** (MY: agclom, bnm, sc — born-digital PDF first). Prefer official
  DOCX, then official HTML body, then DOC-as-PDF, then PDF/OCR; the born-digital cascade runs via
  **go-fitz** (MuPDF, zero-Python) in the app container. vbpl also provides the richest provision tree,
  relation graph, and validity data. OCR is the floor for scanned or failed PDFs. See
  [`docs/design/SOURCES.md`](docs/design/SOURCES.md).
- Treat all source data as large: prefer cursor/page-token iteration with callbacks over returning
  slices. Maintain per-source cursors and watermarks for incremental daily discovery.
- Crawl politely: descriptive User-Agent, fetch concurrency caps, backoff on 429/5xx, keep provenance.

## Extraction, RAG, and evidence

- Extraction keeps deterministic sources first and **no AI as the canonical parser**. The cascade per
  document is **DOCX → HTML body → DOC → PDF/OCR**: `.docx`, HTML body, legacy `.doc`, and born-digital
  PDFs are extracted by **go-fitz** (MuPDF via purego, zero-Python). PDF assessment is Go-owned: try
  go-fitz and run the Go content gate; a scan that fails is tracked (`needs_review`) and OCR runs as a
  **batch** (`OcrAll`, the twin of bulk embedding) — **GCP Document AI** Enterprise OCR (default,
  GCS-cached) per `ocr.engine=documentai`, never inline. EasyOCR (per-jurisdiction language) remains
  available as an offline fallback (`ocr.engine=auto/local/kaggle`). Do not reintroduce inline OCR, an
  OCR sidecar, figure extraction, or repair paths without a reviewed design. Gemma 4 E4B OCR enhancement
  is **MVP2, not current work**. AGPL-3.0 for go-fitz/MuPDF is fine (batch worker, not a network service;
  repo is public). OCR text is never the sole source of binding legal text. See
  [`docs/design/EXTRACTION.md`](docs/design/EXTRACTION.md).
- Persist extraction provenance: engine, version, confidence, `source` kind, `verified` flag.
- Chunk by Điều with citation metadata. Every chunk carries its exact Điều/Khoản citation + a
  deterministic contextual prefix. Retrieval is **hybrid** — dense Qwen3-Embedding vectors + **BM25 sparse vectors**
  (pgvector `sparsevec`, built by `cmd/lexindex`) fused with RRF + a query router — under a current-law
  pre-filter (`in_force` + `partial`). **The query-time embedder is required, not optional.** The lexical
  arm is native pgvector (no `pg_search` — unavailable on managed RDS); each hit returns both the dense
  similarity and the BM25 score.
- **Embedder: Qwen3-Embedding-0.6B ONNX FP16** everywhere (index/query parity, 1024 dims, 32K
  context). **ORT 1.26.0** (`libonnxruntime.so`); 1.27+ GPU requires CUDA 13. Go bindings
  `v1.28.1` (fallback API 17→26). FP16 over INT8: ONNX INT8 dynamic quantization has no CUDA
  kernels — FP16 required for GPU. FP16 external data format (`model_fp16.onnx` +
  `model_fp16.onnx_data`, 1.2 GB) allows ORT to mmap weights; 3 ECS containers share pages.
- **Bulk embedding offloads to Kaggle T4 GPU** (`embed.engine=kaggle`, free) via **Kaggle dataset
  I/O**: the pipeline uploads chunk texts as a Kaggle dataset, pushes a GPU kernel (Qwen3 ONNX
  FP16), polls to completion, and downloads the output vectors — **no GCS in the loop**; on
  success the kernel + input dataset auto-delete. Each run gets a fresh GPU. The Cloud Run L4 GPU
  engine (`embed.engine=cloudrun`, GCS batch) is dropped. The HTTP embed server (`-serve-embed`)
  remains as a fallback for query-time embedding only. Query-time embedding is **in-process ONNX
  Runtime** (`-tags onnx`) on the MCP server. See
  [`docs/design/RAG.md`](docs/design/RAG.md#batch-embedding-kaggle).
- **Never bulk-embed on the dev machine.** The laptop (8 GB) can't handle batch GPU workloads.
  Offload to Kaggle. Read-path (query-time) embedding locally is fine (~50ms).
- **Eval golden sets: realistic phrasing only.** Questions must sound like real users — practical,
  scenario-based, conversational. Not bare số ký hiệu, keyword dumps, or stiff phrasing. Edge
  cases (identifier, no-diacritics, historical) embedded in natural questions.
- **Evidence, not answers.** The MCP tools expose ranked hits with exact citations, validity badges,
  confirmed relations, provenance, and explicit gaps. banhmi does not synthesize an answer or call an
  answer LLM — the user's model does that. Never present repealed/superseded/not-yet-effective text as
  current.

## Code style

- Follow Google Go style. MixedCaps names; no `Get` prefix on getters.
- Import groups: stdlib, external, internal, separated by blank lines. Alias only on collision.
- Return errors; do not panic, `log.Fatal`, or `os.Exit` in library code. Wrap with `%w`:
  `fmt.Errorf("fetch document: %w", err)`. Do not prefix messages with "failed to".
- Do not silently ignore errors; `_ =` only for intentional discards. Never log and return the same error.
- Use `log/slog` with structured fields. No `fmt.Print*` / `log.Print*`.
- Keep linear logic inline; extract helpers only when reused or independently testable. Define interfaces
  at the consumer. No `//nolint` without explicit approval.

## Containers (podman-first)

- All infrastructure and extraction engines run as OCI containers via podman / podman-compose / Quadlet.
  No host installs. Container build files are `Containerfile` (not `Dockerfile`).
- **Local dev stack:** the checked-in dev config points at the podman localhost stack. Agents may connect
  to the local DB ports and the MCP server for verification, because dev is localhost by design. Agents
  may set the documented local `BANHMI_DATABASE_PASSWORD` env var when missing. Localhost ports, the dev
  DB user, and the dev DB name are not sensitive in summaries; non-localhost hosts and real deployment
  secrets remain sensitive.
- DOCX/HTML/PDF extraction runs through **go-fitz** (MuPDF, zero-Python) in the Go app container; OCR
  (**Document AI**, default, GCS-cached; EasyOCR as offline fallback) runs as a batch. Embedder details
  in [Extraction, RAG, and evidence](#extraction-rag-and-evidence).
- Respect the host budget. The dev box (~8 GB RAM) already runs Postgres plus local extraction tools;
  don't stand up heavy services that OOM it.
- **Podman cleanup: remove by exact name only — never blanket-prune.** The host runs multiple projects'
  containers/volumes; `podman volume prune`, `system prune`, or any `-a`/dangling-wide command can
  destroy another project's data (this happened once — hotpot dev volumes lost to a prune). Use
  `podman volume rm <name>` / `podman rm <name>` on names you have verified belong to banhmi
  (`banhmi_*` / compose-project prefix), and list before removing.

## Verification

Use the narrowest check that proves the change, then broaden for shared or risky work.

```bash
make fmt          # format + import sorting
make generate     # after SQL changes (sqlc)
make migrate-gen  # after sql/**/schema.sql changes (Atlas diff → goose migration + atlas.sum)
go build ./...    # compile check; leaves no binaries
make test         # go test ./...
make lint         # golangci-lint + project linters
make eval         # RAG accuracy eval over the golden set (gates retrieval/default changes)
```

Other pipeline commands (not verification, but agents need to know):
- `go run ./cmd/lexindex` — build BM25 sparse vectors (`gold.chunk.content_sparse`) for hybrid retrieval.
  Run after `index-all`; required before hybrid mode works. Also runs as part of `cmd/pipeline -run-all`.

- Unit tests use inline data, no external dependencies. Table-driven tests use `t.Run()`.
- Integration tests use embedded PostgreSQL (with pgvector) and skip cleanly when samples are absent.
- For DB / MCP-contract testing, the maintainer's pattern is **one Haiku-model sub-agent driving the
  localhost MCP server** as a stand-in external agent (no repo files) — this is how we validate the MCP
  contract before cloud deploy.
- Run `make fmt` after touching Go code and `make generate` after SQL changes.

## Commit messages

Conventional Commits, imperative mood, subject under 72 chars; explain why in the body when needed.

```text
feat: add congbao gazette crawler
fix: handle UUID-keyed documents in vbpl source
docs: document tiered extraction strategy
```

**Do not push to the remote** unless the maintainer explicitly asks. Commit locally; the maintainer
decides when to push. When asked to commit, commit **directly on the current branch** (the maintainer
works on `master`); do not create a branch first unless asked.

Never add `Co-authored-by`, `Signed-off-by`, or any AI/Claude authorship trailer. Commits appear as the
developer's own work.

## Commit signing

Commits and tags are signed with the repo-local key at `.claude/commit_sign.key`, set in this repo's
local git config (`gpg.format=ssh`, `gpg.ssh.program=ssh-keygen`, `commit.gpgsign=true`). Using
`ssh-keygen` as the signer bypasses any machine-wide SSH signer. The key lives under the gitignored
`.claude/` directory and must never be committed. Never hardcode absolute machine paths (e.g. a home
directory) in committed files, configs, or docs; use repo-relative paths.

## Sub-agents

- **IMPORTANT — model policy:** the orchestrating assistant may run a frontier model (e.g. Fable 5),
  but sub-agents and workflow fan-outs use **Opus or Sonnet only** — Opus by default, Sonnet
  (`claude-sonnet-4-6`, never the `sonnet` alias) when the task is mechanical or bounded. Never use
  Haiku for orchestration work. The one deliberate exception is the Haiku-over-MCP stand-in agent in
  [Verification](#verification) — a small model there proves the MCP evidence contract works without
  model smarts.
- **Division of labor:** implementation work goes to **Opus** sub-agents; a **Fable** sub-agent is
  used to review `PLAN.md` after significant rewrites. The orchestrator **always reviews sub-agent
  output itself** — read the diff, run the verification, confirm the result — before accepting or
  reporting it.
- Give each sub-agent a **bounded scope, the docs to read, clear file ownership, and the current target**
  (so it never drifts from this guide). Tell it that it is not alone in the codebase and must not revert
  or overwrite unrelated changes.
- Sub-agents follow this guide and the same secret-handling rules. They must not commit, push, or rewrite
  history unless explicitly asked, and must report changed files, the verification they ran, and
  unresolved risks.
