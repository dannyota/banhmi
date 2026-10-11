# Jurisdiction playbook

This playbook defines the shared model for adding and operating a country corpus. It grew from the
first expansion, [Malaysia](MALAYSIA.md). The [jurisdiction registry](README.md) lists the current
countries; each country doc records only its local source, language, parser, and citation decisions.

## Shared model

- **One codebase, one corpus per country.** A jurisdiction is configuration, never a branch or fork.
- **One database per country.** The database is the data boundary. Tables have no `jurisdiction`
  column, and one country cannot query another country's rows through its database pool.
- **One pipeline jurisdiction per run.** `BANHMI_JURISDICTION` selects sources, scope vocabulary,
  parser, citations, and the default database. It defaults to `vn`.
- **Single-site or multi-site MCP.** `cmd/server` can serve one country or the comma-separated
  `BANHMI_JURISDICTIONS` set. A multi-site process opens one database pool per country and shares one
  query embedder. Each CloudFront distribution injects `X-Banhmi-Jurisdiction`; an unknown explicit
  value returns 404 instead of falling back to the wrong corpus.
- **One topical scope.** Every corpus covers banking and financial regulation plus technology law
  that affects banking or finance: AI, data protection, cybersecurity, cloud and outsourcing,
  electronic transactions and signatures, digital banking and payments, e-KYC, system risk, and
  technology operations.

Production currently uses six CloudFront distributions, one slim MCP container for all six
jurisdictions, one shared `cmd/embedder` service on loopback, and one RDS instance with six databases.
This is an operating choice, not a platform requirement: the same image also supports separate
single-country deployments.

## Corpus-language policy

Each corpus has one search and text language:

| Country | Language | Source-text rule |
|---|---|---|
| Vietnam | Vietnamese | Index the authoritative Vietnamese text. |
| Malaysia | English | Select the AGC `BI` English edition; do not index the BM edition. |
| Indonesia | Indonesian | Do not index English renditions from OJK or BI. |
| Singapore | English | Index the official English text. |
| Thailand | Thai | Do not index non-binding English translations. |
| Cambodia | English | Index official-source English translations; the country doc records that Khmer remains the binding language. |

