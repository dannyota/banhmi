# Repository rules

## Authority and reading order

`AGENTS.md` is the single current rule authority. Topic files add task-specific safeguards.
Direct owner instructions take precedence. If project documents conflict, stop dependent work and
resolve the shared documentation with the change.
Read the [documentation index](docs/README.md), [architecture](docs/ARCHITECTURE.md), the applicable
topic rules below, and the spec or design for the task. Read only the [delivery plan](PLAN.md)
sections the task touches. Designs are proposals unless labeled approved or implemented.
Keep durable decisions in tracked repository records, not tool memory.
Read [short-term work](todo.md) for the current execution queue. The owning plans retain scope,
milestone acceptance, evidence, and open decisions.

## Product and evidence boundaries

- The next product is **Tra cứu quy định** at `tracuuquydinh.vn`, the approved, unbuilt direction in
  [Website](docs/design/website.md). banhmi remains the engine, repository, and corpus name.
- Develop Vietnam first. Maintain Malaysia, Singapore, Indonesia, Thailand, and Cambodia unchanged
  on their existing `danny.vn` hostnames. Designs must leave room for subjects and other ASEAN countries.
- VNGCloud in Vietnam is the approved, unbuilt target for the new Vietnam system: serving,
  corpus writer and workers, review services, and artifact and backup storage. Keep writer and
  worker capacity separate from the serving host. Existing AWS services remain deployed until
  the Vietnam cutover; the five other country services stay unchanged. Follow the
  [hosting decision](docs/specs/corpus-write-path.md#decisions).
- Serve evidence only. Our servers run no answer model, generative model, or reranker model.
  The query embedder is the only served model and runs on our own host. The user's own AI reads
  and judges evidence through the ChatGPT plugin or through `/mcp` in Claude and other clients.
- **Decision:** Reject a platform-run model and a reranker.
  **Why:** Sending user questions abroad is a cross-border transfer under Luật Bảo vệ dữ liệu cá nhân
  91/2025/QH15, Điều 20. A reranker adds cost and latency with no gain over the user's own model.
  **Rejected:** Third-party question processing and query-time reranking.
  **Detail:** Public legal source documents may use approved cloud batch OCR and bulk embedding.
- Vietnam search accepts Vietnamese text with diacritics or a document number such as
  `50/2024/TT-NHNN`. English is for interface text only. This requirement is approved, not deployed;
  the deployed MCP brief still says to translate first.
- Keep five MCP tools: `guide`, `corpus_status`, `quality_gaps`, `search`, and `document`.
- Keep a separate corpus and database per jurisdiction on one shared codebase. Preserve each
  jurisdiction's sources, language, parser, citations, validity, and evaluations. Evidence retains exact
  native citations, validity, relations, provenance, confidence, and known gaps. Accuracy takes priority
  over breadth. Never hide weak or missing evidence.
- Legacy and new corpus code are separate. Legacy is the v1 corpus code that serves the five
  maintained countries and the current Vietnam service: `pkg/{app,ingest,extract,pipeline}`,
  `pkg/base/{jurisdiction,sourceurl}`, the migration chains under `deploy/migrations/`, the query
  sources under `sql/{bronze,silver,gold,ingest,config}`, and their generated stores under
  `pkg/store/{bronze,silver,gold,ingest,config}`. Auth code, its chain, and `pkg/store/auth` are not
  legacy. Legacy is frozen: no new migrations, no new features, and no behavior change without
  an owner-approved maintenance fix. Legacy tests stay and run until the slice that retires their
  package deletes both together. New Vietnam code follows the
  [approved layout](docs/design/repository-layout.md).
- **Decision:** Build the new Vietnam corpus schema on fresh migration chains and freeze the legacy
  chains.
  **Why:** The Vietnam writer is a new database with no data to migrate. Appending to the legacy
  chains would carry their constraints, unused tables, and compatibility columns into a database
  that never held them.
  **Rejected:** Additive migrations on the legacy five-schema chains and a nullable legacy `build_id`.
  **Detail:** The [corpus database spec](docs/specs/corpus-database.md) names the chains, query
  sources, and generated-store paths. The [engineering rules](instructions/engineering.md#data-and-generated-code)
  govern chain mutability and hosted generation.
- Report implemented code as coded. Report corpus behavior as validated only after checks against
  real documents. Report deployed behavior only after checking the deployed service.

## Roles and dispatch

| Work | Runner and definition | Model | Effort |
|---|---|---|---|
| Design and research | Claude subagent `.claude/agents/designer.md` | Claude Fable | high |
| Design improvement after Fable, or design when Fable is unavailable | Codex worker | `gpt-6-astra` | high |
| Planning and implementation | Codex `.codex/agents/implementer.toml` | `gpt-6.1-sol` | medium |
| Design and plan review | Codex `.codex/agents/design-reviewer.toml` | `gpt-6.1-sol` | xhigh |
| Code review, adversarial review, fix confirmation | Codex `.codex/agents/code-reviewer.toml` | `gpt-6.1-sol` | xhigh |
| Troubleshooting, flaky tests, root-cause debugging, fix included | Codex `.codex/agents/troubleshooter.toml` | `gpt-6.1-sol` | xhigh |
| Exploration and mechanical work | Codex worker | `gpt-6-luna` | low |

- The primary is the Claude Code session. It plans, briefs workers, fetches evidence they cannot
  reach (CI logs, artifacts, cloud reads), verifies results, lands changes, and reports. It does not
  investigate, debug, implement code, or review work; it may make documentation-only edits and
  mechanical edits a worker sandbox cannot make. The owner chooses its model.
- Design flow: Fable writes the design, then `gpt-6-astra` at high reviews and improves it in
  place; when Fable is unavailable, Astra writes it. `gpt-6.1-sol` at xhigh then reviews the result.
- `gpt-6-astra` is for design only. Every review and fix confirmation runs on `gpt-6.1-sol` at
  xhigh, including review of Sol-written work, in a separate job from the author.
- Troubleshooter fixes get the same independent review as any other code change.
- Keep local RAM and CPU low. Run at most four Codex jobs at once, poll job status no more often
  than every 90 seconds, and stop Codex app-servers whose job or worktree is gone.
- Delegate Claude design and research only, on Fable. Never delegate to Sonnet or Opus. Map a skill's
  Sonnet or Opus delegation to `gpt-6.1-sol` at medium effort. Set model and effort on every dispatch.
  Select only model identifiers available in the current session.
- A reviewer is always a separate job from the author, never implements what it reviews, and runs
  read-only. A worker's own review never counts. Wait if the reviewer is unavailable. Keep design,
  implementation, and review ownership separate for each slice.
- A slice is one pull request that one reviewer reads in one pass: one contract, package group, or
  document set with its tests. A milestone is an acceptance gate made of slices, not a slice. Start
  a milestone's first slice when its inputs exist; unrelated polish in the same milestone does not
  block it. Resolve an owner decision in the slice that consumes it. Follow
  [slice rules](instructions/workflow.md#slices).
- Worker briefs name objective, authorities, constraints, owned paths, forbidden actions, required
  checks, and report contract. Workers never commit, push, deploy, or change cloud resources.
- The primary inspects changes and evidence before accepting a worker report.

## Git

- Use `git commit -s` for every commit with the owner's GPG signing configuration. Never disable
  signing. If signing fails, stop and report.
- Use Conventional Commits with an imperative subject under 72 characters and a body explaining why.
  No AI attribution, `Co-authored-by`, or generated-by lines.
- Stage explicit paths only. Never use `git add -A`, `git add .`, or `git commit -a`.
- The primary may commit finished work locally without asking again. Push only when the owner asks
  in the current conversation, after showing the commits. Never force-push or rewrite pushed history.
  Run any rebase in a separate worktree. Preserve unrelated changes.
- Give each Codex implementation slice its own branch and worktree under `.worktrees/`.
  Every change, including primary documentation edits, reaches `master` through a pull request.
  The `master` ruleset requires the `CI` check on an up-to-date branch, signed commits, and merge
  commits; it blocks force-push and deletion.
- Track `AGENTS.md`, `CLAUDE.md`, `instructions/`, `docs/`, `PLAN.md`, `todo.md`, and the agent definitions
  in `.claude/agents/` and `.codex/agents/`.
  Preserve tracked files and repo-local exceptions. Use scoped ignore negations for named records.
  Do not newly track other globally ignored files without explicit repository instructions or approval.
- Never replace or reconfigure the global ignore file or `core.excludesFile`. Add global entries only
  when explicitly requested. Tracking exceptions never authorize secrets.

## Verification and review

GitHub runs every build, test, lint, format check, generation, and evaluation. Local work is reading,
editing, diff inspection, and static review only. Do not install dependencies or run the app locally.
Use the [hosted check matrix and evidence gates](instructions/verification.md). Required checks gate
merges only. Real-document, deployment, and release evidence lanes are separate workflows that gate
milestones. Generated Go enters the repository only through the hosted generation artifact that the
primary reviews and imports. Never delete, skip, or weaken a test to pass a check.
The primary reads the exact commit's CI result before merge. Missing, skipped, or failing required
checks block merge. Mutation paths, corpus integrity, credentials, auth, OAuth, keys, signing,
enforcement, production writes, deployments, and release workflows need independent adversarial
review before merge.

## Documentation

- State current facts and approved requirements. Label unimplemented requirements. No editorial
  dates, change history, or retrospectives; Git holds history. Keep legal citations and effective dates.
- Use plain short sentences, active voice, and one term per concept. No em dashes. Lead with the point.
  Use prose for connected reasoning and lists or tables when they aid scanning.
- Use lowercase kebab-case file names except root rule files and `README.md` indexes.
  Designs and specs are at most 400 lines; split by concern.
- Pair specs and plans as `docs/specs/<name>.md` and `docs/plans/<name>.md`. A spec may instead
  name one existing plan as its owner in its status line, and a spec split by concern may continue
  in `docs/specs/<name>-<concern>.md` under the same owner. Never create a placeholder plan.
  Decisions use **Decision**, **Why**, **Rejected**, and **Detail** fields.
- Create a directory only with its first real file. Existing document renames and moves belong to a
  later slice. Link current paths now; do not create placeholders.
- Keep public files portable with repository-relative paths and configurable runtime locations.
  Do not publish workstation paths, personal accounts, private hostnames, secret values, or unrelated
  repository references. Keep one concern per document and label parked and open states precisely.
- Keep implementation details out of user-facing docs unless users need them to decide or operate.
  Use Mermaid in committed Markdown and plain text diagrams in transient conversation.

## Topic rules

| File | Scope |
|---|---|
| [Workflow](instructions/workflow.md) | Briefs, handoffs, ownership, findings, completion |
| [Verification](instructions/verification.md) | Hosted checks, evidence, isolated inputs, missing runs |
| [Engineering](instructions/engineering.md) | Go, SQL, migrations, generation, package boundaries |
| [Corpus](instructions/corpus.md) | Sources, extraction, citations, validity, relations, retrieval |
| [Documentation](instructions/documentation.md) | Fact ownership, indexes, pairing, links, maintenance |
| [Security](instructions/security.md) | Secrets, credentials, data, external operations |

## Secrets and external systems

Never read secret values into context or tool output. Never retrieve Secrets Manager values or query
its Agent daemon. Resolve secrets at runtime through dynamic references or an approved ignored
environment file. Keep secrets out of logs, tracked files, and commits. Never read, move, or track
credential files in tool directories such as `.claude/`. User questions never go to a third-party
processor. Use the project operator identity on AWS and a non-root identity on VNGCloud, never an
account root user. The AWS project operator has full `AdministratorAccess`; IAM no longer limits
the operator, so repository rules are the guardrails. Never read secret values. Never delete or
replace a resource without explicit owner confirmation for that exact target. Never touch other
projects' resources in the shared account. Prefer infrastructure as code with reviewed change sets.
Follow [security safeguards](instructions/security.md) and applicable AWS skills and documentation.
