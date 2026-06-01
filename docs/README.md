# banhmi documentation

Index of all docs — keep current when adding, removing, or renaming (documentation rules live in
[`CLAUDE.md`](../CLAUDE.md#documentation)).

## Start here

| Document | Audience |
|----------|----------|
| [Agent guide](../CLAUDE.md) | **Canonical** working agreement, conventions, and the current target |
| [Architecture](ARCHITECTURE.md) | System design, data model, folder layout, interfaces |
| [Local development](DEVELOPMENT.md) | Setup: dev stack, migrations, seed, build/run/test, everyday commands |
| [Deployment](DEPLOYMENT.md) | Generic 3-part deploy (worker · database · MCP) — bring your own stack |
| [Plan](../PLAN.md) | Roadmap, phases, decisions, progress |

## Design (`design/`)

| Doc | Topic |
|-----|-------|
| [SOURCES](design/SOURCES.md) | Scope, the keyword matcher, discovery design & per-source crawl/filter/download |
| [PIPELINE](design/PIPELINE.md) | The two data flows + Temporal workflows (Discover · Fetch · Extract · Normalize · Index) |
| [SCHEMA](design/SCHEMA.md) | Data model — bronze/silver/gold/ingest, the relation graph, + the DB-seeded `config` schema |
| [EXTRACTION](design/EXTRACTION.md) | Deterministic DOCX/DOC/HTML/PDF extraction & per-file OCR gate |
| [RAG](design/RAG.md) | Chunking, vector retrieval, evidence, gaps, eval, and optional Kaggle GPU bulk embedding |

## Conventions

Follow the **Documentation** rules in [`CLAUDE.md`](../CLAUDE.md#documentation): concise and scannable,
one concern per doc, single source of truth, and linked from here so nothing is orphaned.
