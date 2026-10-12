# Vietnam rebuild implementation plan

Status: M0 foundation implementation in progress. No milestone or deployment is complete.
Paired spec: [Vietnam country module](../specs/vietnam.md).
Related plan: [Country modules](country-modules.md) maps shared M0-M3 work and owns the disabled-country migration after M3.
Approved layout: [Repository layout](../design/repository-layout.md) assigns package homes and migration boundaries.

## Goal and authority

Rebuild Vietnam on the country-agnostic core, reuse qualified v1 code and evidence, and launch Tra cứu quy định at `tracuuquydinh.vn`.
This plan owns delivery order, task ownership, status, and evidence. The linked specs own behavior.
Read [AGENTS.md](../../AGENTS.md), [topic rules](../../instructions/workflow.md), [architecture](../ARCHITECTURE.md), and [PLAN.md](../../PLAN.md) before execution.
The sibling specs remain proposals except for their explicitly approved decisions. Resolve open owner questions before dependent implementation.
The owner's latest direction controls review cadence and Vietnam-first delivery.
Country modules supersedes repository restructure; no repository-restructure prerequisite belongs here.

**Decision:** Keep existing domain packages and add narrow core contracts and country modules.
**Why:** Useful v1 code reduces work while new persistence and admission boundaries protect evidence.
**Rejected:** Small v1 repairs, a blanket `internal/core` move, and early migration of all countries.
**Detail:** Follow [package boundaries](../specs/country-modules.md#package-boundaries-and-registration). Keep legacy entry points for their existing callers.

## Execution rules and status

- Use isolated implementation branches and worktrees. Dispatch implementers as `gpt-6.1-sol`, medium.
- Limit concurrent Codex jobs to four, including planners, implementers, and reviewers across worktrees.
- Give each worker objective, authorities, owned paths, forbidden actions, checks, and a report contract. Workers do not commit, push, deploy, or change cloud resources.
- Run one independent `gpt-6.1-sol`, xhigh review at each milestone end, as the owner directs. Do not add routine task review loops. The reviewer does not implement that milestone.
- Add independent adversarial review for the security scopes named below. Confirm material fixes within those reviews before milestone acceptance.
- The primary inspects task diffs and contracts before dependent work. Only the primary handles Git and reads exact-commit hosted results before merge.
- All builds, tests, generation, formatting, evaluation, and replay run on GitHub. Local work is reading, editing, and static inspection only.
- Keep the five existing country AWS services, databases, images, and hostnames unchanged. New Vietnam workers never call v1 destructive mutations or write a legacy database. Existing AWS scripts are compatibility references only.
- Serve only evidence and one local query embedder. No answer model, generative model, reranker, query translation, or foreign query processing.
- Personal data, accounts, human audit, private notes, logs, and questions stay in Vietnam. Public availability does not authorize foreign document processing.
- Run the new Vietnam writer, writer database, capture and converter workers, and private review API on isolated VNGCloud generation capacity under `deploy/corpus/vn`. Keep them off the serving host. Keep human approval and audit beside socket-only auth on the serving host under `deploy/vn/review`, with a distinct database role and listener. Use vStorage instead of planned S3 storage.
- Admit public artifacts through residency classification before Temporal references, external OCR, or embedding. Exclude unclassified or personal-data-bearing artifacts; do not silently redact originals.
- Reuse originals, captured Vision responses, and vectors only when hashes, provenance, and complete recipes qualify. Missing provenance creates a gap or new candidate.
- Keep active plus at least two prior releases for at least 30 days, and all pins or holds. Retain evidence and recipe artifacts together.
- Use one serving model. Before replacement, re-embed retained dense-search releases into new releases or explicitly retire their dense search. Preserve old named-release `document` and lexical access.
- Record legacy baseline comparisons at every milestone. Only M3 claims and gates retrieval parity. Missing baseline cases elsewhere remain named evidence gaps.

Update this table in each milestone's integration change. Use only `not started`, `in progress`, or `done` with an evidence link.
Each evidence record includes full tested SHA, PR head and merge SHA where applicable, run URL/attempt, input hashes, recipes, scope, findings, and remaining gates.

| Milestone | State | Depends on | Evidence |
|---|---|---|---|
| M0 Core layout and CI gate | in progress | Owner design and repository settings | [Foundation source and pending evidence](country-modules.md#evidence-and-progress); M0.3-M0.4 pending |
| M1 Hosted intake skeleton | not started | M0 | None |
| M2 Real Vietnam evidence | not started | M1 | None |
| M3 Candidate releases and retrieval parity | not started | M2 | None |
| M4 Vietnam host and serving rehearsal | not started | M3 | None |
| M5 Review API and approval path | not started | M4; M2 packets | None |
| M6 Website and launch | not started | M4, M5; final reviewed M3 release | None |

M0 → M1 → M2 → M3 → M4 → M5 → M6 is the Vietnam acceptance path.
The independent [conversion layout slice](document-conversion.md) relocates existing pure Go readers
behind compatible entry points. The owner authorized this source-only refactor before M0 acceptance;
it does not start M1 intake processing, complete a milestone, or change any AWS deployment.
M3 can automatically accept and seal only clean first-pass revisions that pass inherited deterministic gates. Disputed evidence, reprocessing, accepted gaps, and legal-meaning corrections await M5 human approval.
M5 applies approved corrections and reruns the affected M2/M3 gates before M6. It cannot waive missing release evidence.
The [country modules plan](country-modules.md) may start its disabled-country migration after M3 hosted end-to-end evidence. No Vietnam milestone depends on that migration, including launch.
Early I/R runs inject test-only country entries and disposable admission state. They cannot enable the compiled production module or expose a runtime admission bypass.
M4 changes Vietnam's compiled eligibility only after M3 qualification and owner approval. Public routing and schedules remain closed until M6.

## Hosted evidence contract

The following workflow paths and new job IDs are implementation targets, not claims that jobs exist or have run.
PR lanes use synthetic credentials, disposable PostgreSQL 17 with pinned pgvector, fake providers, and no production access.
Only the Semgrep jobs receive the repository scan token under the [security CI trust boundary](../specs/security-ci.md). Fork and Dependabot PRs receive no token.
Protected lanes use residency-cleared, versioned public artifacts. Never expose fixture or provider credentials to untrusted PR code.
Every required lane rejects missing inputs, empty selected suites, skips, failed children, and cancelled runs. Expected negative failures are asserted by a green contract job.

| Lane | Exact workflow and jobs required |
|---|---|
| B, existing checks | `ci.yml`: aggregate `ci` named `CI`. `reusable-checks.yml`: `hygiene`, `auth-generation`, `go`, `browser`, `auth-postgres`. Preserve their current check names. |
| C, new required corpus/CI checks | `reusable-checks.yml`: `workflow-contracts`, `corpus-validation`, `country-contracts`, `gitleaks`, `semgrep-sast`, `semgrep-sca`. All feed the always-reporting `CI` aggregate. |
| G, reviewed generation | `corpus-source.yml`: `generate`, named `Generate and verify corpus persistence source`. Import its allowlisted artifact, then require zero drift on the final committed tree. Auth remains in its separate generation lane. |
| I, intake | New `vietnam-intake.yml`: `intake-postgres`, `temporal-e2e`, `temporal-replay`, `converter-sandbox`, `source-contracts`, `privacy-contracts`, and `intake-gate`. |
| Q, real evidence | New protected `vietnam-qualification.yml`: `fixture-admission`, `conversion-real`, `parts-validity-real`, `coverage-audit`, and `qualification-gate`. |
| R, release/evaluation | New `vietnam-release.yml`: `release-postgres`, `embedding-contracts`, `onnx-build`, `release-e2e`, `public-search-eval`, `parts-eval`, `release-integrity`, and `release-gate`. |
| L, baseline | New protected `legacy-baseline.yml`: `legacy-vn`, `legacy-affected-countries`, `baseline-report`. Run unchanged goldens against core library calls over frozen snapshots, outside production admission. |
| E, model selection | New protected `embedding-bakeoff.yml`: `probe-parity`, `pilot-cost`, `three-model-eval`, `sealed-vn-eval`, `cpu-repeat`, and `bakeoff-gate`. Missing runs prevent a model switch. |
| V, writer infrastructure | New `vietnam-writer.yml`: credential-free `tofu-validate`, `writer-host-contracts`, and `writer-gate`. Synthetic inputs validate `deploy/corpus/vn` provider, network, vStorage, identity, and secret-isolation contracts without live credentials. |
| H, hosting | New `vietnam-host.yml`: `tofu-validate`, `host-edge-contracts`, `auth-recovery`, `delivery-restore`, `serving-contracts`, and `host-gate`. |
| A, review | New `vietnam-review.yml`: `review-postgres`, `review-mtls`, `approval-application`, `review-recovery`, `review-e2e`, and `review-gate`. |
| W, website | New `vietnam-web.yml`: `web-browser`, `continuation-download`, `release-samples`, `plugin-contracts`, and `web-gate`. |
| P, artifact publication | New protected `vietnam-publish.yml`: `validate-commit`, `build-scan-sign`, `verify-provenance`, and `publish-gate`. No deployment from PR jobs. |
| S, owner-run evidence | New protected `vietnam-smoke.yml`: `record-host-drill`, `record-capacity`, `record-review-drill`, `google-clients`, `five-tools`, `launch-gate`. GitHub verifies redacted owner-run reports bound to commit, deployed artifact and policy digests, host bundle, and release digests. |

Each milestone requires B, C, its named lanes, and its review receipts. Required PR jobs feed `CI`; protected runs are additional milestone gates.
Do not make L a hidden prerequisite for earlier milestones or require other-country migration to run L.
Separate external-agent workflow evaluation from retrieval evaluation. Neither score substitutes for the other.

The [security CI spec](../specs/security-ci.md) assigns secrets to Gitleaks, static application security testing (SAST) to Semgrep Code, and software composition analysis (SCA) to Semgrep Supply Chain.
The [security CI plan](security-ci.md) owns implementation and evidence for these three M0.3 jobs.

## M0 Core layout and CI gate

Goal: Compile the retained v1 system with neutral contracts and an inventory-only Vietnam declaration, then enforce trustworthy hosted evidence.

1. **M0.1 Registry and neutral contracts.** Own `internal/core/country/`, `internal/country/catalog/`, `internal/country/vn/`, `internal/corpus/model/`, and consumer `api/` packages.
   Reuse descriptors from [jurisdiction.go](../../pkg/base/jurisdiction/jurisdiction.go#L47) and read contracts from [api.go](../../internal/evidence/api/api.go#L1).
   Write one frozen declaration catalog, `vn.Declare()`, immutable asset manifests, typed absent/incomplete errors, and side-effect-free constructors under [foundation declarations](../specs/country-modules.md#foundation-declarations). M0 accepts inventory only and rejects every runtime entry. Vietnam and the other modules remain runtime `absent`.
   Defer full rule bundles, reader/writer views, and `Factories()` bindings to their real M1/M2 consumer slices. The [foundation subplan](country-modules.md#m0-foundation-subplan) records exact ownership and remaining contracts.
   Implements country-modules: Package boundaries and registration, Country data and language, Durable identity; vietnam: Module contract.
2. **M0.2 Reuse inventory and data homes.** Own Vietnam `data/`, `testdata/`, `docs/specs/`, `docs/README.md`, and current-state records in a separately authorized documentation slice.
   Reuse [seed vocabulary](../../deploy/seed/scope_term.csv#L1), [VN goldens](../../deploy/eval/golden.json#L1), and [source wiring](../../pkg/app/app.go#L371). Freeze all six v1 snapshot/golden/floor manifests; record missing inputs.
   Copy useful Vietnam assets without content changes first. Add fixture manifests and explicit compatibility mappings. Keep shared `pkg/rag`, `pkg/mcp`, `pkg/web`, auth, SQL, and generated-store paths. Move country logic only with its owning later task; retain legacy callers.
   Implements country-modules: V1 reuse map, CI and country qualification; vietnam: Reuse map. Reconcile superseded hosting, language, download, and continuation records before dependent code.
3. **M0.3 CI audit PRs 2–7.** Own `.github/workflows/{ci,reusable-checks,corpus-source}.yml`, `tools/ci/`, and `internal/corpus/schematest/`.
   Reuse [generation guards](../../tools/ci/corpus_generation.py#L1) and [schema suite](../../internal/corpus/schematest/schema_integration_test.go#L1). PR 1 is already handled per the owner; do not repeat its scope.
   Sequence checksum-verified Atlas installation; trusted-base history guards for auth/corpus/extensions; isolated race databases; JSON test-presence/skip checks with mandatory OAuth; required corpus validation with populated base-to-candidate replay; artifact additions/modifications/deletions and truthful provenance summaries.
   Add Gitleaks and both Semgrep checks under the [security CI plan](security-ci.md). Keep their results separate and require all three in `CI`.
   Implements database: Migration slices and verification gates; write-path: Current state and evidence; verification: Hosted check matrix and Isolated inputs and missing-run gates.
4. **M0.4 CI audit PRs 8–9 and protection.** Own `Makefile`, `sqlc.yaml`, `.github/dependabot.yml`, and the CI files from M0.3 after handoff.
   Reuse [format/evaluation commands](../../Makefile#L1). Write independent check scheduling, explicit cache writers, one failure-propagating formatting contract, pin updates without auto-merge, and CI documentation links.
   The owner configures branch/tag protection, exact emitted `CI` check, signed commits, merge commits only, allowed actions, and protected release environments. Confirm enforcement with disposable negative branches.
   Implements country-modules: CI and country qualification; verification: Hosted check matrix. No new cloud credentials enter ordinary CI.

Exit evidence: B/C and G after reviewed artifact import. Require PR, default-branch, and manual CI evidence at the final SHA, plus cold/warm cache durations.
Negatives: wrong Atlas digest stops execution; edited/deleted/renamed/rehashed old migrations fail, including edits hidden in earlier PR commits. Missing migration, generated drift, lost seeded data, absent test, nested skip, obsolete generated file, altered artifact hash, missing provenance, invalid formatter config, and broken links fail.
Aggregate contract tests reject failed, skipped, cancelled, and missing required jobs. Registry tests prove no constructor I/O and disabled-country denial before pools, factories, or routes.
Real-document evidence: freeze accepted v1 input references and hashes only. M0 makes no new legal-validation or retrieval-parity claim. Record L comparisons and unavailable cases.
Adversarial review: CI trust boundaries, installer bytes, generation artifact import, migration history, repository protections, and admission contracts.
Owner gates: O1, O2, O3, and O26 repository settings only. Does not implement a blanket package move, a v1 bug backlog, live source probes, any deployment, or five new country modules.

## M1 Hosted intake skeleton

Goal: Prove SourceSync → capture → sandbox decode/conversion → fenced accepted revision on disposable hosted infrastructure.

1. **M1.1 Minimal persistence.** Own `sql/{ingest,bronze,silver}/`, append-only matching `deploy/migrations/` chains, `sqlc.yaml`, hosted-generated `pkg/store/`, and `internal/corpus/store/`.
   Reuse [atomic apply](../../internal/corpus/run/apply.go#L44), [execution budgets](../../internal/corpus/run/types.go#L42), and [revision schema](../../sql/silver/schema.sql#L396).
   Write admission epochs, intake/batch/control leases, executions without build IDs, immutable receipts, launch/notification outbox, revision acceptance projection, nullable legacy `build_id`, identity catalog, and basic conversion provenance. Review G artifacts before adapters import them.
   Implements database: Intake ledger and revision acceptance, Conversion provenance and decisions, Identity/revisions/membership; write-path: Stage contract.
2. **M1.2 Workflows and composition.** Own `internal/corpus/workflow/temporal/`, `internal/app/corpus/`, `cmd/corpus-worker/`, and `vietnam-intake.yml`.
   Reuse [SDK worker](../../internal/corpus/workflow/temporal/worker.go#L47), [replay API](../../internal/corpus/workflow/temporal/replay.go#L11), and [external-job lifecycle](../../internal/corpus/workflow/temporal/types.go#L377).
   Write versioned SourceSync/DocumentIngest and ControlReconcile; durable child intents, ABANDON/start acknowledgement, quarantine wait contracts, cancellation, rollover, resource queues, and worker deployment pinning. Keep v1 registrations intact.
   Implements workflows: Workflow contracts, Source intake and child lifetime, Fan-out/history/resource limits, Versioning and replay. Apply all stated ceilings, including groups of 16, 32 processing/256 open documents, and reference-only envelopes.
3. **M1.3 Core capture and Vietnam source split.** Own `internal/corpus/source/`, `internal/country/vn/sources/{vbpl,congbao,vanban,sbvhanoi}/`, and `vn/corpus/`.
   Reuse [VBPL discovery](../../pkg/ingest/vbpl/discover.go#L82), [gazette detail](../../pkg/ingest/congbao/detail.go#L81), [gazette TLS verification](../../pkg/ingest/congbao/tls.go#L69), [Vanban pagination](../../pkg/ingest/vanban/discover.go#L14), [Hanoi discovery](../../pkg/ingest/sbvhanoi/discover.go#L16), and qualified `pkg/fetch` mechanics.
   Write pure planners, network-only core capture, sandbox listing/detail decoders, typed observations/follow-ups, shared host budgets, artifact inventory, and complete-window cursor accounting. Strip inner v1 retries. Ledger source retry owns five attempts/24 hours and persisted `NextEligible`/`Retry-After`; infrastructure retry owns three attempts only.
   Implements country-modules: Package boundaries and registration; vietnam: Sources and authority, Document types and số ký hiệu; write-path: Stage contract and Closure/accounting/evidence.
4. **M1.4 Block conversion and initial gate.** Own `internal/corpus/{conversion,quality,blob}/`, `deploy/containerfiles/Containerfile.corpus-converter`, and synthetic conversion fixtures.
   Reuse [DOCX](../../internal/corpus/conversion/docx.go), [HTML](../../internal/corpus/conversion/html.go), [MuPDF/DOC bridge](../../pkg/extract/fitz/fitz.go#L47), [text findings](../../pkg/extract/extract.go#L187), and [blob identity](../../internal/corpus/blob/blob.go#L15).
   Write `ConvertedDocument`, hashes, source anchors, reversible maps/edit ledger, per-artifact reports, and bounded supervisor/sandbox output validation. Unknown geometry or fidelity cannot pass a mandatory gate. Add residency admission and synthetic privacy sentinels before every export.
   Implements conversion: Activities, Intermediate document model, Quality gate and authority, Converter isolation; database: Conversion provenance and decisions.

Exit evidence: B/C/G and all I jobs green. A pinned hosted Temporal test server must write an accepted fixture revision before any build exists and produce real SDK replay histories.
Negatives: stale epoch/fence, changed replay payload, response loss after commit, crash before launch/signal, duplicate starts, incomplete pages, follow-up budget reset, worker-loss retry inflation, and cancellation cannot accept extra evidence or advance incomplete cursors.
Sandbox probes deny network, metadata, credentials, sockets, database, and Temporal access. Cover ZIP/XML/image bombs, output traversal, process-group timeout, and resource overruns. Fetch never parses response bodies; decoders never fetch.
Maximum envelopes and concurrent child commands stay below history/transaction budgets. Rejected personal-data sentinels cannot enter logs, histories, exports, or batch manifests.
Real-document evidence: none claimed by synthetic exits. Acquire hash-pinned, residency-cleared originals for M2 through protected admission; no protected fetch means no real validation. Record L separately.
Adversarial review: every mutation/fence/outbox, source server-side request forgery (SSRF)/redirect/TLS policy, sandbox routing and isolation, residency screening, and Temporal payload boundaries.
Owner gates: O2, O4, O6–O8. Does not publish, activate, qualify a converter on fakes, invoke legacy mutations, expose review approval, or migrate other countries.

## M2 Real Vietnam evidence

Goal: Qualify conversion, parts, identity, relations, and date-aware validity against captured official originals.

1. **M2.1 Qualified converters and OCR.** Own conversion/quality from M1.4, `internal/corpus/ocr/`, OCR workflow activities, and `vn/testdata/real/` manifests.
   Reuse [Vision adapter](../../pkg/extract/docai/docai.go#L278), [non-binding OCR precedent](../../pkg/pipeline/ocr_all.go#L290), and [diagnosed text patterns](../../pkg/pipeline/vietnamtext.go#L78) as findings or reversible transforms.
   Write structured native DOCX/HTML/XLSX, isolated LibreOffice/MuPDF/image paths, evidenced mixed-font decoding, full Vision-response retention, request byte caps, page/region/tile bijection, batch reconciliation, and vStorage backup manifests. OcrBatch/Reprocess use stored originals and durable jobs, never inline OCR.
   Implements conversion: Per-format conversion, Legacy encodings, Versioned reprocessing, Converter evaluation and fixtures; workflows: Shared batches and Document processing/quarantine.
2. **M2.2 Body/parts handshake.** Own `internal/country/vn/grammar/{body,parts,citations,references}.go` and matching `_test.go` files, `internal/corpus/parts/`, and block-only structure helpers in conversion after M2.1 handoff. Exclude M2.3 grammar files.
   Reuse [body parser](../../pkg/pipeline/legalparse.go#L27), [VBPL tree](../../pkg/pipeline/vbpl_tree.go#L43), [citation fixtures](../../pkg/pipeline/citation_golden_test.go#L18), and [citation rendering](../../pkg/pipeline/index_activities.go#L615).
   Write Phần/Tiểu mục grammar, cross-page quote state, explicit source gaps, issued-text namespaces, appendix/form boundaries, all six PartResult fields, one-owner reconciliation, native citations, table topology, and reference resolution. Never fabricate missing labels or merge duplicate form codes.
   Implements vietnam: Structure and native citations; conversion: Body structure building; parts: Part model/parsing, Citations/references, Files/table fidelity.
3. **M2.3 Legal assertions, query rules, and scope.** Own `internal/corpus/{relations,validity,subjects}/`, VN pure query rules, and `internal/country/vn/grammar/{identity,effects,subjects,vbhn}.go` with matching `_test.go` files. Exclude M2.2 grammar files; M2.4 alone integrates shared grammar registration/configuration after both handoffs.
   Reuse [identity helpers](../../pkg/pipeline/process_activities.go#L1407), [number/edge evidence](../../pkg/pipeline/relation_evidence.go#L70), [scope matcher](../../pkg/scope/scope.go#L1), and [VBHN family candidates](../../pkg/pipeline/vbhn_validity.go#L61).
   Write typed candidate disambiguation; separate operator/target/recital/replacement spans; exact provision/part effects; uncertain endpoints; half-open Vietnam dates; consolidation cutoff evidence; subject primary/context assertions. Replace status mirroring and unknown-as-current in the new path.
   Write deterministic query validation from frozen lexicons. Tune the proposed 80% token-coverage/two-Vietnamese-token rule and exact short-query entries, then freeze the rule before M3 sealed/public evaluation. Entire qualified selectors bypass only the prose test; bare dates and numbers inside English prose do not.
   Implements vietnam: Scope and subjects, Document types, Amendment grammar and validity, Official consolidated texts, Vietnamese input/text policy; parts: Part validity/relation evidence; write-path: Closure/accounting/evidence.
4. **M2.4 Evidence persistence and qualification.** Own `sql/{silver,bronze,config}/`, matching migrations/generated stores, `internal/corpus/store/`, and `vietnam-qualification.yml` after M1.1 handoff.
   Reuse [revision/subject schema](../../sql/silver/schema.sql#L396) and existing G machinery. Write parts/blocks/cells/artifact spans, common `evidence_target`, relations, differences, validity/text periods, taxonomy assessments, comparisons, and mandatory adjudication rejection.
   Freeze 80 real documents in eight strata, ten each, split 40 calibration/40 held-out by family. Two human annotators establish gold truth; resolve disagreements against originals. Missing real strata leave that format unqualified.
   Qualify source access rules, captured endpoints, redirects, cadence, and attachment coverage in protected runs. Attempt direct-SBV listing/detail/pagination/VBHN qualification before writing or registering `vn/sources/sbv`; unresolved access leaves the named gap governed by O5.
   Implements database: Parts/validity/relations, Subjects; conversion: Converter evaluation/fixtures; vietnam: Evaluation and qualification gates. Capture the six reported clause identities separately from Điều 22's six khoản.

Exit evidence: B/C/G/I plus all Q jobs green for qualified formats. No missing fixture hash, failed original fetch, or skipped case becomes a pass. Record L differences without claiming parity.
Hard set: 09/2020 Điều 25 and 50/2024 khoản 6 Điều 22 on 2024-12-31/2025-01-01; all six reported incoming clauses; omnibus recital traps; `116/ 2025/QH15`; duplicate QH/VBHN numbers; quotes across pages; Tiểu mục; missing khoản; old outlines; mixed TCVN3/VNI; corrupt PDF layers; post-signature forms; duplicate codes; scanned attachments; nested/merged tables; replaced appendix; drafts; missing commencement.
Require exact identity/citation/target/date/namespace/authority results, zero invented text or false-current stamps, and preserved source gaps. Character error rate (CER) is ≤0.1%/document for deterministic text and ≤1% for OCR. Require zero unflagged critical-token errors and all conversion hard gates.
Negatives cover missing/duplicate OCR tiles, encoded image/JSON caps including base64, table loss, ambiguous encoding, unresolved quotes, wrong parent/version, stale comparison/adjudication, timeout/decision races, and build-before-adjudication. Excluded spans enter neither index; a sound attachment can survive a body gap.
Review-backed acceptance remains blocked until M5. Human fixture annotation proves expectations, not a bypass around the approval API.
Adversarial review: converter/package/license risks, provider submission uncertainty, exported content, authority propagation, immutable provenance, part targets, and candidate eligibility.
Owner gates: O4, O6–O7, O9–O11. O5 governs direct-SBV deferral, not completion of qualified four-source fixtures. Does not repair live v1 rows, make OCR binding, synthesize consolidated law, enable unqualified formats, or run optional OCR challengers.

## M3 Candidate releases and retrieval parity

Goal: Run the complete Vietnam write path and seal a qualified immutable release with measured retrieval parity.

The independent [chunking helper slice](chunking-layout.md) relocates existing pure helpers under
the approved layout. The owner authorized this source-only work before M3 acceptance. Hosted and
corpus evidence remain pending; no M3 requirement or AWS deployment is accepted by this move.

1. **M3.1 Snapshot, closure, and release storage.** Own `sql/{ingest,gold,config}/`, append-only migrations/generated stores, `internal/corpus/{store,release}/`, and CorpusBuild workflow activities.
   Reuse [manifest validation](../../internal/corpus/release/validation.go#L5), [publication ports](../../internal/corpus/release/publication.go#L173), [pin resolver](../../internal/corpus/release/resolver.go#L60), and [retention ports](../../internal/corpus/release/retention.go#L1).
   Write accepted-watermark snapshots, base plus additions/replacements/evidenced tombstones, eligibility receipts, bounded one-hop/three-round target closure, immutable membership, durable pins/holds/deletion fences, recovery records, export allowlists, and local desired/applied selection contracts.
   Implements database: Releases/delivery/retention, Vector/lexical storage; write-path: Closure/accounting/evidence, Hosting/delivery; workflows: Shared batches/builds/promotion.
2. **M3.2 Chunks and bulk vectors.** Own `internal/corpus/{chunking,embedding}/`, new EmbedBatch activities, `pkg/rag/lexical/`, and part-aware chunk computation.
   Reuse pure [chunk/context logic](../../pkg/pipeline/index_activities.go#L340), [BM25](../../pkg/rag/lexical/bm25.go#L48), and [query/document formatting](../../pkg/rag/embed/embed.go#L44) only for its matching model.
   Write part-safe token budgeting, complete lexical revision, Hugging Face Jobs controller, Runpod Flex fallback port, recipe-bound caches, one-outcome-per-input receipts, and 768/1024-dimension checks. No hidden truncation or automatic provider resubmission after uncertainty.
   Implements embedding: Recipe/runtime parity, Long provisions/storage; parts: Retrieval/evidence outputs; workflows: Shared batches; database: Vector/lexical storage.
3. **M3.3 Model preflight and bounded bake-off.** Own `pkg/rag/embed/onnxembed/`, `cmd/embedder/`, model/export helpers, and `embedding-bakeoff.yml`.
   Reuse [Qwen runtime](../../pkg/rag/embed/onnxembed/onnxembed.go#L8) and [embedder composition](../../cmd/embedder/embedder_onnx.go#L15). Write encoder support, artifact/license manifests, role-specific recipes, bounded singleton admission, and exact CPU/GPU parity.
   Compare only Granite 311M R2 INT8/768, Qwen 0.6B control/1024, and Vietnamese_Embedding_v2/1024. Use 1,000 public parity probes, batches 1/8/32, a 10,000-chunk cost pilot, and three CPU repeats. Keep Qwen unless every switch gate passes.
   Implements embedding: Selection/rejected options, Recipe/runtime parity, CPU query service, Hosted bake-off, Migration/rollout slices. CPU-runner timing cannot certify the VNGCloud host.
4. **M3.4 Evaluation, sealing, and publication artifacts.** Own `cmd/eval/`, `pkg/eval/`, `pkg/rag/retrieve/`, VN evaluation manifests, `vietnam-release.yml`, `legacy-baseline.yml`, and `vietnam-publish.yml`.
   Reuse [evaluation matcher](../../pkg/eval/eval.go#L109), [RRF](../../pkg/rag/retrieve/rrf.go#L29), [frozen VN goldens](../../deploy/eval/golden.json#L1), and [floors](../../Makefile#L68). Write separate public-search and legacy lanes, exact eligible-set oracle, release integrity checks, signed manifests, amd64 build/scan/provenance, and protected artifact publication.
   Preserve unchanged legacy routing/preprocessing only in L. Public search uses the qualified Vietnamese gate. Add 120 human-judged scenarios, 20 in each embedding stratum, split 60 tuning/60 sealed by family; add at least 20 form and 20 appendix cases.
   Apply subject/date eligibility before candidate limits in both retrieval arms. Keep Vietnam exact dense scanning initially; hierarchical navigable small world (HNSW) indexing, half precision, deeper pools, and chunk changes each need a separate frozen-factor comparison and unchanged-floor gate.
   Implements vietnam: Evaluation and qualification gates; embedding: Hosted bake-off/Retrieval without a reranker; database: Serving queries/accounts; parts: Evaluation/coverage.
5. **M3.5 Isolated VNGCloud writer deployment preparation.** Own `deploy/corpus/vn/`, writer image definitions under `deploy/containerfiles/`, and `vietnam-writer.yml`.
   Use [run-pipeline.sh](../../deploy/aws/run-pipeline.sh#L4) only as a compatibility reference for launcher duties. Reuse M1 worker composition, not the legacy direct-stage command. Write reviewed IaC for an isolated VNGCloud Vietnam writer, writer database, capture and converter capacity, vStorage namespaces and backups, scoped runtime identities, resource pools, and Temporal Cloud deployment configuration.
   Keep writer and worker capacity separate from the serving host. Do not choose a size or claim total cost before measurement. Bind provider credentials at runtime, pin images and recipes plus old recovery workers, configure 30-day Cloud history and reference-only payloads, and keep source schedules disabled.
   Provider API, network, storage, backup, restore, image, and identity parity are unverified. Require V `tofu-validate`, `writer-host-contracts`, and `writer-gate`, plus adversarial review, before apply to an isolated validation target. V uses synthetic inputs and no live credentials. Use the isolated target to collect live proof. Live provider, storage, backup, and restore evidence gates production activation, unattended schedules, and cutover. Workers never deploy or apply resources.
   Implements write-path: Hosting and delivery; workflows: Versioning/replay, Fan-out/history/resource limits, Cost estimates. Record measured actions, history and storage, provider charges, and submission reconciliation before unattended scheduling. Existing AWS resources remain untouched.

Exit evidence: B/C/G/I/Q/R/L/P/V green. V must pass before isolated apply. E is mandatory for a model-switch claim; otherwise retain the qualified Qwen recipe and record no switch. `release-e2e` captures hosted Temporal histories from discovery through sealed release, not handwritten histories.
M3 is the retrieval-parity milestone. Both legacy and public gate-accepted lanes meet recall@8 ≥0.90, mean reciprocal rank (MRR)@8 ≥0.66, current-law precision ≥0.99, abstention ≥0.95. Explain every control/candidate case delta; no new wrong-current-law result.
Public cases `edge-no-diacritics-infosec`, `edge-no-diacritics-payment`, and `edge-no-diacritics-mangluoi` require `VIETNAMESE_QUERY_REQUIRED` with zero embedding/corpus calls. Report rejected cohorts, exclusions, `expect_fail`, graph credit, and direct provision hits separately.
Forms and appendices separately require recall@8 ≥0.95 and MRR@8 ≥0.80. Audit at least 30 eligible documents per release, stratified by source/format; inspect all if fewer. Every expected slot has evidence or an approved named gap; zero denominator means unknown/not applicable.
Negatives: no-op/delta/late-target snapshots, stale writes, uncertain/missing/duplicate/wrong-recipe vectors, nonfinite vectors, token overflow, absent lexical rows, orphan citations, dangling targets, and hash mismatch block sealing. Failed floors cannot become gap waivers.
Model parity uses the embedding spec's cosine/norm/top-20 gates. A switch needs unchanged VN quality and either ≥2 percentage points MRR gain or ≥30% lower p95, sealed paired intervals, complete input coverage, runtime/license gates, and explicit Vietnam-only scope if other candidates fail.
Adversarial review: release integrity, SQL/role boundaries, isolated writer IaC/deployment, provider credentials and receipts, signing/provenance, activation/pin/deletion races, and restore continuity.
Owner gates: O8, O12, O14–O18, O26; O18's spend approval is needed only for the bake-off. Does not deploy serving, activate a public release, promote on agent authority, claim common ASEAN quality without all-country gates, or require disabled-country migration.

## M4 Vietnam host and serving rehearsal

Goal: Prepare the dedicated Vietnam host, verified release delivery, local query runtime, auth, and five-tool serving behind closed public admission.

1. **M4.1 Infrastructure and host bundle.** Own `deploy/vn/{main,variables,outputs,versions}.tf`, `deploy/vn/{Caddyfile,coraza.conf,crowdsec.yaml}`, `deploy/vn/systemd/`, `deploy/containerfiles/Containerfile.vn.*`, and `vietnam-host.yml`. Exclude `deploy/vn/{scripts,review}/`.
   Reuse reviewed patterns from [server container](../../deploy/containerfiles/Containerfile.ecs.server#L36) and [embedder container](../../deploy/containerfiles/Containerfile.ecs.embedder#L60), adapting to amd64 without modifying AWS deployment files.
   Write pinned OpenTofu root, systemd/Podman bundle, Caddy/Coraza/CrowdSec local edge, encrypted volumes, socket-only PostgreSQL, runtime-injected secrets, independent persistent deploy firewall fence, locks, and dependency readiness. Keep serving in HCM03-1A on `a-general-4x8`, with 30 GB root and 50 GB data. Use `a1-standard-2x8` only as a measured fallback. Grow only from measurements.
   Implements hosting: Chosen topology, Workload/sizing, Infrastructure/deployment, Security/residency. Owner applies reviewed infrastructure; workers do not apply.
2. **M4.2 Local release and query serving.** Own `internal/app/serve/` except `hosted.go`, `internal/evidence/{api,service,store}/`, `pkg/mcp/`, local control/promotion adapters, and `deploy/vn/scripts/{import-release,promote-release,rollback-release}.sh`. Exclude M4.3 auth/backup scripts.
   Reuse [serving composition](../../internal/app/serve/app.go#L15), [evidence service](../../internal/evidence/service/service.go#L37), and [five-tool guide](../../pkg/mcp/brief.go#L43).
   Write verified inactive logical import, local admission/ordered revocation, durable pins, bounded shared reads, runtime-recipe readiness, and one query gate before expansion, embedding, or corpus queries. Write production Promote delivery/readiness/ordered-activation adapters, owner approval bindings, duplicate/lost-ack reconciliation, and rollback. Remove new-system legacy-resolver fallback and accent restoration. Add part/review fields, exact selectors, paging, and native locale/guide strings.
   Implements country-modules: Local read admission; vietnam: Vietnamese input/text policy, MCP guide/website strings; hosting: Corpus delivery, Domain/readiness/monitoring; database: Serving queries/accounts.
3. **M4.3 Auth and recovery.** Own `pkg/identity/`, `pkg/oauthserver/`, `sql/auth/`, separate auth migrations/generated store, `cmd/server/`, `internal/app/serve/hosted.go`, and `deploy/vn/scripts/{auth-init,backup,restore-auth,restore-catalog}.sh`. Exclude M4.2 release scripts. Integrate shared serving registration/configuration after M4.2 handoff.
   Reuse [identity manager](../../pkg/identity/manager.go#L67), [OAuth server](../../pkg/oauthserver/server.go#L63), [hosted composition](../../internal/app/serve/hosted.go#L65), and [block primitives](../../sql/auth/schema.sql#L195).
   Write new-origin client configuration, socket roles, durable block/unblock journal, independent catalog recovery, pgBackRest backups to vStorage, key/session/grant invalidation after restore, and startup/traffic/deletion fences. Verify HCM04 placement and restore behavior through live evidence before production activation or cutover. Corpus rollback never restores auth. Register live Google/clients only through owner operations.
   Implements hosting: Accounts/backups/restore, Security/residency; database: Releases/delivery/retention; AUTH: Middleware/failure behavior and Deployment boundary.
4. **M4.4 Destination and host rehearsal.** Own load/smoke harnesses under `tools/ci/`, `vietnam-smoke.yml`, and non-sensitive deployment reports.
   Reuse [publication readiness/CAS](../../internal/corpus/release/publication.go#L173) and [pool pin mechanics](../../internal/corpus/release/pool.go#L1). Write harnesses for M4.2 promotion adapters and host-drill evidence import. Exclude production adapters and all `deploy/vn/` files.
   Implements write-path: Hosting/delivery; workflows: Shared batches/builds/promotion; hosting: Rollout/verification gates. Rehearse only with owner-authorized inactive destinations and synthetic auth data.

Exit evidence: B/C/G/R/H/P and S `record-host-drill`/`record-capacity`. Green static H checks do not prove provider identity and access, encryption, region, backups, or host performance; require redacted owner-run receipts for each.
Measure 2 requests/second for 30 minutes, bursts of 8, concurrent PostgreSQL/application load, one runtime, startup/restore peaks, and ≥25% free headroom. Search p95 <3 s; inference short p95 ≤500 ms/p99 ≤1 s, long p95 ≤1 s, RSS ≤2.5 GiB, warm readiness ≤60 s.
Prove five-tool payload parity on a real qualified release, Vietnamese rejection before all query work, no query egress, and SELECT-only corpus access. Record L; no legacy AWS redeploy is needed.
Negatives: tampered/auth-bearing dump, wrong country/schema/runtime, corrupt originals, missing readiness, CAS conflicts, lost acknowledgements, stale/out-of-order control, revoked pins, and unavailable writer after restore keep admission closed. Reconciled uninterrupted serving can read offline.
Restore an older backup after newer activation, identity/hold changes, and a block. Recover independent journal continuity; old sessions/tokens fail and absent blocked identities cannot register. Missing journal/writer authority fences traffic and deletion across reboot. Hold beyond 30 days preserves closure.
Adversarial review: IaC, provider identity and access, deployment firewall fence, auth/OAuth, token isolation, logging/egress, backup keys/journals, restore/rollback, release loading, and runtime transitions. Complete this review before owner apply approval.
Owner gates: O2, O14, O16–O17, O19–O20, O22, O26. Does not open broad access, redirect an existing endpoint, deploy a corpus worker on serving, run two models, or use AWS failback.

## M5 Review API and approval path

Goal: Apply explicit human decisions through the private review API and the single VNGCloud writer. Keep the private API with generation capacity. Keep review identity, human approval, and audit beside local auth on the serving host.
The [corpus review plan](corpus-review.md) owns its delivery slices and evidence gates. M5 integrates those slices into the Vietnam sequence.

1. **M5.1 Packets, queue, and writer API.** Own `internal/corpus/review/`, `cmd/corpus-review/`, review SQL under `sql/ingest/`, matching generated artifacts, `internal/corpus/{run,workflow/temporal}/{review,review_test}.go`, and `vietnam-review.yml`. Integrate shared writer registration/configuration and correction-loop wiring after M5.3 handoff.
   Reuse [fenced apply](../../internal/corpus/run/apply.go#L44), [decision delivery ports](../../internal/corpus/workflow/temporal/activities.go#L1), and M2 hash-bound packets.
   Write append-only items, proposals, checks, and receipts beside the VNGCloud writer. Implement `list_queue`, `get_item`, `get_evidence`, `propose_decision`, `check_proposal`, and `get_receipt` with exact spec byte, rate, and page caps plus task-scoped credentials. Agents cannot approve, fetch arbitrary URLs, or access human records.
   Implements review: Review item/evidence packet, Append-only decisions, Review API first, Queue order/service targets.
2. **M5.2 Vietnam approval and audit.** Own `internal/app/review/`, `cmd/review-approval/`, local auth review tables/adapters, and `deploy/vn/review/{approval.service,policy.json}`. Exclude M5.3 TLS files; integrate shared review registration/configuration after M5.1 and M5.3 handoffs.
   Reuse [current account/epoch policy](../../pkg/identity/manager.go#L67) and M4 independent Vietnam journals. Write the reviewer directory, scoped grants, human multi-factor authentication (MFA) and operator transport, immutable audit and outbox, separate application principal, live authority checks, and one-use permits ≤60 seconds at every corpus-affecting application.
   Implements review: Roles/authority, Application/fencing/residency, Security/certificates/recovery. Keep account/grant epochs, identities, human CRLs, private notes, and mappings in Vietnam.
3. **M5.3 TLS, application, and correction loop.** Own `deploy/vn/review/{Caddyfile,crl-verifier.conf,renew.sh,revoke.sh}`, `tools/ci/{review_drill,import_review_drill}.py`, and public review projections in evidence service after M4.2 handoff. Exclude M5.1 writer application/workflow files and M5.2 approval files; hand correction-loop wiring to M5.1 and the `record-review-drill` job to M4.4's smoke-workflow owner, sequentially.
   Reuse M2 comparison and M3 immutable assessment/release contracts. Write private-CA human/service separation, per-handshake current CRL enforcement, renewal/revocation, fenced approval consumption, five quarantine actions, supersede/revert, and scoped public summaries. No agent agreement counts as a human check.
   Run the six-clause case through packets, independent human source checking, owner approval, fenced acceptance, next build, evaluation, and separate promotion rehearsal. Add encoding/parts/OCR/metadata/subject regression manifests and held-out rule checks.
   Implements review: Worked example/rollout, Quality loop, User-facing evidence; conversion: Quarantine/review and Versioned reprocessing; workflows: Document processing/quarantine.

Exit evidence: B/C/G/I/A, affected Q/R, and `vietnam-smoke.yml/record-review-drill` green. Record affected L results; unchanged floors gate any explicitly claimed retrieval-parity replacement. `review-e2e` proves application receipts precede published scoped markers on real originals; packet closure or a signal cannot mean acceptance.
M5.3's harness and redacted evidence import require owner-run deployed certificate/CRL enforcement, resumption and direct-backend denial, human/service separation, live MFA/session binding, application authorization, grant revocation, and recovery after a revoked grant and an applied writer decision. Bind the receipt to the exact commit, deployed artifacts, policy digests, and writer decision receipt; synthetic CI cannot substitute.
Negatives: missing/expired/revoked/wrong-issuer certificates, stale/missing CRLs, resumed-session bypass, direct backend access, public tokens, wrong audience/scope, same-model or unverified agent checks, injected document instructions, arbitrary URLs, stale packets, missing independent human check, and all authority/fence/expiry races fail closed.
Prove role revocation before dispatch and before ApplyEvidence, consumed-permit replay, bounded in-flight permit expiry, journal failure before acknowledgement, commit-before-signal loss, timer race, and late linked attempts. No agent can make OCR binding or waive a gate.
Restore after a revoked grant and applied writer decision. Recover directory links, human audit, CRLs, and writer receipts from the Vietnam journal; missing continuity fences review and permits. Privacy sentinels never enter external processor payloads, agent output, Temporal, or public releases.
Adversarial review: certificate/revocation/MFA enforcement, grant authority, one-use permits, journal durability, prompt injection, separate principals, residency, and immutable application. Review the live drill receipt and confirm material fixes before M5 acceptance; missing live proof or review receipt keeps M5 in progress.
Owner gates: O8, O11, O23–O25. Does not build the human console, allow autonomous acceptance, run a review model on servers, or expose review actions in public MCP.

## M6 Website and launch

Goal: Launch a Vietnam evidence workbench with authenticated live search, release-pinned readers, public samples, and client connections.

1. **M6.1 Website and pinned evidence.** Own `pkg/web/` except `{download,download_test}.go`, `cmd/server/landing.go`, local assets/templates, VN locale data, and `vietnam-web.yml`. Exclude M6.2 continuation/download files; integrate shared routes/configuration only after M6.2 handoff.
   Reuse [web evidence/auth interfaces](../../pkg/web/web.go#L29) and [templates](../../pkg/web/templates.go#L1). Write `/`, `/mau/{slug}`, `/ket-noi`, `/nguon`, `/tra-cuu`, `/van-ban/{document}`, release-built samples, source coverage, subject/part filters, accessible reader/tables, exact citation copy, and browser-local dossiers.
   Display target-level date stamps, scoped review, OCR/VBHN/gap warnings, bounded lineage, and stored exact-target redlines where qualified. Use self-hosted fonts, both device themes, Vietnamese legal text, and English controls only. AI handoff copies evidence only on user action.
   Implements WEBSITE: Site map, Visual system, Phase 1 and redesign-dependent fields; parts: Website presentation; review: User-facing evidence; vietnam: MCP guide/website strings.
2. **M6.2 Downloads and continuation.** Own `pkg/identity/{continuation,continuation_test}.go`, `pkg/web/{download,download_test}.go`, `internal/evidence/{api,service}/{artifact,artifact_test}.go`, and `deploy/vn/download-limits.json`. Exclude shared routes/templates/configuration; hand registration to M6.1.
   Reuse [existing file projection](../../pkg/mcp/corpus.go#L1726) and web-session/account checks. Write `GET /van-ban/{document}/tep/{artifact}?release=...`, PDF/DOC/DOCX type validation, catalogued hash-bound bytes, safe attachment headers, and positive byte/stream/deadline limits.
   Implement the parts spec's exact `next` grammar and 2,048-byte bound, 256-byte identifiers, valid ten-byte dates, callback revalidation, preserved release/date/part, and post-login fragment reconstruction. No question enters login state or callback URLs; keep typed draft text only in the browser's current page memory.
   Implements parts: Files/table fidelity and Login continuation; hosting: Corpus delivery/download limits; AUTH: Routes/user experience and token boundaries. XLSX remains searchable with official links, without application download or rich rendering.
3. **M6.3 Plugin and public copy.** Own Vietnamese plugin metadata/assets, `docs/PUBLISHING.md`, privacy/terms/support/connection copy, and plugin contract fixtures.
   Reuse [existing publishing record](../PUBLISHING.md) and M4 resource-bound OAuth/five-tool transport. Write Vietnamese plugin listing/demo material, exact origin/client configuration, evidence-only permissions, current coverage statements, and notice of user-initiated foreign AI-client access.
   Implements WEBSITE: Phase 2 plugin only and Open questions; hosting: Domain/readiness/monitoring and Security/residency. Verify current Directory review/access requirements before submission; owner supplies approved demo access. Listing approval is separate from website launch.
4. **M6.4 Launch cutover.** Own thin Vietnam command clients, `deploy/vn/scripts/` cutover/runbooks, S final jobs, and this plan's evidence/status updates.
   Reuse command duties from [pipeline CLI](../../cmd/pipeline/main.go#L528) and [ingest harness](../../cmd/ingest/main.go#L1). Write start/status/cancel/backfill/promote clients using fenced ports; deny legacy tools access to the new VN writer while preserving other-country callers.
   Owner approves the exact release/destination/expected selection and public cutover. Verify readiness, DNS-only/TLS/DNSSEC, real Google, named ChatGPT and Claude flows, five tools, public samples, and original hashes. Enable bounded source schedules only after measured shadow cost/recovery proof.
   Implements write-path: Command migration and Controlled Vietnam promotion; workflows: Controlled cutover; hosting: Domain/readiness/monitoring and Rollout gates; vietnam: Serving contract/release readiness.

Exit evidence: B/C/G/R/H/A/W/P plus all S jobs green for the launch commit. Owner approval, destination applied receipt, and post-deployment public smoke are mandatory; CI alone cannot mark launch done.
Browser cases cover phone/desktop, both themes, keyboard/focus, exact citation/part/date stamps, no external asset requests, and static anonymous samples without live evidence calls. Match real reader/sample/download evidence to the same release.
Negatives: token crossover, blocked/expired sessions, malicious Host/Origin/CSRF, traversal/encoded/duplicate/unknown `next` inputs, wrong-release files, corrupt/type-mismatched bytes, declared/actual oversize, timeout/disconnect, and per-account/global stream exhaustion fail without substitution and release every slot.
Measure live capacity and restore proof again only when final changes affect those results. Rollback stays in Vietnam and preserves current auth. Check retained named-release reads/lexical search and explicit dense availability after promotion.
Adversarial review: public routes, continuation, authenticated originals, OAuth/client policy, publishing/provenance, command writer admission, schedules, and launch cutover.
Owner gates: O5–O6, O8, O13, O21–O22, O26–O28. Does not build server dossiers, DOCX export, watch/email subscriptions, form filling/uploads, paid tiers, a console, or other-country products.
Old Vietnam AWS behavior changes only under O21's exact endpoint approval. Never forward questions/tokens across origins. No other AWS endpoint changes. Disabled-country migration never blocks launch.

## Parallel ownership and handoffs

Run at most four Codex jobs.

| Work window | Disjoint work allowed | Required handoff |
|---|---|---|
| M0 | Registry/contracts; CI helpers; fixture/data inventory | One integrator owns workflow/SQL configuration edits. M0.4 follows M0.3 on shared CI paths. |
| M1 | Ledger SQL/store; workflow/composition; source planners/decoders; conversion/sandbox | Freeze neutral types first. Persistence artifacts precede adapter wiring; source/conversion handoffs precede temporal-e2e acceptance. |
| M2 | Parts/body grammar; identity/effects/subjects grammar, within their listed files | M2.1 hands conversion files to M2.2 before edits. Legal effects wait for typed trees. M2.4 integrates shared registration/configuration and generated persistence after both grammar handoffs. |
| M3 | Release storage; chunk/batch controller; runtime adapters; evaluation/publication | Freeze recipe/chunk contracts before embedding. Evaluation waits for complete candidate; sealing waits for every gate. |
| M4 | Host/IaC; evidence/MCP; auth/recovery; harnesses, within their listed paths | M4.2 owns production promotion adapters. M4.3 integrates shared serving files after M4.2. M4.4 alone edits the smoke workflow; destination smoke waits for host, auth, and release readiness. |
| M5 | Private review API; Vietnam approval/audit; TLS/CRL harness, within their listed paths | M5.1 alone edits writer application/workflows and the review workflow; M5.2 integrates shared review configuration after handoffs. The smoke-workflow owner imports M5.3's job after M4.4. Reviewed correction and live drill wait for all three and M2 originals. |
| M6 | Website/reader; continuation/downloads; plugin/copy, excluding shared files | M6.1 integrates routes/configuration after M6.2. M6.4 alone edits cutover scripts and the smoke workflow after prior handoffs; launch waits for final release acceptance. |

A worker handoff names changed paths, contracts, generated artifact provenance, hosted results, missing inputs, and unresolved findings.
Do not start a dependent task from an uninspected worker report. Milestone review covers the integrated diff once; retain findings and fix confirmation in its evidence record.

## Settled and open owner decisions

O1-O6 are settled as listed; foundation source implements part of O1-O3. O7-O28 are open recommendations. Resolve each open decision in its owning record before the named milestone needs it. These decisions do not approve either full design.
Settled VNGCloud Vietnam writer and storage, Temporal Cloud, Hugging Face Jobs, Vision, private-CA access, Vietnam residency, single runtime, and dedicated serving host decisions are not reopened.

| ID | Decision and recommended default | Owning record | Needed by |
|---|---|---|---|
| O1 | **Decided as recommended.** Approve established domain paths with narrow core/module contracts. | country-modules: Owner decisions 1 | M0 |
| O2 | **Decided as recommended.** Approve compiled state, deployment allowlist, writer epoch, local admission/revocation and restore fence. | country-modules: Owner decisions 2 | M0 contract; M4 activation |
| O3 | **Decided as recommended.** Approve offline baseline harness over frozen snapshots/retained adapters. | country-modules: Owner decisions 3 | M0 |
| O4 | **Decided as recommended.** Approve seven VN subject keys and primary/context scope; keep ordinary insurance claims out. | vietnam: Approved decisions 1 | M1 admission; M2 subjects |
| O5 | **Decided as recommended.** Permit four-collector launch with direct SBV disabled and explicit coverage gaps if floors pass. | vietnam: Approved decisions 2 | M2 qualification; M6 |
| O6 | **Decided as recommended.** Approve source cadence and one-request/second, one-in-flight host budget, subject to stricter source rules. | vietnam: Approved decisions 3 | M1 fixtures; M6 schedules |
| O7 | Approve 32 processing/256 open workflows, fan-out ceilings, 24-hour reminder and seven-day expiry. | corpus-workflows: Owner questions 1–2 | M1/M2 |
| O8 | Approve $25/month Temporal alert; daily sync, weekly validity builds, 24-hour deadlines; owner-only gap approval/promotion, with scoped delegation only if granted. | corpus-write-path: Owner questions 1–3 | M1 contracts; M5 authority; M6 schedules |
| O9 | Approve proposed hard gates and 80-document/two-human-annotator qualification set. | document-conversion: Owner questions 1 | M2 |
| O10 | Approve pinned UVConverter helper and MuPDF/LibreOffice license/packaging disposition. | document-conversion: Owner questions 2 | M2 images; M3 publication |
| O11 | Require independent technical checking before owner critical-change adjudication; legal-meaning approval also needs the review spec's independent human check. | document-conversion: Owner questions 3; corpus-review: Roles/authority | M2 contracts; M5 acceptance |
| O12 | Permit labelled file-only/OCR-only discovery hits; strict current-only needs resolved validity. | document-parts: Owner questions 1 | M3 |
| O13 | Keep browser-local dossiers without server retention holds; show availability deadline. | document-parts: Owner questions 2 | M6 |
| O14 | Approve one-year archives beyond the settled online minimum. | corpus-database: Owner questions 1 | M3 storage; M4 recovery |
| O15 | Include unknown/unreviewed subjects in a separate possible-match set; strict filters disclose limited coverage. | corpus-database: Owner questions 2 | M3 |
| O16 | Approve no loss of activated manifests/identity keys/acknowledged holds, four-hour recovery, and ≤24 hours unpublished work loss. | corpus-database: Owner questions 3 | M3 recovery contract; M4 drill |
| O17 | Approve 500 ms short-query p95 at stated load and measured reservations. | embedding-models: Owner questions 1 | M3 runtime; M4 capacity |
| O18 | Approve bake-off spend ceiling $200 and Vietnam-only winner policy; stop after pilot if projected cost exceeds ceiling. | embedding-models: Owner questions 2–3 | M3 |
| O19 | Accept five-minute ordinary-auth recovery loss/four-hour recovery time, with durable blocks and fresh credentials. | vngcloud-hosting: Owner questions 1 | M4 |
| O20 | Approve seven-day local IP logs, 30-day redacted operations, Vietnam mail relay, and separate auth audit/deletion retention. | vngcloud-hosting: Owner questions 3 | M4 |
| O21 | Choose old Vietnam endpoint policy and exact cutover targets. Prefer 30-day website reconnection notice and explicit MCP retirement, without forwarding tokens/questions. | vngcloud-hosting: Owner questions 2; WEBSITE: Open questions 1 | M6 |
| O22 | Approve measured download limits. Start 50 MiB, two streams/account, eight overall, 120 seconds/stream. | vngcloud-hosting: Corpus delivery; document-parts: Files/table fidelity | M4 contract; M6 route |
| O23 | Approve scoped delegation after owner-only launch; keep auto-application disabled, including subjects/metadata. | [Corpus review plan, decisions 1–2](corpus-review.md#remaining-owner-decisions) | M5 |
| O24 | Approve one-use application permits with ≤60-second in-flight window after adversarial review. | [Corpus review plan, decision 3](corpus-review.md#remaining-owner-decisions) | M5 |
| O25 | Approve P0/P1/P2 staffing targets, 20% backlog/audit reserve, next-daily-candidate target, optional 30-day exposure buckets, and audit lifetime plus ≥one year after last reference. Resolve deletion/backup retention. | [Corpus review plan, decisions 4–5](corpus-review.md#remaining-owner-decisions) | M5 |
| O26 | Configure branch/check/tag/environment protections; authorize exact publisher identity, production refs, signed artifacts, and reviewed infrastructure/deploy operations. | CI audit settings; hosting: Infrastructure/deployment | M0 settings; M3 publishing; M4 apply |
| O27 | Supply approved plugin demo access and decide whether Directory approval gates broad plugin launch. | WEBSITE: Open questions 2–3 | M6 |
| O28 | Approve exact release, destination, expected selection, source schedules, and public launch after all evidence. | corpus-write-path: Hosting/delivery; hosting: Cutover | M6 |

Other launch gates are evidence, not invitations to reopen settled choices: direct-SBV endpoints/access rules; residency screening; official original hashes and six-clause identities; hard-font/table fixtures; qualified query thresholds; provider reconciliation; actual model recipes/licenses; VNGCloud placement, encryption, identity and access, network, storage, and backup behavior; measured restore, capacity, and cost; CRL verifier/MFA compatibility; and real Google/client compatibility.
Keep `sbv` disabled until qualified. O5 decides whether its absence is an acceptable launch gap. Unavailable mandatory evidence leaves the relevant milestone in progress.
