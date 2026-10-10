<div align="center">

# banhmi

**A citable regulatory corpus and Model Context Protocol (MCP) server for Southeast Asian banking, finance, and technology law.**

[Documentation](docs/README.md) · [Architecture](docs/ARCHITECTURE.md) · [Roadmap](PLAN.md)

[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![MCP](https://img.shields.io/badge/MCP-Streamable_HTTP-6E40C9)](https://modelcontextprotocol.io)

</div>

---

banhmi collects legal documents from government, regulator, and supplementary legal-data sources.
It extracts their text and serves ranked evidence over MCP. Results include native citations,
validity, amendment relations, provenance, source links, and known coverage gaps.

banhmi does not answer legal questions. A connected agent retrieves the evidence and forms the
answer.

## Connect to a live corpus

The public endpoints use MCP over Streamable HTTP. They require no signup or API key.

Those statements describe the current AWS deployment verified on 2026-08-14. The repository now
contains a later authenticated Vietnam server composition, but that source has not been deployed or
verified with Google or an MCP client. See the
[authenticated access runbook](deploy/authenticated-access.md) for its pending release gates.

| Jurisdiction | Endpoint | Search language | Sources |
|---|---|---|---|
| Vietnam | `https://banhmi.danny.vn/mcp` | Vietnamese | VBPL, Cong Bao, government document portal, SBV Region 1 portal |
| Malaysia | `https://laksa.danny.vn/mcp` | English | Attorney General's Chambers, Bank Negara Malaysia, Securities Commission Malaysia |
| Indonesia | `https://rendang.danny.vn/mcp` | Indonesian | BPK JDIH, Bank Indonesia, OJK |
| Singapore | `https://kaya.danny.vn/mcp` | English | Singapore Statutes Online, MAS, PDPC, CSA |
| Thailand | `https://tomyum.danny.vn/mcp` | Thai | Office of the Council of State, Bank of Thailand, SEC, ETDA |
| Cambodia | `https://amok.danny.vn/mcp` | English | NBC, SERC, CDC, Open Development Cambodia |

Add one endpoint to an MCP client that supports Streamable HTTP. Search in the language shown above.
The connected agent can translate the question before searching and translate the retrieved
evidence afterward.

The server exposes five tools:

| Tool | Use |
|---|---|
| `guide` | Read the jurisdiction-specific usage and citation guidance. |
| `corpus_status` | Check live document, source, embedding, relation, and gap counts. |
| `search` | Find ranked provisions with citations, validity, relations, and source links. |
| `document` | Read a full provision and its provenance, relations, and amendment context. |
| `quality_gaps` | Inspect specific corpus-quality worklists before relying on coverage. |

For example, ask: *What are the technology risk management requirements for banks?*

## What it provides

- Scope-filtered discovery for banking and financial regulation plus related technology law,
  including cybersecurity, data protection, artificial intelligence, cloud, electronic
  transactions, payments, and digital banking.
- Text extracted from each cited source without paraphrasing. Each result links back to its source so
  the wording can be checked.
- Native provision structure, such as Vietnamese Điều/Khoản, Indonesian Pasal/ayat, and
  Section/Subsection for Malaysia and Singapore.
- Effective dates, current-law filtering, recorded amendment and repeal relations, provenance, and
  explicit data gaps.
- Hybrid retrieval over dense Qwen3 embeddings and BM25 sparse vectors, fused with reciprocal rank
  fusion (RRF).

Each country has its own corpus and database. Sources and language rules are selected by
configuration, not by maintaining country-specific forks. See the
[source design](docs/design/SOURCES.md) and [jurisdiction registry](docs/design/jurisdictions/README.md).

## Architecture

```mermaid
flowchart TB
  subgraph WRITE["Write path: cmd/pipeline"]
    DISC["Discover"] --> FETCH["Fetch official files"]
    FETCH --> EXT["Extract"]
    EXT --> NORM["Normalize structure, validity, relations"]
    NORM --> IDX["Chunk and index"]
  end

  GPU["Kaggle T4 GPU<br/>Qwen3 bulk embedding"]
  IDX <-->|"dataset I/O"| GPU

  DB[("AWS RDS PostgreSQL 17 + pgvector<br/>one database per country")]
  IDX -->|"write over TLS"| DB

  subgraph READ["Read path: AWS ECS on EC2"]
    MCP["One slim MCP service<br/>routes all six jurisdictions"]
    EMB["Shared cmd/embedder service<br/>Qwen3 ONNX FP16"]
    MCP -->|"query text"| EMB
  end

  DB -->|"corpus read"| MCP
  CF["CloudFront<br/>six HTTPS domains"] --> MCP
  USERS["User-owned agents"] -->|"MCP over Streamable HTTP"| CF
```

The ingestion pipeline uses three data layers:

1. **Bronze:** discover and fetch source records and files.
2. **Silver:** extract text and normalize structure, validity, and relations.
3. **Gold:** create provision-aware chunks, dense embeddings, and BM25 sparse vectors.

Production runs one slim MCP service for all six jurisdictions and one shared query-embedding
service. PostgreSQL remains the jurisdiction boundary: each country has a separate database. See
[Architecture](docs/ARCHITECTURE.md) and [Deployment](docs/DEPLOYMENT.md).

## Corpus and retrieval status

These corpus sizes were verified in production on 2026-08-14. The evaluation metrics are accepted
offline baselines recorded from 2026-07-25 through 2026-07-27. Call `corpus_status` for current live
counts.

| Jurisdiction | Chunks | Golden cases | Recall@8 | MRR@8 | Current-law precision | Abstention |
|---|---:|---:|---:|---:|---:|---:|
| Vietnam | 142,086 | 92 | 93.8% | 73.3% | 100% | 100% |
| Malaysia | 11,304 | 72 | 94.3% | 79.6% | 100% | 100% |
| Indonesia | 160,142 | 110 | 79.8% | 62.4% | 100% | 100% |
| Singapore | 27,951 | 76 | 93.5% | 76.9% | 100% | 98.7% |
| Thailand | 32,715 | 58 | 89.5% | 69.6% | 100% | 96.6% |
| Cambodia | 7,757 | 42 | 94.4% | 72.7% | 100% | 100% |

Recall@8 measures whether expected citations appear in the first eight results. Current-law
precision measures whether returned primary hits are in force. Abstention measures whether the
retriever flags questions that lack usable in-scope evidence. See the
[evaluation design](docs/design/RAG.md#retrieval-evaluation) and [roadmap](PLAN.md) for details and
known gaps.

## Contribute and self-host

Read [Development and verification](docs/DEVELOPMENT.md) for the hosted contribution workflow,
initial GitHub checks, and checks that still need hosted infrastructure. The
[repository cleanup plan](docs/plans/repository-cleanup.md) records the current documentation and CI
work.

Self-hosting requires PostgreSQL 17 with pgvector, the Go services, and a matching
Qwen3-Embedding-0.6B query embedder. Building a corpus can also require object storage, bulk
embedding, OCR, and source-specific network access. [Deployment](docs/DEPLOYMENT.md) defines the
portable runtime contract and reference deployment.

## Documentation

- [Documentation index](docs/README.md)
- [Architecture](docs/ARCHITECTURE.md)
- [Development and verification](docs/DEVELOPMENT.md)
- [Contributor instructions](AGENTS.md)
- [Current repository cleanup plan](docs/plans/repository-cleanup.md)
- [Deployment](docs/DEPLOYMENT.md)
- [Publishing](docs/PUBLISHING.md)
- [Roadmap and current status](PLAN.md)
- [ASEAN Banking MCP redesign proposal](docs/design/ASEAN-BANKING-MCP.md)
- [Source, pipeline, schema, extraction, retrieval, and workflow evaluation designs](docs/README.md#design-documents)
- [Jurisdiction registry and country guides](docs/design/jurisdictions/README.md)

## License

[Apache 2.0](LICENSE).
