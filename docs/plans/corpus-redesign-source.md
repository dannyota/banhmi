# Corpus Pipeline Redesign Source Implementation Plan

**Goal:** Implement the source boundaries approved in
[`docs/design/corpus-pipeline-redesign.md`](../design/corpus-pipeline-redesign.md) while preserving
live data, deployed behavior, exact citations, country isolation, and immediate auth revocation.

See the paired [source contracts](../specs/corpus-redesign-source.md).

**Baseline:** `5eb2c04b831b1a4184d115b228eb2c1bc1ef63b6`

**Method:** Follow [AGENTS.md roles and dispatch](../../AGENTS.md#roles-and-dispatch) before
dependent work starts.

## Source status

The source pass and independent static review are complete. A checked item records reviewed source,
not complete runtime qualification. The [Goose migration plan](goose-migrations.md#evidence-and-remaining-gates)
owns migration-backed generation, artifact, and replay status. Ordinary Go build/vet/short tests pass with this source in
[master run 38237880056](https://github.com/dannyota/banhmi/actions/runs/38237880056), attempt 1,
at `dfa35280fc71cd4af61d0e46f0ac4630c41b004c`. These checks do not establish corpus persistence,
provider integration, captured-history replay, or real-document behavior. Adapter integration,
captured Temporal history replay, evaluation, and deployment remain pending here.

| Area | Source state | Remaining gate |
|---|---|---|
| Evidence and serving | Static review and ordinary hosted Go checks passed | Deployed transport/client and real-corpus evidence |
| Retrieval | Independent static review passed | Fixed-snapshot and country evaluation |
| Inference admission | Independent static review passed | Tagged ONNX build and resource checks |
| Current batches | Independent static review passed | Database, provider, and native-library tests |
| Durable runner contracts | Independent static review passed | Freeze build-independent intake ports in [M1.1](vietnam.md#m11-neutral-intake-contracts), then fresh-writer adapters and transactions in [M1.2–M1.6](vietnam.md#m12-admission-ledger-and-policy) |
| Corpus persistence definitions | Reviewed legacy generated stores and synthetic schema validation; recorded evidence remains in the [Goose migration plan](goose-migrations.md#evidence-and-remaining-gates) | Legacy definitions stay frozen. [Schema reset](vietnam.md#s-schema-reset) establishes fresh chains; [M1.2–M1.6](vietnam.md#m12-admission-ledger-and-policy) implement new writer persistence, not adapters over legacy stores |
| Release domain | Independent static review passed | Fresh writer catalog, release projection and serving control adapters in [M3.1](vietnam.md#m3-candidate-releases-and-retrieval-parity), using the [database homes](../specs/corpus-database.md#migration-chains-and-generated-stores) |
| Temporal SDK pilot | Independent static review passed; ordinary Go CI covers the default test suite | Hosted Temporal execution and captured-history replay |
| Hosted generation lane | Independent static review passed | Keep [Goose evidence](goose-migrations.md#evidence-and-remaining-gates); [CI reduction](vietnam.md#c-ci-reduction) replaces the lane and [schema reset](vietnam.md#s-schema-reset) adds fresh-chain replay and generated child packages |

Follow [AGENTS.md Git rules](../../AGENTS.md#git) for source worktree integration.
Further persistence wiring and corpus-specific CI dispatch follow the owning milestone and
verification rules. No cloud change or deployment is accepted by this source pass.

## Boundaries

- Preserve the five MCP tools: `guide`, `corpus_status`, `quality_gaps`, `search`, and `document`.
- Preserve existing JSON fields, citations, validity, relations, provenance, confidence, and gaps.
  Release identifiers are additive.
- Preserve the compatibility fallbacks for unknown search detail and document include names. Apply
  raw byte and count limits before normalizing those fields or selecting their effective defaults.
- Keep one database boundary per country. Never fall back to another country on routing, lookup,
  release resolution, or workflow failure.
- Keep account state, authorization epochs, grants, and blocks in the mutable auth database. Corpus
  snapshots and release caches never supply auth state.
- Reject oversized reads before embedding, database access, tokenization, or large allocation.
- Use one formatted dense query vector for primary, non-current, and related retrieval. Keep BM25
  input, citation rules, and quality floors unchanged.
- Bound corpus work by item count, input bytes, pages, elapsed time, and concurrency. Record rejected
  or terminal inputs as visible gaps.
- PostgreSQL owns leases, fencing, idempotency, source retry eligibility, external-job identity, and
  completeness. Temporal owns phase order, timers, activity retries, and cancellation.
- Workflow values contain IDs, hashes, cursors, counts, times, fences, and object references only.
  Workflow code performs no database, filesystem, provider, or source I/O.
- The completed source pass edited SQL under `sql/`; its persistence definitions are being carried
  into reviewed Goose migrations. Do not hand-edit `pkg/store/` or trusted migration history. Do
  not call a future sqlc method before hosted generation produces it.
- Add test source in each task. GitHub runs execution checks. No local build, test, lint, formatting,
  generation, install, database, pipeline, provider, cloud, or continuous integration dispatch belongs
  to this source pass. Follow [AGENTS.md Git rules](../../AGENTS.md#git) for integration.

## Fixed contracts

The source-pass contracts live in the paired [spec](../specs/corpus-redesign-source.md).

## Ownership and order

Tasks 1, 3, 4a, 4b, and 5 may run in parallel. Task 2 follows the Task 1 interface freeze. Task 6
follows review of Task 1's neutral release interfaces and has no runner dependency. Task 8 follows
Tasks 4b and 6. Task 7 waits for hosted schema and sqlc
generation.

| Task | Owned paths |
|---|---|
| 1. Evidence and serving boundary | `internal/evidence/api/`, `internal/evidence/service/`, `internal/app/serve/`, `pkg/base/sourceurl/`, `pkg/base/config/`, focused edits in `pkg/mcp/`, `pkg/web/`, `pkg/app/`, `pkg/ingest/vbpl/detail.go`, `cmd/server/`, and `cmd/mcp/` |
| 2. Retrieval defense and vector reuse | `pkg/rag/retrieve/` and focused tests only |
| 3. Query inference admission | `pkg/rag/embed/onnxembed/` and `cmd/embedder/` |
| 4a. Bounded current batch work | `pkg/pipeline/embed_all.go`, `pkg/pipeline/ocr_all.go`, their legacy callers in `pkg/pipeline/run_all.go` and `reprocess_local_test.go`, focused helpers and tests, `cmd/pipeline/main.go` and tests, `internal/corpus/run/batch.go`, `batch_test.go`, and a serialized PDF page counter in `pkg/extract/fitz/` |
| 4b. Durable runner contracts | other new files under `internal/corpus/run/` |
| 5. Immutable persistence definitions | Existing query sources, reviewed Goose migrations, and `internal/corpus/schematest/` |
| 6. Release domain | `internal/corpus/release/` and `internal/corpus/blob/` |
| 7. Generated persistence rollout | deferred adapters under `internal/corpus/postgres/` and `internal/evidence/postgres/`, then focused handoff edits in Task 1 and Task 4 paths |
| 8. Temporal SDK pilot | `internal/corpus/workflow/temporal/`, Temporal-only config source, and `go.mod` |

A bounded implementer owns `go.sum`, `sqlc.yaml`, Goose migrations and generated stores, GitHub workflows,
and other shared implementation files under [roles and dispatch](../../AGENTS.md#roles-and-dispatch).
The primary coordinates, inspects and imports artifacts, verifies evidence, and performs Git operations.

### Task 1: Evidence API and lean serving composition

**Create:** `internal/evidence/api/`, `internal/evidence/service/`, `internal/app/serve/`,
`pkg/base/sourceurl/vbpl.go`, and focused tests.

- [x] Move all five operation payloads to `internal/evidence/api`. Leave compatibility aliases in
  `pkg/mcp`. Add the release fields and neutral release contracts above.
- [x] Inject `StartupMetadata` into MCP registration so guide text and tool schemas preserve current
  coverage, issuer, document-type, indexed-language, and binding-language descriptions without
  importing jurisdiction or corpus worker packages.
- [x] Define `ReadLimits` in `pkg/base/config.RetrieveConfig` with configurable provisional safety
  caps:
  `MaxQueryBytes=4096`, `MaxResults=50`, `MaxRelated=50`, `MaxFilterValues=20`,
  `MaxFilterBytes=256`, and `MaxDocumentChunks=50`. Load YAML and environment overrides. Reject
  non-positive values during startup. Preserve current document default 20 and maximum 50, current
  related default 8, and existing zero-value request defaults. Use YAML keys `max_query_bytes`,
  `max_results`, `max_related`, `max_filter_values`, `max_filter_bytes`, and `max_document_chunks`
  under `retrieve`; use the same uppercase names prefixed with `BANHMI_RETRIEVE_` for environment
  overrides.
- [x] Enforce read limits in the shared evidence service before embedding or pool access.
  Add exact-boundary and one-over service tests proving rejection calls neither dependency.
- [x] Implement shared operations without HTTP, MCP SDK, OAuth, website, workflow, crawler, OCR, or
  bulk embedding imports. Make MCP and website adapters depend on `api.Operations`.
- [x] Move the pure VBPL file-listing URL builder to `pkg/base/sourceurl`. Keep
  `vbpl.FilesListingURL` as a delegate. Corpus source and serving may both import the neutral helper.
- [x] Build `internal/app/serve` with only config, logging, corpus read pool, query embedder, evidence,
  MCP, website, identity, OAuth, and auth pool dependencies. Move every `cmd/server` and `cmd/mcp`
  callsite. Keep corpus worker construction outside the serving graph.
- [x] Load optional `BANHMI_EMBED_QUERY_ENDPOINT` in serving composition as the remote client's base
  URL, for example `http://embedder:8089/query`. The existing client appends `/embeddings`, producing
  `/query/embeddings`. When unset, preserve the current base URL and third-party compatibility.
- [x] Keep live bearer and website-session checks before evidence calls. Preserve unknown-country
  rejection, routing, public routes, and response payloads.
- [x] Add parity test source for tools, startup metadata, citations, unknown validity, non-binding text,
  relations, gaps, errors, and pin closure. Add an import-graph test for serving dependencies.

**Static review:** Compare JSON tags and startup schema descriptions to baseline, trace all callsites,
and confirm current auth checks and country routing remain intact.

### Task 2: Retrieval defense and one dense query vector

**Modify:** `pkg/rag/retrieve/retrieve.go`, `pkg/rag/retrieve/evidence.go`, and focused tests only.

- [x] Defensively reject retrieval counts or filter shapes above active Task 1 limits before embedder
  or pool access. Task 1 remains the owner of API and service validation.
- [x] Compute one formatted dense query vector per vector or hybrid request. Pass it unchanged to
  primary, non-current, and related dense retrieval.
- [x] Keep BM25 input independent. BM25 mode makes no embed call. Vector failure remains an error.
- [x] Add exact-boundary and one-over test source. Rejected paths call neither embedder nor database. A
  counting embedder proves one call for a hybrid request with all dense passes.
- [x] Add focused source tests for vector reuse, identifier-scoped search, validity, relations,
  out-of-domain hit preservation, diacritic restoration, and abbreviation expansion.
- [ ] Extend and execute fixed corpus fixtures across all six citation vocabularies in hosted
  evaluation before rollout. Existing jurisdiction behavior remains a compatibility requirement.

**Execution gate:** Hosted client and resource measurements must approve read caps before deploying
this source; the provisional values make no capacity claim. Fixed-snapshot retrieval evaluation must
pass before production uses vector reuse.

### Task 3: Query inference admission with batch compatibility

**Create:** ONNX admission source and focused tests.

**Modify:** tagged ONNX files and `cmd/embedder/` only.

- [x] Preserve `embed.Embedder` and `Embed(ctx, []string)`. Keep `POST /embeddings` as the bounded
  batch endpoint used by `cmd/embed-backfill` and other maintenance clients.
- [x] Add `POST /query/embeddings` for exactly one text. Task 1 owns the explicit query endpoint
  override. Do not change the root embed client, the `Embedder` interface, or backfill callers.
- [x] Add `AdmissionConfig{MaxInputBytes, MaxBatchTexts, MaxQueued, MaxConcurrent int}` with positive
  startup validation. HTTP admission happens before the embedder call.
- [x] Use one finite admission controller across batch and singleton HTTP paths. Bound both active
  and queued calls inside ONNX `Embed` before tokenization, padded slices, tensor creation, or
  session execution. Both in-process serving retrieval and HTTP paths construct this admitted ONNX
  embedder. Serving accepts positive `BANHMI_EMBED_MAX_QUEUED` overrides.
- [x] Map query overload to HTTP 503 with `Retry-After`. Preserve batch response JSON and existing
  auth behavior.
- [x] Add test source for query and batch limits, full queue, cancellation, no tokenization or tensor allocation on
  rejection, slot release, in-process admission, and unchanged legacy batch routing.

### Task 4a: Bound current OCR and dense batch work

**Create:** focused batch helpers and tests beside `pkg/pipeline/embed_all.go` and `ocr_all.go`, plus
`internal/corpus/run/batch.go` and `batch_test.go`.

**Modify:** `pkg/pipeline/embed_all.go`, `pkg/pipeline/ocr_all.go`, `cmd/pipeline/main.go`, and focused
command tests. Update existing legacy callers to supply positive budgets and reject incomplete
shards before advancing a stage.

- [x] Make current whole-corpus OCR and embedding require positive item, byte, page, elapsed-time,
  and concurrency bounds. Page current rows by durable ID cursor and acquire capacity before large
  allocation or provider submission.
- [x] Keep the current synchronous provider path to one bounded shard per invocation. This legacy
  path remains unfenced and cannot reconcile a submission after process loss. Task 8 defines
  prepare, submit, check, and stream-apply ports with stable submission keys. Task 7 supplies their
  durable storage and provider adapters before they can replace the legacy path.
- [x] Preserve MuPDF serialization and exact embedding cache provenance.
- [x] Stop swallowing OCR and embedding item errors in `cmd/pipeline`. Return explicit counts and a
  non-nil command error when failures or rejected inputs remain.
- [x] Add test source for each budget, partial pages, multiple failures, malformed inputs, provider errors,
  and command error propagation.

**Static review:** Find no unbounded whole-corpus OCR or dense load, confirm capacity precedes
allocation, and trace every failure to returned state.

The source defaults are provisional limits, not measured capacity:

| Path | Items | Input bytes | Pages | Elapsed time | Concurrency |
|---|---|---|---|---|---|
| Dense shard | 25,000 chunks | 64 MiB total; 32 MiB per input | One logical batch | 15 minutes | Two provider jobs |
| OCR shard | 50 documents | 512 MiB of admitted files | 2,000 PDF pages | 30 minutes | At most eight requests |
| Generic ID shard | 200 IDs | 64 MiB of declared metadata | 2,000 logical records | 15 minutes | One to four workers |

Commands use one shard and report a same-stage `-after-id` cursor when work remains. Forced
`-run-all` is rejected because its selectors would restart the first shard. Fetch processes at most
20 claimed artifacts per source invocation and honors `-max`.

Generic ID metadata uses eight bytes and one logical record per ID. It does not measure parser
input. OCR checks file bytes before serialized PDF page counting, but a missing file can download
before that check. Pre-download admission, native hard resource limits, and real provider behavior
remain hosted rollout gates. Existing provider submissions remain unfenced and nonresumable.

### Task 4b: Define durable runner contracts

**Create:** the fixed contracts and pure fake-backed tests in new `internal/corpus/run/` files other
than `batch.go` and `batch_test.go`.

- [x] Add the fixed stage, execution, shard, owner, fence, `UnitOfWork`, executor, and lease contracts.
  Implement `run.Direct` over injected ports. Keep durable adapters unwired until Task 7 and add no
  in-memory production substitutes.
- [x] Use the existing `Discover`, `Fetch`, `Extract`, `OCR`, `Normalize`, `Index`, `Dense`,
  `Lexical`, `Evaluate`, and `Publish` names and lowercase values. Add `Activate` after `Publish`.
- [x] Label existing stage mutations as legacy unfenced behavior. Do not route Temporal or claim
  durable build safety through them. Only Task 7's `UnitOfWork` may accept new build-scoped writes.
- [x] Add contract test source for build, country, owner, fence, output references, result, and
  idempotency key reach `UnitOfWork.Apply` together. Use fakes for response-loss replay and
  stale-fence rejection without claiming database behavior. The unit of work reads execution only
  from `Result.Shard.Execution`, so no duplicate execution fields can conflict.

### Task 5: Immutable persistence definitions and meaningful database tests

**Modify:** source-pass SQL under `sql/{config,ingest,bronze,silver,gold}/`; its persistence
definitions now transition to reviewed Goose migrations under the paired migration plan.

**Create:** disposable PostgreSQL integration test source under `internal/corpus/schematest/`.

- [x] Add run, build, stage result, external job, scheduler lease, and content-addressed blob records.
  Use one surrogate primary key plus natural unique constraints for idempotency and fencing.
- [x] Add source records, immutable observation revisions, artifacts, and links beside compatibility
  tables. Never overwrite accepted revision inputs.
- [x] Add stable document identity, assertions, legacy mappings, document and provision revisions,
  and reviewed merge or split evidence. Separate observation, processing, and legal effective times.
  Retain exact native citation and source anchor on every provision revision.
- [x] Add taxonomy releases and reviewed assignments with evidence, confidence, method, unknown
  state, and context role.
- [x] Add build-scoped chunk sets, complete dense recipe identity, lexical revision, and published
  evidence projection.
- [x] Add release, manifest, activation, pin, and retention records in the country writer database,
  outside replaceable snapshots. Bind every recipe, source blob, schema, evaluation artifact, and
  compatible server or embedder image digest.
- [x] Add query source with no Go caller. Keep cross-schema references as validated scalar IDs.
- [x] Add disposable PostgreSQL test source for immutable constraints, duplicate idempotency keys,
  stale fences, ambiguous legacy IDs, incomplete releases, activation compare-and-set races, and
  pin retention. Tests target actual migrated tables and queries.
- [ ] Prove atomic output plus stage result through Task 7 adapters and country isolation through
  separate disposable databases. Run schema tests after hosted migration replay and sqlc generation.

### Hosted generation lane and gate

The [Goose migration plan](goose-migrations.md#evidence-and-remaining-gates) records completed
hosted generation, independent artifact review, and the imported stores. Future changes retain
source review before hosted generation and independent artifact review before import. Assign
workflow changes to a bounded implementer under [roles and dispatch](../../AGENTS.md#roles-and-dispatch).
The `generation` job in [reusable checks](../../.github/workflows/reusable-checks.yml) uploads the
generated trees under `pkg/store/` as its review artifact and fails on drift; see the
[hosted check matrix](../../instructions/verification.md#hosted-check-matrix).

The primary inspects and imports each independently reviewed artifact. Immutable persistence,
scheduler fencing, release activation, and production worker wiring remain pending under the
Vietnam milestone order and consumer-contract approval.
Missing generated methods do not authorize raw replacement SQL or generated-file edits.

### Task 6: Release and publication domain

**Create:** `internal/corpus/release/` and `internal/corpus/blob/blob.go` with pure tests.

- [x] Define catalog, validation, activation, retention, pin, and blob ports with domain types only.
- [x] Validate country, manifest completeness, citation and provenance parity, accepted gaps, dense
  and lexical coverage, recipes, evaluation artifact, and image compatibility. Source observation,
  document revision, provision revision, and chunk counts must all be positive before publication.
- [x] Implement compare-and-set activation with prior selection and reconciliation state. Readiness
  failure leaves selection unchanged. Release and activation contracts carry the build UUID.
  Activation must match the requested country, release, and build before readiness or selection
  mutation. Publication transitions preserve the release's build identity.
- [x] Implement in-flight pin counts and retention for active, rollback, named, in-flight, and backup
  releases. Closing the last neutral API pin permits drain.
- [x] Provide an adapter to Task 1's `api.ReleaseResolver` for later composition. Evidence imports
  only the neutral API. Task 7 owns production wiring.
- [x] Add test source for wrong country, incomplete manifest, readiness failure, activation races, expired release,
  close idempotency, pool drain, and retention.

### Task 7: Generated persistence rollout, deferred

**Start condition:** [CI reduction](vietnam.md#c-ci-reduction) and [schema reset](vietnam.md#s-schema-reset)
establish the fresh writer, release and control chains in the
[corpus database spec](../specs/corpus-database.md#migration-chains-and-generated-stores).
Keep the [Goose migration plan's recorded evidence](goose-migrations.md#evidence-and-remaining-gates)
and imported legacy stores as legacy evidence only. They do not authorize new adapters against
`sql/`, `pkg/store/` or frozen `deploy/migrations/`. Review each fresh-chain generation artifact
before adapter wiring; require zero drift after import. Queries and adapters use the parent store
path; hosted-generated Go uses its generated child package.

[M1.1](vietnam.md#m11-neutral-intake-contracts) freezes the new intake ports before persistence.
[M1.2–M1.6](vietnam.md#m12-admission-ledger-and-policy) own writer files 00002–00005 after reset's
00001, ledger/capture under `internal/corpus/store/intake`, revisions/provenance under
`internal/corpus/store/document`, and final cross-store acceptance with no new schema.
[M1.14](vietnam.md#m114-hosted-intake-integration-and-exit-evidence) composes reviewed adapters
and proves accepted intake before any build exists. M2.4 adds writer 00006–00008. M3.1 adds writer
00009–00010, `internal/corpus/store/{index,catalog,projection}` and
`internal/evidence/store/{reader,control}` on the fresh release/control chains. Later milestones
own provider and serving wiring; this deferred task is a handoff map, not one implementation PR.
These store homes supersede the historical Task 7 PostgreSQL adapter paths in the ownership table.

The adapters preserve these identity rules. Reviewed ports alone do not prove persistence:

- For build work, set SQL `shard_key` to `run.IdempotencyKey(shard)`. Intake uses the M1.1
  execution-scope identity and never mints a fake build or requires a build ID.
- Resolve `ManifestRef` through immutable artifact metadata to the SHA-256 digest of the exact
  registered input manifest bytes. Never hash an opaque reference string as the input digest.
- Persist an ordered output manifest using a versioned canonical encoding. Hash those exact bytes
  for `output_manifest_digest`.
- Encode `result_digest` with the immutable fields compared by `SameImmutablePayload`, adapted to
  the execution scope. Exclude mutable owner and fence. Normalize times to UTC and fix field and
  output ordering in the encoding version. Changed immutable payload under one key fails.
- Resolve external request and result digests from persisted manifest bytes through the same
  registry. Do not invent a result digest before a result exists.
- Lock the lease and verify country admission epoch, execution scope, owner and current fence,
  input digests, recipe, budget and decision revision. Verify build identity only for build work.
  Resolve registered cross-schema keys, then commit outputs, result, eligibility and required
  delivery records atomically. Revision origin uses NOT NULL `intake_execution_id`, with no build column.
- Add opaque-reference, changed-payload, equivalent-time, ordered-output and stale-fence tests
  before wiring persistence.

- [ ] Implement leases, UnitOfWork, manifests/results, blobs, identity and acceptance through
  [M1.2–M1.6](vietnam.md#m12-admission-ledger-and-policy), then prove response loss, lease expiry,
  ambiguous IDs, country isolation and transaction behavior on disposable hosted PostgreSQL.
- [ ] Reconcile provider jobs by stable submission key in the owning M2/M3 controller slices.
  Unsupported reconciliation records an uncertain submission and stops. Prove uncertain-job handling
  on disposable hosted inputs before provider wiring.
- [ ] Implement durable catalog, activation, pins and retention on the fresh M3.1 persistence;
  prove activation races, rollback and pin drain, then wire release resolution in M4 serving.
- [ ] Resolve databases only from registry-approved country and release records.
- [ ] Hand `cmd/pipeline` from Task 4's bounded synchronous path to `run.Direct`, required build ID,
  persisted lease and durable UnitOfWork in the later command-cutover slice. Direct and Temporal
  cannot own one build. M1 intake does not perform this handoff.
- [ ] Wire additive release identifiers through later serving integration, after its evidence gates.

### Task 8: Actual Temporal SDK pilot source

**Start condition:** Tasks 4b and 6 are reviewed. Task 8 may use fake ports before Task 7, but it does
not create an executable production worker.

- [x] Verify the official `go.temporal.io/sdk` module, released version, and used APIs from upstream.
  Add the verified requirement to `go.mod`. Leave `go.sum` to hosted tidy; invent no checksum.
- [x] Implement the actual SDK workflow, activities, durable timers, heartbeats, cancellation,
  Continue-As-New, versioning, worker registration function, test environment, and replay APIs under
  `internal/corpus/workflow/temporal/`.
- [x] Use workflow ID `corpus-refresh/<country>/<build-id>`. Carry country, build, current fence,
  phase, cursor, counts, cutoff, and object references across Continue-As-New.
  The activation request and returned activation summary must match the workflow's build UUID.
- [x] Order the fixed stages from `Discover` through `Activate`. Use ledger `NextEligible` for durable
  waits. Activity retry owns worker or infrastructure failure only.
- [x] Use short status activities for OCR and dense waits. Cancellation records jobs and attempts
  provider cancellation without claiming to undo completed work.
- [x] Add SDK test source for response loss, delayed retry, fence loss, cancellation, uncertain
  submission, activation failure, and history rollover. Register the real SDK replay API.
- [ ] Capture hosted workflow histories and execute replay fixtures. No captured history is claimed
  by the source test suite.
- [x] Add no `cmd/corpus-worker` until Task 7 supplies durable adapters and scheduler fencing.

## Current state and remaining gates

Static review and ordinary hosted Go checks cover the source core. They do not establish the
persistence and real-corpus evidence below.

Completion has two parts:

- **Source core complete:** serving boundary, read admission, vector reuse, query and batch inference
  admission, bounded current OCR and dense work, runner contracts, reviewed persistence definitions, release domain,
  and actual Temporal SDK pilot source passed static review.
- **Persistence rollout pending:** durable adapters, executable Temporal worker, adapter database
  integration, captured Temporal replay, retrieval evaluation, candidate corpus,
  publication, activation, rollback, other-country gates, deployment, and hosted verification remain
  unclaimed until their execution evidence exists. The [Goose migration plan](goose-migrations.md#evidence-and-remaining-gates)
  owns reviewed generated stores and completed synthetic schema replay.

The SDK requirement is `go.temporal.io/sdk v1.49.0`, checked against the
[official release](https://github.com/temporalio/sdk-go/releases/tag/v1.49.0) and its
[module declaration](https://github.com/temporalio/sdk-go/blob/v1.49.0/go.mod). Hosted auth generation
reports no module drift on the recorded master run. Replay APIs and test source do not substitute for a
history captured from a hosted execution.
