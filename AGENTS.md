# Repository instructions

These instructions apply to every contributor and agent. A direct user instruction takes precedence.
If project documents conflict, stop dependent work, identify the conflict, and resolve the shared
documentation with the change.

## Read before work

Read [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) for the current system and [`PLAN.md`](PLAN.md)
for delivered and open work. Read the topic instructions that match the task:

| Work | Instructions |
|---|---|
| Scope, ownership, agents, or Git | [`instructions/workflow.md`](instructions/workflow.md) |
| Builds, tests, lint, evaluations, or CI | [`instructions/verification.md`](instructions/verification.md) |
| Go, SQL, schemas, migrations, or architecture | [`instructions/engineering.md`](instructions/engineering.md) |
| Sources, extraction, citations, validity, relations, or retrieval | [`instructions/corpus.md`](instructions/corpus.md) |
| Public or internal documentation | [`instructions/documentation.md`](instructions/documentation.md) |
| Secrets, credentials, data, or external systems | [`instructions/security.md`](instructions/security.md) |

Read the relevant design under [`docs/design/`](docs/design/) before changing its behavior. The
[`ASEAN Banking MCP redesign`](docs/design/ASEAN-BANKING-MCP.md) is a proposal. Do not describe its
components as current until implementation and hosted verification prove that status. The
[`repository cleanup plan`](docs/plans/repository-cleanup.md) governs the current cleanup.

## Product invariants

- banhmi builds evidence corpora for Southeast Asian banking, financial regulation, and related
  technology law. Data accuracy takes priority over feature breadth.
- banhmi serves evidence. It does not answer legal questions and contains no answer large language
  model. A user-owned model reasons over the evidence.
- The MCP contract has five tools: `guide`, `corpus_status`, `quality_gaps`, `search`, and `document`.
- One shared codebase serves separate jurisdiction corpora. Preserve each jurisdiction's source,
  language, parser, citation, validity, and evaluation behavior.
- Evidence includes exact native citations, validity, relations, provenance, confidence, and known
  gaps. Never hide weak or missing evidence behind confident text.
- Report implemented code as coded. Report corpus behavior as validated only after checks against real
  documents. Report deployed behavior only after checking the deployed service.

## Repository records

`AGENTS.md`, `CLAUDE.md`, `instructions/`, `docs/`, and `PLAN.md` are tracked project records. Keep
durable decisions in the repository instead of tool memory. Keep public files portable: use
repository-relative paths and configurable runtime locations. Do not publish workstation paths,
personal accounts, secret values, or references to unrelated repositories.
