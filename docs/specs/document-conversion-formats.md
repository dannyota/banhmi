Proposal, not implemented. This continuation of the [document conversion spec](document-conversion.md) owns the persisted formats, canonical hashing, catalog registration, and closure rules that fenced acceptance reads. The [Vietnam plan](../plans/vietnam.md#m1-hosted-intake-skeleton) owns delivery through M1.12, M1.13, M1.15, and M1.6. Formats marked coded exist on the M1.12 branch under review and are described as inspected source; every other format is proposed and must be approved before M1.15 and M1.6 start.

# Document conversion formats

`ApplyEvidence` in the [acceptance spec](corpus-database-acceptance.md) validates retained bytes, not caller claims. That is possible only when every input it reads has one versioned format, one canonical byte form, one producer, and one retention path. This document fixes those for conversion outputs, candidate manifests, gate reports, policy, and the Bronze catalog rows that hold them. It adds no schema and changes no frozen `model` or `run` type.

## Canonical hashing

Every digest is `model.Digest`: lowercase hexadecimal SHA-256 of the exact retained bytes. Every `model.AssetRef.ByteSize` is the exact byte count. Two canonical byte rules exist, and each format below names its rule.

| Rule | Bytes | Reader check | Used by |
|---|---|---|---|
| A, struct encoding | `encoding/json.Marshal` of the Go value: declaration field order, no indentation, standard-library escaping. Values are the frozen `model` types or the structs named below; no maps. | Decode with `DisallowUnknownFields`, reject a trailing value, re-encode, and require byte equality before trusting any field. This is the check the M1.12 branch's `convert.go` applies to the output manifest and its `validation.go` applies to the verdict. | Every retained blob format |
| B, sorted-key object | JSON object with keys in byte order, no whitespace, values limited to strings, integers, booleans, arrays, and objects. | Canonicalize the decoded object and recompute the digest. | `config.policy_bundle.manifest` only, because `jsonb` does not preserve key order or whitespace |

A format's `schema_version` string is part of its hashed bytes. A reader rejects an unknown version; it never decodes a newer version with a permissive decoder.

## Formats and producers

| Format | Version | Producer | Retained as | First consumer |
|---|---|---|---|---|
| Conversion request manifest | `ConversionManifest` struct, rule A | Trusted composition, type from M1.12 | Not retained; its digest is the request digest in result paths and `ConvertEnvelope.Manifest` | Sandbox child |
| Sandbox output manifest | Sorted `[]model.AssetRef`, rule A | M1.12, coded | `manifests/<digest>`; `conversion_artifact` role `output_manifest` | Catalog, provenance, acceptance |
| Converted document | `blocks-v1`, rule A | M1.12, coded | `results/<request digest>/document.json` | Gate, projection, acceptance |
| Block, text, map, ledger, evidence, member objects | Frozen `model` arrays or raw bytes | M1.12, coded | `objects/<digest>` | Projection, acceptance |
| Detection result | `detect-v1`, rule A | M1.12, coded | `objects/<digest>`; `conversion_artifact.detector_digest` | Acceptance |
| Converter observation report | `conversion-quality-v1`, rule A | M1.12, coded | `results/<request digest>/quality.json` | Gate input; never an acceptance report |
| Normalizer manifest | `normalizer-v1`, rule A | Composition from constants M1.12 exports | `recipes/<digest>`; `normalization_recipe_digest` columns | Projection, acceptance |
| Candidate input manifest | `candidate-input-v1`, rule A | Composition; format frozen by M1.13 | `candidates/<digest>`; every `candidate_digest` and `CandidateDigest` | Conversion request, gate, acceptance |
| Candidate output manifest | `candidate-output-v1`, rule A | Composition; format frozen by M1.13 | `candidates/<digest>`; `IntakeApplyRequest.OutputDigest` | Gate, revision minting, acceptance |
| Policy bundle manifest | `policy-bundle-v1`, rule B | M1.13 | `config.policy_bundle.manifest` with `digest` | Gate, acceptance |
| Artifact gate report | `gate-report-v1`, rule A | M1.13 | `reports/<digest>`; `quality_report` with `artifact_hash` | Acceptance |
| Document roll-up | `gate-rollup-v1`, rule A | M1.13 | `reports/<digest>`; `quality_report` with NULL `artifact_hash`; `QualityReportHash` | Acceptance |
| Decision scope manifest | `gap-scope-v1` | M5.1 and M5.2; M1 fixtures seed it | Owned by [acceptance decision evidence](corpus-database-acceptance.md#decision-evidence-permits-and-comparison) | Acceptance |
| Comparison result | Open | M2.4 | Outside this document | Acceptance after M2 |

The trusted composition is the `DocumentIngest` activity code that M1.7 binds and M1.14 composes. It produces manifests from ledger state and verified outputs; it never trusts converter bytes before sandbox validation.

## Conversion outputs

These facts describe the M1.12 branch. The request manifest `ConversionManifest` carries `RequestKey`, `Request` (`api.ConvertRequest` with the original `model.CaptureRef`, recipe `AssetRef`, and `model.ResourceBudget`), `ObservationKey`, `ArtifactRole`, `Language`, `HTMLSelector`, `DetectorHash` (detector recipe), `CandidateDigest`, and `OutputBytes`. The request digest is the digest of its rule A bytes and names the result directory.

The sandbox output manifest is a `[]model.AssetRef` sorted by `Path` in strictly ascending byte order with no duplicate path. Paths are `objects/<digest>` for content-addressed objects and `results/<request digest>/document.json` and `quality.json`. Both result paths are required. Total bytes are at most `OutputBytes` and the count is at most `Budget.MaxItems`. Validation requires every output file to be referenced by the document, its report, or a span, so membership is complete and closed: a path outside the manifest is not part of the conversion.

`document.json` is `model.ConvertedDocument` with `SchemaVersion` `blocks-v1`, `Original` equal to the request artifact, `ObservationKey`, `ArtifactRole`, and `CandidateDigest` equal to the request, `RecipeHash` equal to the recipe asset digest, and `DetectorHash` equal to the digest of the `detect-v1` object, not the detector recipe. Its six `AssetRef` fields name `objects/` entries: `RawBlocks` (array of `{ID, Kind, Text, Sources}`), `NormalizedBlocks` (`[]model.Block`), `RawText` and `NormalizedText` (whole-document bytes in block order), `OffsetMap` (`[]model.OffsetMapSegment`), and `EditLedger` (`[]model.EditLedgerEntry`). Block IDs are `block-<six digits>-<digest>`; `Authority` is `unknown`; `Language` equals the request; every source span is in `original_bytes` or `decoded_package_member` space with `BlockID` set. The DOCX adapter emits two spans per block, an original-bytes anchor with `Member` `word/document.xml`, `Object` `paragraph/<ordinal>`, a token ordinal, and `BYTE_MAPPING_UNAVAILABLE`, plus a decoded-member span with a byte range in the retained member object. The HTML adapter emits original-byte ranges.

Map transforms are `source-decode-v1`, `identity-v1`, and `nfc-crlf-v1`; segment `Ordinal` is the array index. Ledger entries use rule `nfc-crlf` version `1`; `Removed`, `Replacement`, and `Evidence` name `objects/` entries, and `Evidence` is a `model.DecodedEdit`. Whole-text destination spans name the raw or normalized text object with `Object` `text`, a full byte range or `BYTE_MAPPING_UNAVAILABLE` for empty text, and an empty `BlockID`. The converter observation report `conversion-quality-v1` binds artifact hash, candidate digest, request digest, recipe hash, detector hash, and document hash, and always carries `Outcome` `quarantined` with mandatory unknown `geometry` and `fidelity` metrics (threshold `qualification-required`, reason `BASELINE_UNQUALIFIED`) and blocking artifact findings including `GEOMETRY_UNKNOWN` and `FIDELITY_UNQUALIFIED`. It is closure evidence that proves the converter ran unqualified; it is never the report acceptance gates on.

**Decision:** Derive the normalizer manifest from exported constants instead of adding a field to the frozen `ConvertedDocument`.
**Why:** `silver.conversion_artifact.normalization_recipe_digest`, `silver.document_revision.normalization_recipe_digest`, and `ConversionArtifactInput.NormalizationRecipeDigest` are required, but the branch pins its rules only by name. M1.1 froze `ConvertedDocument` without a normalizer field.
**Rejected:** Reusing the converter recipe digest for both columns; a hand-typed manifest in the composition.
**Detail:** `normalizer-v1` is `{SchemaVersion, Rules []{Rule, Version}, Transforms []string}` sorted by name. M1.12 must export its rule and transform names as constants; the composition builds the manifest from them and retains it at `recipes/<digest>`. Acceptance requires every retained ledger entry's `(rule, rule_version)` to be in `Rules` and every segment `transform` to be in `Transforms`.

## Candidate manifests

A candidate is one processing attempt's frozen selection for one document version. Two manifests describe it: the input manifest exists before conversion starts and the output manifest exists after every selected artifact converts.

`candidate-input-v1` fields, all required unless stated:

| Field | Binding |
|---|---|
| `Country`, `DocumentVersionKey`, `ProcessingExecutionKey` | Equal the resolved execution's country, lease key, and `execution_key`. |
| `RecipeDigest`, `BudgetDigest` | Equal `ingest.execution.recipe_digest` and `budget_digest`. The input digest is not repeated here because the execution is registered before originals are captured. |
| `ObservationKey` | One captured observation of this version, linked to a committed capture result. |
| `Artifacts` | Ordered list; index is the artifact ordinal `k`. Each entry has `ArtifactKey`, `Role` (`main`, `appendix`, `original_scan`), `Required`, `Original` (`model.AssetRef` of the captured original with catalog path), `MediaType`, and `Format`. Every entry must be an `observation_artifact` link of `ObservationKey` with matching role and ordinal. |
| `Language`, `HTMLSelector`, `DetectorDigest`, `ConversionRecipe` (`AssetRef`), `NormalizerManifest` (`AssetRef`), `ParserRecipeDigest` | Recipe selection. M1 compositions set `ParserRecipeDigest` to the digest of the rule A bytes of `{SchemaVersion: "parser-v1", Parser: "none"}`; M2.2 replaces it. |
| `GatePolicyDigest`, `ThresholdVersion` | Select the policy bundle. No field in `Outputs` or in any report selects policy. |
| `Mode` | `first_pass` or `reprocess`. |
| `Predecessor` | Required exactly when `Mode` is `reprocess`: `DocumentVersionKey`, `RevisionKey`, `CandidateDigest`, and `AcceptanceWatermark` of the baseline, which may belong to another source version of the same identity. |

Its digest is the `CandidateDigest` that the conversion request, every `document.json`, every gate report, and every `candidate_digest` column carry, and that `IntakeApplyRequest.CandidateDigest` names. The manifest is retained at `candidates/<digest>` with an execution edge before the first conversion request is issued. `OriginalHash` in the acceptance request must be one of `Artifacts[*].Original.SHA256`.

`candidate-output-v1` lists, per ordinal `k`: `OriginalHash`, `DetectorDigest` (the `detect-v1` object digest), and the six `AssetRef` values copied from that artifact's `document.json`. It also carries `CandidateDigest`, `NormalizerManifestDigest`, `GatePolicyDigest`, and `TextHash`, the digest of the rule A array of per-ordinal `NormalizedText.SHA256` values. It excludes ledger keys, request digests, capture keys, and the sandbox output manifest hash, because those change on retry while the content does not. Its digest is `IntakeApplyRequest.OutputDigest` for `candidate`, `accepted`, and `accepted_with_gaps`, and the delivery record's output hash. The composition mints the reserved revision key with `source_observation_set_hash` equal to the digest of the sorted observation key array, `text_hash` equal to `TextHash`, `metadata_hash` equal to the observation's `metadata_digest`, and the four recipe digests from the input manifest; acceptance compares those `silver.document_revision` columns against the manifests, not against caller fields.

Per-artifact `document.json` hashes and sandbox output manifest hashes are attempt provenance. They appear in `conversion_artifact` rows and in the gate reports, not in the revision identity.

## Policy and gate reports

`policy-bundle-v1` is the rule B object in `config.policy_bundle.manifest`; `config.policy_bundle.digest` is its digest and `FindPolicyBundle` resolves it. Required keys:

| Key | Content |
|---|---|
| `schema_version`, `country`, `threshold_version` | `policy-bundle-v1`; the writer country; the version every report under this bundle must carry. |
| `artifact_profile`, `rollup_profile` | Arrays of `{kind, mandatory, allow_not_applicable, threshold}` that decode to `model.MetricRequirement`; `kind` values are `model.MetricKind` members, unique within each profile. The trusted `model.QualityMetricProfile` for a report is `{PolicyHash: digest, Metrics: profile}`. |
| `acceptance_limits` | `max_blobs` (at most 1024), `max_rows`, `max_json_bytes`, `max_object_bytes`, `max_elapsed_ms`. Missing or nonpositive limits reject acceptance. |
| `gap_reason_codes` | Allowlisted reason codes for gap records; `GapRef.ReasonCode` must be a member. |

`gate-report-v1` is one per selected artifact. Its bytes are the rule A encoding of `{SchemaVersion, Artifact model.QualityReport, DocumentHash, OutputManifestHash}` where `Artifact.ArtifactHash` is the original hash, `Artifact.Report` names this blob, and `DocumentHash` and `OutputManifestHash` bind the attempt's `document.json` and sandbox output manifest. `Artifact.Metrics` match `artifact_profile` exactly once each and `Artifact.Findings` carry typed scopes. Metric `Exclusions` and finding `Spans` are `model.Span` values in `normalized_text` space naming that artifact's `NormalizedText` object. `Artifact.Report.SHA256` cannot appear inside its own bytes, so the producer sets `Artifact.Report` to `{Path: "reports/self", SHA256: <64 zeros>, ByteSize: 0}` before hashing, retains the bytes under the real digest, and a reader recomputes the digest the same way.

`gate-rollup-v1` is one per candidate. `model.QualityReport` is per artifact and requires a 64-character `ArtifactHash`, so the roll-up is its own struct: `{SchemaVersion, CandidateDigest, OutputManifestDigest, PolicyHash, ThresholdVersion, Outcome, ArtifactReports []{Ordinal, OriginalHash, ReportHash, ReportByteSize, Outcome}, Metrics []model.QualityMetric, Findings []model.Finding, Gaps []{Scope model.GapKind, ScopeKey, ReasonCode, Spans []model.Span}, Retained []{Ordinal, Spans []model.Span}, DecisionRef}`. `Metrics` match `rollup_profile`; `ArtifactReports` names every ordinal in the candidate output manifest exactly once; `Outcome` is `needs_ocr` or `quarantined` if any artifact report has that outcome or if any gap exists without a bound decision, `accepted_with_gaps` when `DecisionRef` names the decision that authorizes every gap and the gate reran over the retained ranges, and `accepted` otherwise. `Retained` and `Gaps[*].Spans` partition each ordinal's normalized text range with no overlap and no uncovered byte; an ordinal with a wholly missing body has an empty `Retained` entry and a `document_revision` or `artifact` gap, never fabricated text. `DecisionRef` is empty unless the outcome is `accepted_with_gaps`, when it names the decision key whose scope manifest repeats these exact spans.

Report rows in `silver.quality_report` and `silver.quality_finding` are the indexed projection of these bytes: `report_hash` and `report_byte_size` are the blob; `policy_digest` and `threshold_version` are copied; `record_count` is `len(Metrics) + len(Findings) + len(Gaps)`; ordinals run metrics, then findings, then gaps in array order; each `evidence` array lists the `model.AssetRef` values a record's spans name, which the M1.5 adapter requires. Acceptance decodes the retained bytes and compares the projection row for row; a mismatch rejects.

## Projection into writer rows

The composition translates verified outputs into the M1.5 adapter inputs. These rules make the translation deterministic so acceptance can recompute it.

| Rows | Rule |
|---|---|
| `conversion_artifact`, five rows per ordinal `k` | Roles `raw_blocks`, `normalized_blocks`, `raw_text`, `normalized_text` take the four `document.json` assets; `output_manifest` takes the sandbox output manifest at `manifests/<digest>`. `ordinal` is `k`. `schema_version` is `blocks-v1`; `original_hash`, `conversion_recipe_digest`, `detector_digest`, and `candidate_digest` come from `document.json`; `normalization_recipe_digest` is the normalizer manifest digest; `manifest_hash` and `manifest_byte_size` are the sandbox output manifest. `Members` is the five-role projection for that ordinal. |
| `offset_map_segment` | For segment `t` of the `OffsetMap` array, emit the cross product of `Sources` and `Destinations` as edges with `transform_order = t` and `segment_ordinal` as the cross-product index, grouped by `(source artifact hash, destination artifact hash)` for `InsertOffsetMapSegments`. `map_hash` is the `OffsetMap` asset digest. |
| `edit_ledger` | Entry `i` gives `operation_ordinal = i`, `ledger_hash` the `EditLedger` asset digest, `input_hash` and `output_hash` from `Inputs[0]` and `Outputs[0]`, byte ranges from those spans, `removed_bytes` and `replacement_bytes` the retained `Removed` and `Replacement` object bytes, `source_anchors` the rule A array of `Inputs`, and `encoding_evidence` and `font_evidence` the object `{}` until a qualified legacy-encoding adapter defines them in M2.1. Removed or replacement bytes above 1 MiB reject under the adapter bound; a block that large is a `RESOURCE_LIMIT` finding, not a truncated ledger. |

**Decision:** Project spans into `offset_map_segment` by dropping `TokenOrdinal` and by giving whole-text spans the reserved block ID `document`.
**Why:** `00005` has no token ordinal column and the M1.5 adapter rejects a span with `TokenOrdinal` set or an empty `BlockID`, while the M1.12 DOCX anchors carry token ordinals and whole-text destination spans carry no block ID. The complete map remains the hashed `OffsetMap` blob, which the [parent spec](corpus-database.md#conversion-provenance-and-decisions) names as the authoritative map; indexed rows exist for paging and range checks.
**Rejected:** Adding a column to `00005` and reopening the complete M1.5 slice; changing M1.12 to emit block IDs on whole-text spans and to drop token ordinals, which would lose anchors from the hashed map; rejecting DOCX conversions until a schema change.
**Detail:** `document` passes `model.ValidateKey` and cannot collide with `block-` IDs. Acceptance compares each indexed row with the hash-verified map bytes after applying these two rules and no others. A span whose projection differs in any other field rejects.

## Catalog registration and execution retention

**Decision:** Add `ConversionCatalog.RegisterConversionOutputs` to the `intake` store as the first half of the `ApplyConversion` composition that the [workflow spec](corpus-workflows.md#workflow-contracts) names; the existing `document.Conversion` adapter is the second half.
**Why:** `bronze.blob` and `bronze.blob_retention_reference` belong to `intake` under the [store ownership table](corpus-database.md#migration-chains-and-generated-stores). `document.Conversion` requires every referenced hash to already exist in `bronze.blob` and adds no retention edge, so a conversion blob is unprotected between staging and acceptance. The [retention owner table](corpus-database-sources.md#blob-retention-owner-kind) assigns the `execution` edge to the fenced apply that registers the blob.
**Rejected:** Bronze writes inside the `document` store; one cross-store transaction for conversion, which is unnecessary because the execution edge protects the blobs between the two commits and both adapters replay by hash; registering at acceptance time, which would leave quarantined and candidate evidence unprotected.
**Detail:** Signature `RegisterConversionOutputs(ctx, work run.IntakeWork, registration ConversionRegistration) (run.IntakeReceipt, error)`. `ConversionRegistration` carries `Manifest blob.Metadata` (the manifest blob itself), `Members []blob.Metadata`, and `MemberRefs []model.AssetRef` (the manifest's members), and the composition calls it once per retained manifest: the candidate input manifest, each sandbox output manifest, the normalizer manifest, each gate report, the roll-up, and the candidate output manifest. `Members` is empty for a manifest whose references capture or an earlier registration already cataloged; the manifest blob itself always receives the edge. Member metadata uses `MediaType` `application/json` for rule A objects, `text/plain; charset=utf-8` for text objects, the declared member type for decoded members, `ObjectRef` equal to the asset path, `CaptureSource` equal to the producing envelope kind (`candidate`, `conversion`, `quality`, `recipe`), and `CaptureRef` equal to the envelope or execution key. The catalog is authoritative for `object_ref`: when a hash already exists, the receipt returns the stored metadata and the composition uses that path in every later provenance input and in `Outputs`.

Transaction, in one `ledgerTransaction` as the runtime login: lock admission `FOR SHARE` and the `document_version` lease `FOR UPDATE` through `lockAdmission` and `lockLease`; resolve the execution and recheck the frozen budget through `resolveExecution`; look up the result by `IntakeIdempotencyKey` with `Stage` `normalize`, `Substep` `conversion-catalog`, and `Shard` equal to the manifest digest, returning the committed receipt on identical replay and `ErrPayloadConflict` on a changed member set; validate that every `MemberRefs` entry has exactly one `Members` entry with equal hash and size, that the manifest blob is not listed among its own members, and that the count is within `acceptance_limits.max_blobs`; register each blob through `RegisterSourceBlob` then `ReadSourceBlob` in hash byte order, rejecting a stored row whose size differs; lock every catalog row by hash in byte order through the new `LockCatalogBlob` statement; insert `execution` edges in the same order with `AddBlobRetentionReference` using the execution key as `owner_key`; insert the execution result with `Outputs` equal to the catalog-derived `AssetRef` set sorted by hash and `OutputDigest` equal to the manifest digest; recheck lease expiry; commit. Edges are never transferred, removed, or given another owner kind here. The order is admission, lease, blobs, a prefix of both the [source lock order](corpus-database-sources.md#discovery-claims-and-lock-order) and the [acceptance lock order](corpus-database-acceptance.md#lock-order-and-concurrency-proof) with no window, page, host, request, identity, attempt, or projection lock, so it adds no reverse edge.

Owned paths for M1.15 are `internal/corpus/store/intake/{conversion_catalog,conversion_catalog_integration_test}.go`, `internal/corpus/store/intake/queries/conversion_catalog.sql` with `LockCatalogBlob` and any bounded read it needs, and the reviewed generated `intakedb` output. Handoff to M1.6: acceptance reuses `LockCatalogBlob`, requires an `execution` edge whose `owner_key` equals each provenance row's `processing_execution_key` for every retained dependency of a `candidate`, `quarantined`, or `excluded` state, and seeds its fixture through this adapter instead of raw inserts. Handoff to M1.14: the composed `DocumentIngest` calls this adapter before `document.Conversion` for every manifest above and treats a registration failure as a retryable infrastructure failure with no evidence.

## Closure traversal and bounds

Acceptance discovers the protected closure by following only the typed reference fields of the versioned formats above, deduplicating by hash, and stopping at `acceptance_limits.max_blobs`. The start set is the candidate input manifest, the roll-up, and, when present, the candidate output manifest and decision scope manifest. From there: the input manifest adds originals, the conversion recipe, and the normalizer manifest; the output manifest adds each ordinal's six assets; the roll-up adds each artifact report; each artifact report adds its `document.json` and sandbox output manifest; each sandbox output manifest adds its members; `document.json`, maps, and ledgers add nothing new because validation proved their references are manifest members; every `Evidence` asset in a metric or finding must already be a member. A span, anchor, or evidence hash never adds a member; one that is not already a member rejects. Inline policy JSON is a database dependency, not a blob. Every member must resolve to one `bronze.blob` row with equal size and `object_ref`, carry the required retention edge, and verify by bounded read.

## Freeze list and open items

| Slice | Must freeze before M1.15 and M1.6 |
|---|---|
| M1.12 | The coded formats above as described; exported constants for rule `nfc-crlf` version `1` and the three transform names; the empty-`BlockID` whole-text span shape and the DOCX token-ordinal anchor shape, so the projection rules stay exact. |
| M1.13 | `candidate-input-v1`, `candidate-output-v1`, `policy-bundle-v1`, `gate-report-v1`, `gate-rollup-v1`; the artifact and roll-up metric profiles; `gap_reason_codes`; the retained and gap span partition rule; `acceptance_limits` defaults. |
| M1.15 | `ConversionRegistration`, the receipt shape, `LockCatalogBlob`, and the catalog-authoritative `object_ref` rule. |

1. **Open, owner approval:** approve this document as the acceptance input contract. Recommendation: approve with the projection decision above; the alternatives reopen a complete slice or lose anchors.
2. **Open, M2.4:** the comparison result format, outcome, and critical-change classification. Recommendation: a `comparison-v1` rule A struct with `Outcome` and `CriticalChanges []model.Finding`, bound to both report hashes, decided in the slice that produces reprocess work.
3. **Open, M2.1:** `encoding_evidence` and `font_evidence` schemas for qualified legacy-encoding adapters; the baseline writes `{}`.
