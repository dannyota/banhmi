Proposal, not implemented. This continuation of the [corpus database spec](corpus-database.md) owns the closed vocabularies for writer file `00003_sources.sql`. The [Vietnam plan](../plans/vietnam.md#m13-source-accounting-and-bronze) owns delivery.

# Corpus database sources

## Vocabulary rules

Add `SourceWindowState`, `DiscoveryPageState`, `ArtifactRequestState`, `ArtifactRole`, and `RetentionOwnerKind` as typed strings with `Valid()` in `internal/corpus/model`, following [AcceptanceState](../../internal/corpus/model/intake.go). Preserve frozen contracts: [ArtifactInventoryEntry.Role](../../internal/corpus/model/observations.go) stays a string; the adapter checks membership before persistence. SQL CHECK sets must match the constants. Change constants, constraints, and the object inventory together under [chain mutability](corpus-database.md#chain-mutability-and-verification). Hosted M1.3 schema checks must cover every member, unknown values, and state-dependent nullability; adapter checks must cover transitions and stale fences.

Reuse [source/api.DiscoveryMode](../../internal/corpus/source/api/ports.go) for window mode and [model.RequestPurpose](../../internal/corpus/model/observations.go) for purpose. Do not move or duplicate their constants. Tables, identities, indexes, and immutable receipts remain owned by the [intake ledger](corpus-database.md#intake-ledger-and-revision-acceptance) and [access paths](corpus-database.md#persistence-dependencies-and-access-paths).

**Decision:** Keep one checked, non-null state per accounting row and the value sets below. Make `incomplete` resumable without resetting its window. Preserve failed-page evidence and retain blobs until every dependency has protection.
**Why:** The [workflow contract](corpus-workflows.md#source-intake-and-child-lifetime) resumes incomplete windows and requires launch accounting. Terminal `incomplete`, blanket page failure, and unconditional edge removal would defeat recovery or erase evidence. [Writer 00002](../../deploy/corpusmigrations/writer/00002_admission_and_ledger.sql) binds retry time to the count `pending_retry > 0`, not to an outcome name; request eligibility below is a separate proposed constraint.
**Rejected:** Timestamp-derived states, PostgreSQL enums, lookup tables, reopening terminal evidence, and resetting budgets on recovery.
**Detail:** Only listed transitions are allowed. Terminal rows permit identical receipt replay, never mutation. SQL checks enforce row shape; fenced adapters enforce transitions, cross-row conditions, and [atomic ledger acceptance](corpus-database.md#intake-ledger-and-revision-acceptance). The [workflow contracts](corpus-workflows.md#workflow-contracts) name all six applies used here: `ApplyDiscoveryPage`, `ApplyCapture`, `ApplyConversion`, `ApplyEvidence`, `ApplySnapshot`, and `SealRelease`. Receipt, claim, and recovery substeps below are proposed adapter operations under those owners, not new workflow activity names.

## Source window state

`ingest.source_window.state`. `SourceSync` owns it under the `source_window` lease selected by [IntakeSubject](../../internal/corpus/run/scopes.go). Freeze the window inputs once. Hold the committed cursor at its initial value until `complete`. `complete` and `failed` are terminal. A replacement window gets a new key and preserves its predecessor's accounting.

| Value | Meaning | Enters from | Transition owner and condition |
|---|---|---|---|
| `frozen` | No page is accepted; discovery can proceed. | Insert; `incomplete` | `SourceSync` creates the window, or resumes it after its blocker clears with no accepted page. |
| `progressing` | At least one page is accepted; discovery or launch accounting can proceed. No page has an intrinsic failure. | `frozen`, `incomplete` | `ApplyDiscoveryPage` accepts a page, or recovery resumes remaining work. Stay here while more accounting is possible. |
| `complete` | Every planned page is accepted, the final page proves exhaustion, no discovery continuation or unresolved coverage finding remains, and required child launches are acknowledged. | `frozen`, `progressing`, `incomplete` | `ApplyDiscoveryPage` acceptance or reconciliation satisfies all conditions and commits the final `NextCursor`. |
| `incomplete` | Coverage or launch accounting is unresolved and cannot currently proceed. No page has an intrinsic failure. Keep a non-empty reason. | `frozen`, `progressing` | `ApplyDiscoveryPage` records a coverage blocker, exhausted budget, deadline, or unresolved launch accounting. |
| `failed` | A page has an intrinsic terminal failure. Keep its reason. | `frozen`, `progressing`, `incomplete` | `ApplyDiscoveryPage` records the page failure and window failure atomically. |

Apply failure before incompleteness, and completion before ordinary progress. Require a non-empty reason exactly for `incomplete` and `failed`; clear it on resumption and retain the earlier outcome receipt. On window failure, mark only nonterminal pages `failed` with `window_closed`; preserve accepted pages and existing failure reasons. Incompleteness never fabricates a page failure. Resume the same window only within its frozen allowances; a spent request budget or deadline forbids further fetches. Receipt and launch reconciliation may continue without new fetches. Lease loss or cancellation blocks the old owner; a newly admitted owner reconciles under a fresh fence and cannot erase cancellation or reset budgets. [Closure](corpus-write-path.md#closure-accounting-and-evidence) owns late document acceptance and snapshot coverage. Open question 1 covers source-window gap projection.

## Discovery page state

`ingest.discovery_page.state`. Purpose is `listing` or `number`. `SourceSync` records request planning and staged receipts under its window lease; `ApplyDiscoveryPage` alone accepts page evidence. These intermediate receipts never commit observations, launch intents, or cursor progress separately. Page eligibility uses the lease's `next_eligible_at` and shared host pacing; it adds no independent retry clock.

| Value | Meaning | Enters from | Transition |
|---|---|---|---|
| `requested` | A frozen request plan awaits a successful capture. | Insert; `requested` | Record the plan. A retryable source outcome retains this state only while source allowance remains. |
| `captured` | Staged bytes and an immutable capture receipt exist; no decoded result exists. | `requested` | Fenced Fetch receipt commit. The decoder must verify mounted bytes before reading them. |
| `decoded` | The sandbox result passed `DecodedDiscoveryPage.Validate`; its digest, cursor proposal, completeness flag, and findings are recorded. No page evidence is accepted yet. | `captured` | Fenced decode receipt commit. |
| `accepted` | Terminal. Observations, findings, launch intents, and the cursor proposal are committed with the acceptance result. | `decoded` | `ApplyDiscoveryPage`; window completion has the additional conditions above. |
| `failed` | Terminal. Source allowance exhausted, policy rejected the request, capture integrity or decoding failed, or the window failed first. Keep the precise reason. | `requested`, `captured`, `decoded` | `ApplyDiscoveryPage` records the outcome; `window_closed` means closure by another page's failure. |

CHECKs require no capture/decode receipt in `requested`, a complete capture receipt in `captured`, and both receipt groups in `decoded` or `accepted`. Decode fields, including nullable `complete`, are present together; absence is not `false`. Only `accepted` has an acceptance result reference. `failed` retains any earlier receipt groups without claiming acceptance. Require a reason exactly for `failed`. Never overwrite a recorded receipt on retry.

[CursorPosition.Validate](../../internal/corpus/model/observations.go) requires an incomplete proposal to have `NextCursor == CommittedCursor`. It does not prove exhaustion or bind cursors to database state. `ApplyDiscoveryPage` must validate window, slice, ordinal, initial and committed cursors against frozen accounting and validate complete coverage across all pages. Pagination continuations are request plans, not committed progress. Accepting `complete = false` preserves findings and observations; it leaves the window progressing or incomplete. A decode rejection follows retry Q and cannot recapture unchanged input.

## Artifact request state

`ingest.artifact_request.state`. Purpose is `detail` or `artifact`. `DocumentIngest` owns requests under its `document_version` lease. It plans detail requests from listing provenance; `ApplyCapture` records artifact requests from decoded inventory. Use `owner_kind = observation` and the opaque parent observation key; for artifact requests this is [source/api.ArtifactRequest.ParentObservation](../../internal/corpus/source/api/ports.go). Freeze the request identity, purpose, required flag, owner, and plan. Recovery reuses rows and source-attempt accounting; a new processing attempt may create linked new work but cannot bypass source eligibility.

Require `expected_role` exactly when purpose is `artifact`, using `ArtifactRole`; otherwise use NULL. Preserve `expected_response_kind` as non-null text: detail allows the empty value and the other values admitted by [validateResponseKind](../../internal/corpus/source/api/ports.go); artifact requires the empty value. The empty value supports sources with one detail response. Follow-up origin checks remain owned by [FollowUpRequest.ValidateOrigin](../../internal/corpus/model/observations.go) and [RequestPlan.ValidateOrigin](../../internal/corpus/source/api/ports.go).

| Value | Meaning | Enters from | Transition |
|---|---|---|---|
| `due` | Waiting for eligibility; no successful Fetch receipt. | Insert; `leased` | Creation, retryable source outcome with allowance left, or recovery after lease loss. Only a current owner may return a row to due. |
| `leased` | The current document lease holder has claimed the request and its host slot. | `due` | Claim after both request and host eligibility, within the frozen allowances. |
| `fetched` | Staged bytes and immutable transport receipt exist; Bronze registration is pending. | `leased` | Fenced Fetch receipt commit. Release the host slot once. |
| `verified` | Terminal capture success. Staged length and hash match the receipt; purpose-appropriate capture references and retention are registered. | `fetched` | `ApplyCapture`. Artifact requests also register the artifact and observation link. Detail capture does not invent an artifact role or imply successful decoding. |
| `failed` | Terminal policy, permanent source, size/budget, or integrity failure. Keep its reason. | `due`, `leased`, `fetched` | Fenced `ApplyCapture` outcome; Q forbids retrying unchanged bad input. |
| `exhausted` | Terminal. The source retry allowance ended without a successful Fetch receipt. | `due`, `leased` | Fenced outcome or eligibility reconciliation. |
| `expired` | Terminal. The owning processing attempt closed or its deadline passed before registration. | `due`, `leased`, `fetched` | Fenced owner closure or recovery reconciliation; never the stale worker. |

CHECK `(state = 'due') = (next_eligible_at IS NOT NULL)`. Require a complete receipt in `fetched` and `verified`, none in `due`, `leased`, or `exhausted`; `failed` and `expired` preserve any existing receipt. Require a non-empty reason exactly for `failed`, `exhausted`, and `expired`. A verified receipt never regresses on later decode or quality failure. Reconcile committed outcomes before expiry; otherwise owner closure takes precedence over retry exhaustion. Release or reconcile host slots exactly once on every exit from `leased`. Infrastructure recovery reuses a fetched receipt instead of downloading again.

Both request tables use [retry classes I/S/Q/X](corpus-write-path.md#stage-contract), not new retry policy. Count each durable source attempt once, including a successful fetch; worker loss, transport replay of a commit, and cancellation alone add no source attempt. Source exhaustion uses the frozen attempt and time allowances, not an unconditional fifth failure. Replanning never resets them. Provider batch reconciliation X does not authorize recapturing these requests.

## Artifact role

**Decision:** Keep `ArtifactRole = main | appendix | original_scan` for both Bronze role columns and artifact request expectations. Validate every observation link against its artifact's role.
**Why:** These are exactly the combined emitted values in the four decoders, not a common filename grammar: [VBPL](../../internal/country/vn/sources/vbpl/decoder.go), [Công Báo](../../internal/country/vn/sources/congbao/decoder.go), [Vanban](../../internal/country/vn/sources/vanban/decoder.go), and [SBV Hanoi](../../internal/country/vn/sources/sbvhanoi/decoder.go). Only VBPL relabels main PDFs as `original_scan`. The [parts evidence](document-parts.md#current-state-and-evidence) describes legacy OCR selection, not a new role-based OCR restriction.
**Rejected:** Unchecked database roles, a lookup table, and speculative `attachment` or `version_snapshot` values.
**Detail:** `main` means source-presented document text; `appendix` means a labelled supplement; `original_scan` means a declared main PDF rendition, not proof of scanned content. Format validity remains in [ArtifactFormatValid](../../internal/corpus/model/observations.go). Detection and gates determine usability and authority. Scanned attachments follow the [part gate](document-parts.md#part-model-and-deterministic-parsing), including when the body has binding text. Add a role only with its producer, consumers, constants, and CHECK values. Conversion-output roles belong to writer file `00005`, not this set.

## Blob retention owner kind

`bronze.blob_retention_reference.owner_kind`. Edges protect dependencies, not workflow success. `00003` admits the five kinds below; insert build and release edges only when their consumers exist. Key checks select the grammar by kind. Cross-schema owners require adapter validation under the [reference contract](corpus-database.md#persistence-dependencies-and-access-paths).

| Value | Owner key | Added by | Removal condition |
|---|---|---|---|
| `execution` | Existing `execution_key`, using its [00002 CHECK](../../deploy/corpusmigrations/writer/00002_admission_and_ledger.sql); any admitted execution scope | The owning fenced apply when registering a blob, including `ApplyDiscoveryPage`, `ApplyCapture`, or `ApplyConversion`. | A fenced cleanup proves no recovery, quarantine, report, or downstream dependency remains unprotected, or atomically transfers protection. A stage result alone never suffices. |
| `observation_revision` | Existing `observation_digest`, validated as [model.Digest](../../internal/corpus/model/assets.go) | `ApplyDiscoveryPage` or `ApplyCapture` when accepting an observation and its blob dependencies. | No removal rule yet; open question 2. |
| `document_revision` | Full immutable revision key from [revision identity](corpus-database.md#identity-revisions-and-release-membership); unlike the broader 00002 grammar, it requires the revision suffix | `ApplyEvidence` protects the accepted closure; earlier outputs keep their execution protection until transfer. | No removal rule yet; immutable content alone does not establish an expiry policy. |
| `build` | Nonzero [run.BuildKey](../../internal/corpus/run/scopes.go), with the UUID text grammar in 00002 | `ApplySnapshot`, then each blob-producing build apply before its output becomes referenced. | `SealRelease` transfers protection atomically; failure cleanup waits until retained reports and all other dependencies are protected. |
| `release` | Existing release key; propose [model.ValidateKey](../../internal/corpus/model/intake.go) until the consuming release contract fixes its grammar | `SealRelease` protects the complete release closure. | Only the fenced deletion protocol in [release retention](corpus-database-releases.md#releases-delivery-and-retention). |

**Decision:** Keep the five owner kinds and make edge removal depend on protected closure, not execution termination or release tombstones alone.
**Why:** The database's [provenance contract](corpus-database.md#conversion-provenance-and-decisions) retains reports, maps, comparisons, recipes, and decisions; [release retention](corpus-database-releases.md#releases-delivery-and-retention) also protects archives, pins, and holds. Terminal work can still supply that evidence. A document revision edge needs the full revision key because a document identity alone does not select an immutable closure.
**Rejected:** Deleting every execution edge at its terminal result, or every build edge on failure, without a dependency check.
**Detail:** Edge insertion, transfer, removal, and garbage collection serialize on the blob so a new reference cannot race deletion. Follow the parent spec's immutable blob/artifact rules. Absence of edges permits consideration of object deletion, never deletion of immutable catalog rows. Staging objects remain governed by the [write-path staging contract](corpus-write-path.md#closure-accounting-and-evidence).

## Open questions

1. How should standalone source-window gaps enter the public projection? [00002](../../deploy/corpusmigrations/writer/00002_admission_and_ledger.sql) requires `evidence_gap.acceptance_id`, but [derived coverage](corpus-database.md#migration-chains-and-generated-stores) requires unresolved window gaps with a stable source `gap_key`. Reading window states alone does not settle that identity or historical watermark. M1.3 must preserve coverage receipts; agree the gap representation before implementing its projection. Do not fabricate revision acceptance or silently prescribe a nullable migration.
2. When may observation and document revision retention edges expire? The [retention authority](corpus-database-releases.md#releases-delivery-and-retention) sets release retention, not Bronze or revision expiry. Keep these edges until a reviewed policy protects every remaining dependency; immutable rows do not by themselves require permanent blob storage.