banhmi does not translate stored legal text. A connecting model may translate a question before
search and translate retrieved evidence for the user, but the corpus keeps the selected source text
verbatim. Cambodia is the documented exception to the usual binding-language choice. The
[AGENTS.md language requirement](../../../AGENTS.md#product-and-evidence-boundaries) makes Vietnam
an approved, not deployed exception to the translation allowance: questions must be Vietnamese,
and clients must not translate them before search.

## Shared and country-specific parts

| Layer | Shared | Country-specific |
|---|---|---|
| Sources | `ingest.Source`; discovery, fetch, and drain stages | Source packages and the source builder in `pkg/app` |
| Structure | `[]Section`, normalization, chunk walking | Parser selected by `Descriptor.StructureParser` |
| Citations | Gold chunk storage and retrieval | Provision kinds, labels, article and sub-article prefixes |
| Scope | `pkg/scope` matcher | `deploy/seed/scope_term*.csv` and any safe structural or issuer rule |
| Language | Extraction gates and OCR runners | OCR language, mojibake checks, and lexical normalizer |
| Retrieval | Dense plus BM25 retrieval, reciprocal rank fusion, evidence assembly | Router and corpus tuning; `deploy/eval/golden*.json` |
| MCP | Five tools and shared response mechanics | Brief, guide, tool text, source names, and reply-language rule |
| Deploy | Same images and database schema | Database, domain, storage, proxies, and source credentials |

## Registry seam

`pkg/base/jurisdiction` is the descriptor registry. `pkg/app` separately maps every registered code to
its source builder, and `pkg/mcp` maps it to a brief. `TestAllComplete` and
`TestSourceBuildersCoverRegistry` guard those seams.

| Code | Database | OCR language | Parser | Long-leaf label | Scope seed | Golden set |
|---|---|---|---|---|---|---|
| `vn` | `banhmi` | config default `vi` | `vn-markdown` | `Đoạn` | `scope_term.csv` | `golden.json` |
| `my` | `laksa` | `en` | `my-act` | `Paragraph` | `scope_term_my.csv` | `golden_my.json` |
| `id` | `rendang` | `id` | `id-uu` | `Alinea` | `scope_term_id.csv` | `golden_id.json` |
| `sg` | `kaya` | `en` | `sg-act` | `Paragraph` | `scope_term_sg.csv` | `golden_sg.json` |
| `th` | `tomyum` | `th` | `th-act` | `วรรค` | `scope_term_th.csv` | `golden_th.json` |
| `kh` | `amok` | `en` | `kh-act` | `Paragraph` | `scope_term_kh.csv` | `golden_kh.json` |

The descriptor also owns validity defaults, citation prefixes, text normalization, retrieval tuning,
reference canonicalization, and relation-backfill allowlists. Empty configuration resolves to `vn`.
Startup composition uses `Lookup` and rejects an unknown non-empty code; internal fallback sites use
`For` where a descriptor must always exist.

## Live-corpus safety

1. Treat all six registry entries as live. Run the affected parser, citation, source-wiring, and
   golden-set tests before changing shared behavior.
2. Do not rebuild chunks or embeddings as a side effect of a code-only deploy. A citation or chunking
   change requires an explicit corpus rebuild and before-and-after evaluation.
3. Keep `vn` as the empty-configuration fallback. Reject an explicit unknown jurisdiction before
   opening sources or serving requests.
4. Keep schema changes compatible across every country database. Apply and verify migrations per
   database before deploying code that depends on them.
5. Preserve partial discovery results but do not advance a source cursor when any required slice
   fails. A retry must be safe and idempotent.

## Build sequence

1. **Verify sources.** Prove listing discovery, per-document metadata, legal text download, validity
   and relation signals, robots or terms constraints, bot protection, and geo-routing. Record the
   date, client requirement, and known gaps.
2. **Prove the parser.** Run the production extractor on a representative flagship law. Compare the
   complete article or section inventory and check for gaps, duplicates, table-of-contents capture,
   page furniture, schedules, and scanned files.
3. **Add the descriptor and seeds.** Register the country, scope vocabulary, OCR language, lexical
   normalizer, validity and relation mappings, abbreviation data, and golden-set path.
4. **Add source packages.** Implement one `pkg/ingest/<source>` package per fetch contract. Prefer
   source sweeps plus the shared scope matcher. Use structural or issuer admission only when the
   source boundary makes it safe.
5. **Normalize and validate.** Wire the parser, canonical document identity, validity, relations,
   source priority, and file selection. Test cross-source deduplication with real number formats.
6. **Index and evaluate.** Render native citations, build dense and sparse vectors, and run the
   country golden set. Inspect coverage and ranking failures separately.
7. **Deploy.** Create and migrate the country database, seed it, run the single-country pipeline,
   bulk-embed, configure the MCP site and CloudFront jurisdiction header, then verify
   `corpus_status`, `search`, `document`, and the public `/mcp` endpoint.

## Current production wiring

| Part | Shape |
|---|---|
| Pipeline | Local, one country per run via `BANHMI_JURISDICTION`; Kaggle handles bulk embedding. |
| Database | One RDS PostgreSQL 17 instance in `ap-southeast-1`; one database per country. |
| MCP | One `cmd/server` container with `BANHMI_JURISDICTIONS=vn,my,id,sg,th,kh`. |
| Query embedding | One shared `cmd/embedder` service at `http://127.0.0.1:8089`. |
| Routing | One CloudFront distribution per domain injects `X-Banhmi-Jurisdiction`. |

The common database settings apply to every site. `BANHMI_DATABASE_NAME_<CC>` can override one
country's registered database name. Source-specific proxies remain pipeline settings, such as
`BANHMI_OJK_PROXY_URL`. See [`DEPLOYMENT.md`](../../DEPLOYMENT.md) for the platform-neutral contract.
