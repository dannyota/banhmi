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
| [Publishing](PUBLISHING.md) | Prepare the six jurisdiction endpoints for the OpenAI Plugins Directory. |
| [Roadmap](../PLAN.md) | Review deployed work, corpus baselines, open issues, and planned changes. |
| [Repository cleanup plan](plans/repository-cleanup.md) | Track the current contributor-documentation and GitHub CI cleanup. |
| [Google access implementation plan](plans/google-access.md) | Deliver Google login, website evidence search, and authenticated MCP access. |

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
| [AWS operator access](../deploy/aws/operator-access.md) | Provision and use the scoped AWS MCP operator without exposing credentials. |
| [Build dependency cache](../deploy/BUILD-CACHE.md) | Seed and use the private S3 cache for large container-build inputs. |

## Design documents

| Document | Topic |
|---|---|
| [ASEAN Banking MCP redesign proposal](design/ASEAN-BANKING-MCP.md) | Proposed one-domain service, subject organization, GitHub CI, account control, and VNGCloud transition. |
| [Sources](design/SOURCES.md) | Scope rules and discovery, filter, and download behavior by source. |
| [Pipeline](design/PIPELINE.md) | Ingestion stages and the MCP serving flow. |
| [Schema](design/SCHEMA.md) | Bronze, Silver, Gold, ingest, and configuration data models. |
| [Extraction](design/EXTRACTION.md) | Deterministic file extraction, content gates, and OCR fallback. |
| [Retrieval](design/RAG.md) | Chunking, hybrid retrieval, evidence output, evaluation, and bulk embedding. |
| [Workflow evaluation](design/WORKFLOW-EVAL.md) | Multi-step agent evaluation and transcript scoring. |
| [Authentication](design/AUTH.md) | Google registration, website sessions, MCP access, blocking, and privacy. |
| [OAuth storage](design/OAUTH-STORAGE.md) | Fosite persistence, transaction boundaries, resource binding, and replay handling. |

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
