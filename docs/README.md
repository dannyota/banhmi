# Documentation

Use this index to find the current public documentation for banhmi.

## Start here

| Document | Use |
|---|---|
| [Project README](../README.md) | Connect to a live corpus, review current metrics, or start self-hosting. |
| [Contributor instructions](../AGENTS.md) | Read project boundaries and find the topic-specific contributor rules. |
| [Architecture](ARCHITECTURE.md) | Understand the system design, data model, repository layout, and interfaces. |
| [Development and verification](DEVELOPMENT.md) | Use hosted checks and identify verification that still needs hosted infrastructure. |
| [Deployment](DEPLOYMENT.md) | Deploy the pipeline, PostgreSQL database, query embedder, and MCP service. |
| [Authenticated access runbook](../deploy/authenticated-access.md) | Configure and gate the undeployed Google account, website, and OAuth MCP source. |
| [Publishing](PUBLISHING.md) | Prepare the six jurisdiction endpoints for the OpenAI Plugins Directory. |
| [Roadmap](../PLAN.md) | Review deployed work, corpus baselines, open issues, and planned changes. |
| [Short-term work](../todo.md) | Find the next open slices and evidence holds; completed work lives in its owning plan. |
| [Repository cleanup plan](plans/repository-cleanup.md) | Track the current contributor-documentation and GitHub CI cleanup. |
| [Security CI plan](plans/security-ci.md) | Deliver separate Gitleaks secrets, Semgrep SAST, and Semgrep SCA checks. |
| [Google access implementation plan](plans/google-access.md) | Deliver Google login, website evidence search, and authenticated MCP access. |
| [Vietnam rebuild plan](plans/vietnam.md) | Milestones M0 to M6 for Vietnam on the new core, exit evidence, open owner decisions, and status. |
| [Chunking layout plan](plans/chunking-layout.md) | Pure token estimation and splitting relocation, hosted check evidence, and remaining corpus gates. |
| [Country modules implementation plan](plans/country-modules.md) | Map shared work to Vietnam M0-M3 and track the later disabled-country migration. |
| [Document conversion implementation plan](plans/document-conversion.md) | Relocate existing Go readers under the approved layout and track the remaining conversion gates. |
| [Goose migrations spec](specs/goose-migrations.md) | Reviewed contract for using Goose SQL as the only schema source and removing Atlas and `tools/migragen`. |
| [Goose migrations implementation plan](plans/goose-migrations.md) | Implementation owners, hosted evidence gates, and remaining work for migration-backed sqlc. |

## Contributor rules

| Document | Use |
|---|---|
| [Workflow](../instructions/workflow.md) | Roles, ownership, review, Git, and external actions. |
| [Verification](../instructions/verification.md) | Required evidence and hosted-check policy. |
| [Engineering](../instructions/engineering.md) | Go, SQL, migrations, generated code, and architecture boundaries. |
| [Corpus integrity](../instructions/corpus.md) | Sources, extraction, indexing, retrieval, evaluation, and publication safeguards. |
| [Documentation](../instructions/documentation.md) | Public writing, status, links, and portability. |
| [Security](../instructions/security.md) | Secrets, credentials, production data, and cloud access. |

## Operations references

| Document | Use |
|---|---|
| [AWS read-path runbook](../deploy/aws/setup-checklist.md) | Provision, deploy, verify, recover, and retire the reference AWS read path. |
| [AWS operator access](../deploy/aws/operator-access.md) | Use the AWS administrator operator through the CLI and AWS MCP under repository guardrails. |
| [Build dependency cache](../deploy/BUILD-CACHE.md) | Seed and use the private S3 cache for large container-build inputs. |

## Design documents

| Document | Topic |
|---|---|
| [Approved repository layout](design/repository-layout.md) | Target package homes, dependency rules, staged moves, and agent ownership for the Vietnam rebuild. Implementation remains staged. |
| [tracuuquydinh.vn website proposal](design/website.md) | Proposed Vietnam-only website, Vietnamese-only questions, ChatGPT plugin, and the public and signed-in pages. |
| [ASEAN Banking MCP redesign proposal](design/asean-banking-mcp.md) | Proposed one-domain service, subject organization, GitHub CI, account control, and VNGCloud transition. |
| [Approved corpus and workflow redesign](design/corpus-pipeline-redesign.md) | Schema revisions, bounded processing, Temporal ownership, immutable releases, read efficiency, and package boundaries. |
| [Corpus source implementation plan](plans/corpus-redesign-source.md) | Source ownership, review status, and deferred generation and execution gates. |
| [Corpus review delivery plan](plans/corpus-review.md) | Delivery order, ownership, hosted evidence, external gates, and unresolved owner decisions for the proposed review API. |
| [Sources](design/sources.md) | Scope rules and discovery, filter, and download behavior by source. |
| [Pipeline](design/pipeline.md) | Ingestion stages and the MCP serving flow. |
| [Schema](design/schema.md) | Bronze, Silver, Gold, ingest, and configuration data models. |
| [Extraction](design/extraction.md) | Deterministic file extraction, content gates, and OCR fallback. |
| [Retrieval](design/rag.md) | Chunking, hybrid retrieval, evidence output, evaluation, and bulk embedding. |
| [Workflow evaluation](design/workflow-eval.md) | Multi-step agent evaluation and transcript scoring. |
| [Authentication](design/AUTH.md) | Google registration, website sessions, MCP access, blocking, and privacy. |
| [OAuth storage](design/OAUTH-STORAGE.md) | Fosite persistence, transaction boundaries, resource binding, and replay handling. |

