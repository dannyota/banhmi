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
| [Publishing](PUBLISHING.md) | Listing the MCP servers in agent directories (ChatGPT apps et al.): URLs, assets, test cases |
| [Plan](../PLAN.md) | Roadmap, phases, decisions, progress |

## Design (`design/`)

| Doc | Topic |
|-----|-------|
| [SOURCES](design/SOURCES.md) | Scope, the keyword matcher, discovery design & per-source crawl/filter/download |
| [PIPELINE](design/PIPELINE.md) | The two data flows + pipeline stages (Discover · Fetch · Extract · Normalize · Index) |
| [SCHEMA](design/SCHEMA.md) | Data model — bronze/silver/gold/ingest, the relation graph, + the DB-seeded `config` schema |
| [EXTRACTION](design/EXTRACTION.md) | Deterministic DOCX/DOC/HTML/PDF extraction & per-file OCR gate |
| [RAG](design/RAG.md) | Chunking, hybrid retrieval, evidence, gaps, eval, and Kaggle bulk embedding |
| [WORKFLOW-EVAL](design/WORKFLOW-EVAL.md) | Agent-workflow eval harness: mcpcall bridge, multi-step golden set, transcript scorer |

## Jurisdictions (`design/jurisdictions/`)

One corpus per country off one codebase — registry, shared playbook, one design doc per country.

| Doc | Topic |
|-----|-------|
| [Registry](design/jurisdictions/README.md) | Country index: codename, domain, DB, language, sources, status |
| [PLAYBOOK](design/jurisdictions/PLAYBOOK.md) | The shared jurisdiction model: seam, language policy, safety invariants, phase template, deploy fan-out |
| [MALAYSIA](design/jurisdictions/MALAYSIA.md) | **LIVE** `laksa`: verified sources, PDF parser, MY build history |
| [INDONESIA](design/jurisdictions/INDONESIA.md) | **LIVE** `rendang` (revived 2026-07-12): BPK · BI · OJK sources, Pasal/Ayat parser |
| [SINGAPORE](design/jurisdictions/SINGAPORE.md) | **LIVE** `kaya` (2026-07-17): sso · mas · pdpc · csa sources, MY-family citations |
| [THAILAND](design/jurisdictions/THAILAND.md) | **LIVE** `tomyum` (2026-07-17): ocs · bot · etda · sec sources, Thai script + word segmentation |
| [CAMBODIA](design/jurisdictions/CAMBODIA.md) | **LIVE** `amok` (2026-07-18): nbc · serc · cdc · odc sources, English corpus, Khmer-only scans as gaps |

## Conventions

Follow the **Documentation** rules in [`CLAUDE.md`](../CLAUDE.md#documentation): concise and scannable,
one concern per doc, single source of truth, and linked from here so nothing is orphaned.
