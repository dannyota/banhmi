# CLAUDE.md

**This is the single canonical guide for banhmi** — the working agreement and conventions for every
agent and contributor. If any other doc conflicts with this file, follow this file and fix the other
doc. (There is no separate `AGENTS.md`; this file replaces it.)

Read [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) for the system design and [`PLAN.md`](PLAN.md) for
the roadmap and current phase before making changes. Local setup is in
[`docs/DEVELOPMENT.md`](docs/DEVELOPMENT.md); generic (vendor-neutral) deployment in
[`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md). Deep-dive design docs live in
[`docs/design/`](docs/design/): [`SOURCES.md`](docs/design/SOURCES.md) (scope, discovery & per-source crawl),
[`PIPELINE.md`](docs/design/PIPELINE.md) (data flows + Temporal workflows),
[`SCHEMA.md`](docs/design/SCHEMA.md) (data model + DB-seeded config),
[`EXTRACTION.md`](docs/design/EXTRACTION.md) (deterministic extraction & the per-file OCR gate),
[`RAG.md`](docs/design/RAG.md) (chunking, retrieval evidence, gaps, and eval), and
[`MALAYSIA.md`](docs/design/MALAYSIA.md) (proposed Malaysia jurisdiction — `laksa`).

## What banhmi is

banhmi is an **evidence-only RAG corpus + MCP server** for Vietnamese banking **digital/technology**
regulation (IT, cybersecurity, data, cloud, e-transactions, outsourcing, digital channels, technology
operations). It crawls official government/regulator sources, extracts and normalizes documents into a
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

**PROJECT PURPOSE — READ THIS BEFORE TOUCHING DISCOVERY OR EXTRACTION:** BANHMI IS FOR VIETNAMESE
BANKING DIGITAL/TECHNOLOGY REGULATION. DO NOT HARDCODE DOCUMENT IDS, ONE-OFF SOURCE EXCEPTIONS, OR
"KNOWN GOOD" SHORTCUTS TO FORCE A RESULT. SCOPE MUST COME FROM THE CONFIG VOCABULARIES AND VERIFIED
SOURCE BEHAVIOR; IF THE VOCABULARY IS WRONG, FIX THE CONFIG SEED AND RE-SEED, THEN MEASURE THE REAL ROWS.

## The target (MVP1)

**INPUT before OUTPUT.** The hard, valuable part is the data: good data + any decent model = good
answers; bad data = *confidently wrong legal answers*, which is worse than nothing.

- **INPUT** (crawl → fetch → extract → normalize → index): a *trustworthy corpus* in the DB — discovery
  in scope, faithful extraction, correct Điều/Khoản structure, real validity dates, amendment relations.
- **OUTPUT** (the MCP evidence service): retrieval + the MCP tools that expose citations, validity,
  relations, and gaps. **No answer generation** — the user brings the model.

**Deployment shape (the MVP1 output) — split-cloud, scale-to-zero (decided 2026-05-31, SHIPPED 2026-06-01):**

- **Worker runs locally** (uses the local GPU for extract / embed / index) and **writes the corpus over
  TLS to AWS RDS PostgreSQL** (PG17, pgvector/HNSW; `ap-southeast-1` Singapore). *(Originally planned on
  Neon serverless; switched at deploy time — Neon's 512 MB free cap overflowed mid-restore.)*
- **The MCP server runs on GCP Cloud Run** as one scale-to-zero service that **embeds queries in-process**
  via OpenVINO running the index BGE-M3 INT8 model (`-tags openvino`) — **single self-contained binary, no
  OVMS, no sidecar**. *(The original plan was an OVMS CPU embedder sidecar; the in-process build replaced
  it — one image, exact OVMS parity.)* The **public endpoint `https://banhmi.danny.vn/mcp`** is served by
  **Firebase Hosting** (free Spark) in front of Cloud Run — not a Cloud Run domain mapping, not a load
  balancer. Hosted agents (Claude.ai/ChatGPT/Gemini/Grok) connect over **remote MCP (Streamable HTTP)**.
  Co-locate the regions (RDS `aws-ap-southeast-1` ↔ Cloud Run `asia-southeast1`, both Singapore).
- **Retrieval is hybrid** (pgvector, single datastore): dense BGE-M3 vectors + **BM25 sparse vectors**
  (`sparsevec`), fused with RRF and a **deterministic query router** (boost the lexical arm only for
  diacritic-less or số-ký-hiệu queries, vector-primary otherwise). No ParadeDB/`pg_search` — it can't run
  on managed RDS. Eval beats vector-only: recall@k 85.7%→89.3%, mrr 78.6%→84.6%, current-law 100%.
- **Sequence: validate all dev locally first, then deploy DB + MCP to the cloud.** Do not start cloud
  work until the local corpus + MCP contract are validated on real documents.

> **Status convention:** "coded" = code written + unit/integration tests; "validated" = checked on real
> SBV documents. Most of the spine is **coded but not validated** — validation *is* the MVP1 work.

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
- Extraction, embedding, and retrieval are interfaces (`pkg/extract`, `pkg/rag/embed`,
  `pkg/rag/retrieve`) with implementations selected by config. No hardcoded vendor.
- **MCP is the primary query surface.** `cmd/mcp` serves it over **stdio** (local clients); the same
  `pkg/mcp` server is served over **Streamable HTTP** from `cmd/server` for remote hosted agents (this is
  the Cloud Run deploy path). Keep retrieval/citation/evidence logic in the shared core (`pkg/rag`, `pkg/mcp`),
  not in a surface.
- Dependency wiring uses **go.uber.org/dig** at the composition root (`pkg/app`): providers live there,
  and each `cmd` builds the container and `Invoke`s what it needs. Workflows and activities take their
  dependencies via plain constructors — no DI in business logic. Resources needing the startup context or
  cleanup (DB pool, Temporal client) are built eagerly in `app.New` and released by `App.Close`.
- Temporal backpressure is stage-specific. Discover/Fetch use the external activity queue and its remote
  API/download cap; Extract, Normalize, and Index use a separate local activity queue capped at
  `cores - 2`. Do not use one worker-wide cap to throttle every stage.

## Multi-jurisdiction

banhmi is multi-jurisdiction: **Vietnam (live)** + **Malaysia (`laksa`, proposed)** — see
[`docs/design/MALAYSIA.md`](docs/design/MALAYSIA.md). Each jurisdiction is a **separate corpus / DB /
deployment off ONE shared codebase**, not a branch or fork.

- **One main language per country (native = ground truth).** Each country's corpus is in its single main
  legal language — **VN: Vietnamese; MY: English** — and banhmi indexes, serves, and supports search in
  **that language only**. The native text is the binding ground truth; banhmi **never translates** legal
  text (translation risks legal error). Translating a result to another language is the **user's own
  responsibility**. No multilingual/translated index, no in-corpus English/Chinese layer.
- **Share only the common; customize what differs — behind interfaces** (Go idiom: interface at the
  consumer + config-selected impl, as already done for sources/extractors/embedders). Common = pipeline,
  extract mechanics, embedding, retrieval mechanics, MCP framework. Customized = source set, provision/
  citation model, structure parser, scope signal, MCP brief/guide/language. Don't force two jurisdictions
  into one shape, and don't fork.
- **VN is LIVE in production — protect it.** Before changing any shared code, check whether VN uses it.
  Default every jurisdiction switch to VN. Never change `gold.chunk.citation` bytes or force a VN
  re-index/re-embed without explicit sign-off. Keep VN brief/guide/labels as the compiled fallback.
- **Improve VN where the generalization allows** (centralize duplicated label maps, de-hardcode the `nhnn`
  signal, etc.) — but as separate, VN-safe changes guarded by regression tests.

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
- Source text strategy: congbao, vbpl, and sbv_hanoi are all **authoritative government sources**. Prefer official
  DOCX, then official HTML body, then DOC-as-PDF, then PDF/OCR; the born-digital cascade runs via **local
  MarkItDown** in the app container. vbpl also provides the richest provision tree, relation graph, and
  validity data. OCR is the floor for scanned or failed PDFs. See [`docs/design/SOURCES.md`](docs/design/SOURCES.md).
- Treat all source data as large: prefer cursor/page-token iteration with callbacks over returning
  slices. Maintain per-source cursors and watermarks for incremental daily discovery.
- Crawl politely: descriptive User-Agent, Temporal activity caps for fetch concurrency, backoff on
  429/5xx, keep provenance.

## Extraction, RAG, and evidence

- Extraction keeps deterministic sources first and **no AI as the canonical parser**. The cascade per
  document is **DOCX → HTML body → DOC → PDF/OCR**: `.docx`, HTML body, legacy `.doc`, and born-digital
  PDFs are converted to GFM Markdown by **local MarkItDown** (Python/MIT). PDF assessment is Go-owned: try
  MarkItDown and run the Go content gate; a scan that fails is tracked (`needs_review`) and OCR runs as a
  **batch** (`OcrAll`, the twin of bulk embedding) — **EasyOCR (`vi`, Apache-2.0)** on the local CPU or a
  Kaggle GPU per `ocr.engine`, never inline. Do not reintroduce inline OCR, an OCR sidecar, figure
  extraction, or repair paths without a reviewed design. Gemma 4 E4B OCR enhancement is **MVP2, not
  current work**. The core text/OCR path stays permissive (MIT/Apache/BSD; no GPL/AGPL parsers). OCR text
  is never the sole source of binding legal text. See [`docs/design/EXTRACTION.md`](docs/design/EXTRACTION.md).
- Persist extraction provenance: engine, version, confidence, `source` kind, `verified` flag.
- Chunk by Điều with citation metadata. Every chunk carries its exact Điều/Khoản citation + a
  deterministic contextual prefix. Retrieval is **hybrid** — dense BGE-M3 vectors + **BM25 sparse vectors**
  (pgvector `sparsevec`, built by `cmd/lexindex`) fused with RRF + a query router — under a current-law
  pre-filter (`in_force` + `partial`). **The query-time embedder is required, not optional.** The lexical
  arm is native pgvector (no `pg_search` — unavailable on managed RDS); each hit returns both the dense
  similarity and the BM25 score.
- **Bulk embedding can offload to Kaggle GPU (optional).** `embed.engine` (`auto`/`local`/`kaggle`) picks
  only the **bulk/backfill** engine, never the query path — **query-time embedding always stays the local
  OVMS BGE-M3**. Run with `go run ./cmd/worker -embed-all [-force]`; auth is the single `KAGGLE_API_TOKEN`
  env var (owner auto-derived; no username). Batch-only; chunking stays deterministic in Go. See
  [`docs/design/RAG.md`](docs/design/RAG.md#kaggle-batch-embedding-optional-bulk-engine).
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
- Use `log/slog` with structured fields. In Temporal code use `workflow.GetLogger` / `activity.GetLogger`.
  Wrap Temporal activity errors so non-retryable failures fail fast. No `fmt.Print*` / `log.Print*`.
- Keep linear logic inline; extract helpers only when reused or independently testable. Define interfaces
  at the consumer. No `//nolint` without explicit approval.

## Containers (podman-first)

- All infrastructure and extraction engines run as OCI containers via podman / podman-compose / Quadlet.
  No host installs. Container build files are `Containerfile` (not `Dockerfile`).
- **Local dev stack:** the checked-in dev config points at the podman localhost stack. Agents may connect
  to the local DB/Temporal/Redis ports, the embedder, and the MCP server for verification, because dev is
  localhost by design. Agents may set the documented local `BANHMI_DATABASE_PASSWORD` env var when
  missing. Localhost ports, the dev DB user, and the dev DB name are not sensitive in summaries;
  non-localhost hosts and real deployment secrets remain sensitive.
- DOCX/HTML/PDF→Markdown conversion runs through local MarkItDown in the Go app container; OCR (EasyOCR,
  `vi`) runs as a batch on the local CPU or a Kaggle GPU. The **BGE-M3 embedder (OpenVINO) is required**:
  locally it runs as an **OVMS GPU container** for index + query embedding; on Cloud Run the query
  embedder is **in-process OpenVINO** in the MCP binary (`-tags openvino`) — no OVMS, no sidecar.
- Respect the host budget. The dev box (~8 GB RAM) already runs Postgres/Temporal/Redis/worker plus local
  extraction tools; don't stand up heavy services that OOM it.

## Verification

Use the narrowest check that proves the change, then broaden for shared or risky work.

```bash
make fmt          # format + import sorting
make generate     # after SQL changes (sqlc)
make migrate-gen  # after sql/**/schema.sql changes (Atlas diff → goose migration + atlas.sum)
go build ./...    # compile check; leaves no binaries
make test         # go test ./...
make lint         # golangci-lint + project linters
```

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

After committing, push to the remote so shared history stays current (sub-agents excepted — see
Sub-agents). When asked to commit, commit **directly on the current branch** (the maintainer works on
`master`); do not create a branch first unless asked.

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
  but sub-agents and workflow fan-outs top out at **Opus** — always use the **default Opus model** for
  them; never downshift tiers (Haiku/Sonnet) for orchestration work. The one deliberate exception is
  the Haiku-over-MCP stand-in agent in [Verification](#verification) — a small model there proves the
  MCP evidence contract works without model smarts.
- Give each sub-agent a **bounded scope, the docs to read, clear file ownership, and the current target**
  (so it never drifts from this guide). Tell it that it is not alone in the codebase and must not revert
  or overwrite unrelated changes.
- Sub-agents follow this guide and the same secret-handling rules. They must not commit, push, or rewrite
  history unless explicitly asked, and must report changed files, the verification they ran, and
  unresolved risks.