## Proposed specs

These specs remain proposals except for decisions explicitly approved by the owner. Each spec and
paired plan records implementation and evidence status. VNGCloud placement for the new Vietnam system is approved; see
[product boundaries](../AGENTS.md#product-and-evidence-boundaries).

| Spec | Topic |
|---|---|
| [Security CI](specs/security-ci.md) | Required security scans, scan-token access, failure handling, and hosted evidence. |
| [Corpus source contracts](specs/corpus-redesign-source.md) | Fixed contracts from the reviewed corpus source pass; its paired plan owns remaining generation, persistence, and execution evidence. |
| [Corpus write path](specs/corpus-write-path.md) | Stage contracts from discovery to a promoted release, the VNGCloud writer, delivery, failure policy, and rollout. |
| [Corpus workflows](specs/corpus-workflows.md) | Temporal topology: `SourceSync`, per-document `DocumentIngest`, quarantine, `OcrBatch`, `EmbedBatch`, `CorpusBuild`, `Promote`, `Reprocess`. |
| [Chunking helper layout](specs/chunking-layout.md) | Existing rune estimates and text-splitting behavior in the shared chunking package. |
| [Document conversion](specs/document-conversion.md) | Turning DOCX, DOC, HTML, PDF, scans, and spreadsheets into a source-linked block model, quality gates, sandboxing, and reprocessing. |
| [Document parts](specs/document-parts.md) | Phụ lục, Biểu mẫu, and Mẫu số as citable parts with their own validity, files, and display. |
| [Corpus review](specs/corpus-review.md) | Review items, authority, decisions, acceptance requirements, and review API behavior behind mutual TLS. |
| [Corpus database](specs/corpus-database.md) | Approved fresh-chain boundary; proposed writer, release, and control schemas, migrator guards, sqlc and test homes, M1 persistence dependencies, evidence indexes, and capacity. Not implemented. |
| [Embedding models](specs/embedding-models.md) | Model bake-off, self-hosted query runtime, storage, re-embedding, and retrieval without a reranker. |
| [VNGCloud hosting](specs/vngcloud-hosting.md) | Vietnam hosting: serving, separate writer capacity, PostgreSQL, edge, releases, vStorage, backups, and cost gates. |
| [Country modules](specs/country-modules.md) | Country-agnostic core versus per-country modules, interfaces, registry states, v1 reuse map, and enablement gates. O1-O3 are approved; the inventory-only foundation is merged, and M0 remains in progress. |
| [Vietnam](specs/vietnam.md) | Vietnam module: sources, numbering, structure, amendments and validity, consolidated texts, language, and evaluation. O4-O6 are approved but unimplemented. |

## Jurisdictions

All six live services use one codebase and a separate database per country. The registry and shared
playbook define the common model; each country guide records its sources, language, parser, and known
gaps.

| Document | Jurisdiction or purpose |
|---|---|
| [Registry](design/jurisdictions/README.md) | Country names, codenames, domains, databases, languages, sources, and status. |
| [Playbook](design/jurisdictions/PLAYBOOK.md) | Shared language policy, safety rules, country seam, and deployment fan-out. |
| [Malaysia](design/jurisdictions/MALAYSIA.md) | `laksa`: AGC, BNM, and Securities Commission sources. |
| [Indonesia](design/jurisdictions/INDONESIA.md) | `rendang`: BPK, Bank Indonesia, and OJK sources. |
| [Singapore](design/jurisdictions/SINGAPORE.md) | `kaya`: SSO, MAS, PDPC, and CSA sources. |
| [Thailand](design/jurisdictions/THAILAND.md) | `tomyum`: OCS, BOT, SEC, and ETDA sources. |
| [Cambodia](design/jurisdictions/CAMBODIA.md) | `amok`: NBC, SERC, CDC, and ODC sources. |
