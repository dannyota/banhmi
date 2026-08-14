# Documentation

Use this index to find the current public documentation for banhmi.

## Start here

| Document | Use |
|---|---|
| [Project README](../README.md) | Connect to a live corpus, review current metrics, or start self-hosting. |
| [Architecture](ARCHITECTURE.md) | Understand the system design, data model, repository layout, and interfaces. |
| [Local development](DEVELOPMENT.md) | Configure the local stack, build a corpus, run the MCP server, and test changes. |
| [Deployment](DEPLOYMENT.md) | Deploy the pipeline, PostgreSQL database, query embedder, and MCP service. |
| [Publishing](PUBLISHING.md) | Prepare the six jurisdiction endpoints for the OpenAI Plugins Directory. |
| [Roadmap](../PLAN.md) | Review deployed work, corpus baselines, open issues, and planned changes. |

## Operations references

| Document | Use |
|---|---|
| [AWS read-path runbook](../deploy/aws/setup-checklist.md) | Provision, deploy, verify, recover, and retire the reference AWS read path. |
| [Build dependency cache](../deploy/BUILD-CACHE.md) | Seed and use the private S3 cache for large container-build inputs. |

## Design documents

| Document | Topic |
|---|---|
| [Sources](design/SOURCES.md) | Scope rules and discovery, filter, and download behavior by source. |
| [Pipeline](design/PIPELINE.md) | Ingestion stages and the MCP serving flow. |
| [Schema](design/SCHEMA.md) | Bronze, Silver, Gold, ingest, and configuration data models. |
| [Extraction](design/EXTRACTION.md) | Deterministic file extraction, content gates, and OCR fallback. |
| [Retrieval](design/RAG.md) | Chunking, hybrid retrieval, evidence output, evaluation, and bulk embedding. |
| [Workflow evaluation](design/WORKFLOW-EVAL.md) | Multi-step agent evaluation and transcript scoring. |
| [Authentication](design/AUTH.md) | Planned MCP authentication, blocking, privacy, and audit design. |

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
