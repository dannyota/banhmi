Proposal, not approved or implemented. The [Vietnam plan](../plans/vietnam.md#m113-initial-quality-and-residency-gate) owns delivery as M1.13 phase 2. Phase 1 codecs are merged in `internal/corpus/quality/formats/`. This proposal consumes their frozen contracts without changing them. Residency formats and production classification remain open; they do not block the pure baseline gate.

# Quality gate and residency screen

[Document conversion](document-conversion.md#quality-gate-and-authority) owns metric meaning and proposed launch thresholds. [Conversion formats](document-conversion-formats.md#policy-and-gate-reports) owns retained reports, precedence, partitions, and writer projections. [Vietnam](vietnam.md#evaluation-and-qualification-gates) and [hosting](vngcloud-hosting.md#security-and-residency) own residency requirements. This spec defines the baseline gate, records residency implementation prerequisites, and separates their delivery slices.

## Existing contracts and implementation boundary

The following identifiers exist. Paths are relative to `internal/corpus/`.

| Surface | Exact contract and limitation |
|---|---|
| `quality/api.Gate` | `Evaluate(GateRequest) (QualityEvaluation, error)`; request fields are `Document`, `Parts`, `Profile`. Evaluation fields are `Metrics`, `Findings`, `Outcome`. No I/O or staging port. |
| `model.DecodedDocument` | `OriginalHash`, `CandidateDigest`, `Blocks`, `Limits`. `DecodedBlock` has string `RawText` and `NormalizedText`, `ID`, `ParentID`, `Kind`, `Language`, `Authority`, `SourceSpans`; no ordinal or content asset. Array order is processing order. |
| `model.DecodedPartResult` | Six parser outputs plus `Limits`. `Exclusions` is `[]model.ExcludedRange`, each with `Span` and `ReasonCode`. `Findings` can contain evidence assets; their presence alone proves no qualification. |
| `conversion/api.LanguageRules` | `Inspect(model.DecodedDocument) ([]model.QualityMetric, []model.Finding)` and `Normalize(model.TextInput) (model.MappedValue, []model.Finding)`. Neither receives a profile or parts. |
| `quality/formats` | `PolicyProfiles`, `GateReportCodec`, `GateRollupCodec`, `ReportSentinel`, `ValidateReportReference`, `ValidatePartition`, `ValidateScopeBinding`, and the typed `GateReport`, `GateRollup`, `GapScope` exist. Codec constructors take trusted policy and limits where applicable. `Codec.Encode` returns bytes, digest, error; `Decode` returns value, digest, error. |
| `quality/api.ResidencyScreen` | `Screen(context.Context, ResidencyRequestEnvelope) (model.ResidencyEnvelope, error)`. The port requires classification inside the local in-country sandbox. |
| `quality/api.ResidencyRequest` | `Artifact model.CaptureRef`, `Policy model.AssetRef`, `Budget model.ResourceBudget`. A capture requires `RequestHash`, `ResponseHash`, and `TransportReceipt`, among other fields. |
| `source.ExportScreens` | `PrepareExport(context.Context, []byte, model.Digest, model.ResourceBudget) (api.ResidencyRequestEnvelope, error)` and `ResolveAdmission(context.Context, model.ResidencyEnvelope) (model.ResidencyAdmission, error)`, where `api` denotes quality API. It does not include `Screen`. |
| `model.ResidencyAdmission` | `ArtifactHash`, `PolicyHash`, `Class`, `ReasonCode`, `Report`. `Validate` checks shape; `Admitted` only tests `Class == model.ResidencyPublic`. Neither authenticates the report. |

`source.Capture` already calls all three residency operations for each authorized request. It compares admission hashes with the outbound bytes and pinned policy and checks admission. `source/http.ExportBytes` serializes `net/http.Request.Write` output, including body, URL, and headers. It is the canonical screening representation, not a guarantee of identical HTTP wire framing. The residency implementation and its retained formats do not exist.

## Pure baseline gate

**Decision:** Preserve metric definitions and quarantine incomplete evidence.
**Why:** Decoded blocks cannot prove independent inventories, edit reversal, or qualified geometry and fidelity.
**Rejected:** Calling block coverage structure completeness, anchor presence full provenance, or a generic Latin-mark count Vietnamese orthographic validity.
**Detail:** Baseline signals are findings. The metric table below defines exactly what this slice can report. A successful conversion, optional metric, or review decision cannot turn missing evidence into a pass.

Proposed new implementation surface, all under `internal/corpus/quality/`:

- `Gate` implements `api.Gate`.
- `Options` has `Policy formats.PolicyBundle`, `Limits formats.Limits`, and `Language conversionapi.LanguageRules`.
- `NewGate(Options) (*Gate, error)` verifies the policy through `formats.PolicyProfiles`, parses every artifact and roll-up threshold with the grammar below, deep-copies configuration, and rejects a non-nil `Language` in the baseline. Country binding belongs at construction; no country import or new request field is needed. Qualified language providers require the later handoff below.
- `(*Gate).Report(api.GateRequest, ConversionRefs) (formats.GateReport, error)` evaluates the request itself; it never accepts caller-supplied metrics or `Passed` values.
- `ConversionRefs` has value fields `RequestManifest`, `Document`, `OutputManifest`, all `model.AssetRef`.
- `(*Gate).MissingReport(original, candidate model.Digest, reason string) (formats.GateReport, error)` builds an unconverted quarantine report.
- `(*Gate).LimitReport(original, candidate model.Digest, refs *ConversionRefs) (formats.GateReport, error)` builds a minimal `REPORT_LIMIT` report. Non-nil refs preserve a completed conversion; nil denotes no completed conversion. Composition can call it after a projection or edit-size overflow.

These names are proposed, not frozen API additions. Keep threshold parsing and text-signal helpers private. There is no new `RollUpValue`, generic report-binding type, or publicly mutable evaluation-to-report builder.

### Validation, bounds, and determinism

1. Before `GateRequest.Validate`, check the profile has exactly the pinned requirements, in their pinned order, and the pinned policy hash. Compare string lengths before bytes. Require geometry and fidelity to be mandatory, as `formats.PolicyProfiles` already does. The generic profile validator alone does not require them.
2. Cap each input's positive `RuleLimits.MaxItems` and `MaxBytes` by the constructor's effective `MaxRows` and `MaxJSONBytes`. Reject larger declared limits before traversing collections. Effective limits are the fieldwise minimum of caller and policy limits. Validate document and parts next. Do not treat caller-declared large limits as trusted allocation authority.
3. `GateRequest.Validate` validates each input separately. It does not prove cross-input ownership, original-hash binding, parent existence, namespace membership, or absence of cycles. Check those relations with bounded maps and iterative traversal. Reject foreign classified-range hashes, unknown block IDs, duplicate classified ownership, unknown namespaces, and invalid parent chains. Exclusion spans must match known document provenance; the presence of a block ID alone is insufficient. Source spans can legitimately reference decoded-member objects; their closure is verified by the consumer, not by requiring every hash to equal the original.
4. Preserve inputs. No asset reads, clock, randomness, logger, goroutine, network, or rewriting of evidence occurs in `Evaluate`. Placeholder folding uses a temporary diagnostic view only. Map iteration never determines array order. Use checked counters and linear scans, not a block-by-range cross product. Storage and elapsed limits remain the consumer's responsibility.
5. Reserve result capacity before appending. Count every nested array entry and string against effective row and JSON budgets, with subtraction before addition. Top-level report records are at most `min(MaxRows, 1000)`; report encoding and projected writer input must also fit the existing 4 MiB adapter limit. Do not silently drop findings to fit.
6. Invalid input or exhausted bounds return a zero evaluation and a fixed error category: `GATE_INPUT_INVALID`, `GATE_POLICY_INVALID`, or `GATE_REPORT_LIMIT`. Never wrap decoded identifiers, text, codec errors, or a language provider's diagnostics into an outward error.

The same valid inputs and pinned options yield identical values and encoded report bytes. All new report arrays, including nested metric/finding evidence and spans, are non-null. This differs from the M1.12 observation report, whose already-hashed nil arrays remain unchanged. The phase 1 gate codecs reject nil arrays.

### Threshold interpretation

**Decision:** Give the baseline consumer a bounded grammar without changing `MetricRequirement` or the policy codec.
**Why:** `Threshold` is a nonempty string; neither `QualityMetric.Validate` nor the codecs compute arithmetic or authenticate `Passed`.
**Rejected:** Floats, expressions, arbitrary precision driven by unbounded strings, and assuming codec validation proves a measured pass.
**Detail:** Accept exactly `qualification-required`, `min:d`, or `max:d`. Decimal `d` is `0`, `1`, or `0.` followed by one to six digits with a nonzero last digit. Reject signs, spaces, exponents, trailing zeroes, values outside [0,1], and every other spelling. Compare ratios inclusively with integer rational arithmetic and overflow-safe multiplication. `qualification-required` always yields unknown, never a measured pass. This is a proposed consumer grammar, not a new approved format rule.

Future measured metrics must have `0 <= Numerator <= Denominator`, positive denominator, matching threshold and mandatory flag, and a recomputed pass. Empty denominators yield unknown with `NO_ELIGIBLE_INPUT`. Baseline computation below remains unknown even under a numerical threshold. The existing phase 1 synthetic bundle, with only mandatory geometry and fidelity at `qualification-required`, remains valid and unchanged.

### Metric computation

Emit one metric per pinned `ArtifactProfile` entry, in profile order. Unknown means zero counts, false pass, the copied threshold and mandatory flag, the stated reason, and empty evidence/exclusion arrays.

| Kind | Baseline state and reason |
|---|---|
| `geometry`, `fidelity` | Unknown, `BASELINE_UNQUALIFIED`. Conversion success supplies no qualification. |
| `page_accounting` | Unknown, `PAGE_INVENTORY_UNAVAILABLE`. Block page anchors are not an independent inventory. |
| `text_coverage` | Unknown, `REGION_INVENTORY_UNAVAILABLE`. Text-bearing blocks cannot reveal missing source regions. |
| `table_integrity` | Unknown, `TABLE_INVENTORY_UNAVAILABLE`. Absence of a table block does not prove absence of source cells. |
| `metadata_agreement` | Unknown, `METADATA_UNAVAILABLE`. There is no independent source observation in the request. |
| `structure_completeness` | Unknown, `SOURCE_STRUCTURE_UNAVAILABLE`. Parts membership is diagnostic; source labels and quote-scope evidence are missing. |
| `provenance_completeness` | Unknown, `PROVENANCE_VERIFICATION_UNAVAILABLE`. Anchors and authority are visible, but recipe binding and reversible edits are not. |
| `diacritic_validity_rate`, `citation_parse_rate` | Unknown, `LANGUAGE_RULES_UNQUALIFIED`. Generic Unicode checks cannot substitute for qualified Vietnamese rules and citation candidates. |
| `corruption_rate` | Unknown, `CORRUPTION_RULES_UNQUALIFIED`. The legacy marker heuristic is not a qualified classifier of unexplained corruption or legitimate foreign text. Emit its findings below instead. |

This deliberately conservative gate always quarantines under the pinned baseline policy. It does not claim measured quality on synthetic inputs. M2 must qualify metric producers and specify their evidence bindings before promoting these states. `LanguageRules.Inspect` can later supply country measurements, but its outputs need bounded validation, exact allowed kinds, recomputed passes, and evidence verification by the consumer. `Normalize` must never be called by the gate. That provider handoff is not authorized by a non-nil interface alone.

`not_applicable` is not emitted. Strictly, `Parts.Findings[*].Evidence` contains `AssetRef`s, so the claim that the request contains none is false. No field binds those assets to metric-specific qualification, and a pure gate cannot resolve them. Neither copied findings nor evidence returned by `Inspect` can grant inapplicability without a reviewed qualification contract.

### Exclusions and findings

**Decision:** Do not remove text based only on `ExcludedRange.Span.BlockID`.
**Why:** A source span with a block ID can name a partial range, and the request supplies no authorized decision scope or aggregate normalized-text binding.
**Rejected:** Excluding an entire block for any matching ID; ignoring unsupported ranges while returning an accepting result.
**Detail:** Baseline evaluation inspects all blocks. Every nonempty exclusion set adds a blocking `EXCLUSIONS_UNSUPPORTED` finding with the original spans in producer order. Unknown metric exclusions stay empty because no exclusion was applied. M1.14 must not project decision ranges into this request and assume a retained-range rerun is supported.

New findings use `model.GapArtifact`, `ScopeKey = string(Document.OriginalHash)`, and no evidence assets. Preserve native source spans, including member anchors and token ordinals, rather than inventing aggregate offsets. Document roll-up findings may use existing `model.GapDocumentRevision` with a resolved revision key. `GapKind` already has that distinct scope; a candidate hash is not a revision key.

Finding order is deterministic:

1. One unknown-metric finding per requirement in profile order: `FIDELITY_UNQUALIFIED`, `GEOMETRY_UNKNOWN`, or the ASCII uppercase metric kind plus `_UNKNOWN`. Mandatory means `model.SeverityBlocking`; otherwise `model.SeverityWarning`. These findings have no spans.
2. Per-block `TEXT_NOT_NFC` and `EMPTY_TEXT_BLOCK`, in that order, following block array order. Check only heading, paragraph, list-item, and table text. No repair. NFC failure is blocking; empty text is warning. Each cites that block's `SourceSpans` unchanged.
3. Aggregate legacy diagnostic findings, in the code order below, with empty spans. Do not run the 50-letter minimum separately on short headings or paragraphs.
4. `EXCLUSIONS_UNSUPPORTED`, then all `Parts.Findings` in producer order. Copy their values and materialize empty arrays for this new report without modifying the input.

Findings and provenance may contain source-supplied labels, members, paths, or IDs. They are local evidence, not sanitized transport. Residency admission governs publication of their closure. Do not promise that planting a name in `Span.Member` produces a name-free gate report while also promising exact provenance preservation.

### Legacy text diagnostics

**Decision:** Port the arithmetic from [legacy text extraction](../../pkg/extract/extract.go) without importing or changing legacy code.
**Why:** M1.13 reuses findings, not the legacy confidence or verdict.
**Rejected:** Reusing confidence as accuracy, silently normalizing input, or treating every suspicious glyph as proven corruption.
**Detail:** The proposed internal rule version is `legacy-text-signals-v1`. Process the concatenation, with no separators, of included text-bearing block `NormalizedText` values. All blocks remain included in the baseline. Inspect NFC separately; do not reproduce the legacy NFC rewrite on invalid input.

| Finding code, in order | Exact trigger on nonempty aggregate text |
|---|---|
| `TEXT_REPLACEMENT_CHARS` | U+FFFD or `unicode.IsControl`, except LF, TAB, CR, FF: count / rune count > 0.01. |
| `TEXT_TOO_FEW_LETTERS` | `unicode.IsLetter` count < 50. |
| `TEXT_HIGH_WHITESPACE` | `unicode.IsSpace` count / rune count > 0.40. |
| `TEXT_PUA_RUNES` | U+E000 through U+F8FF count / rune count > 0.005. |
| `TEXT_MOJIBAKE_MARKERS` | Count of runes in `√∆·ªƒ∫≠‚ÄØ` >= 8 and count / rune count > 0.005. |
| `TEXT_CYRILLIC_MOJIBAKE` | U+0400 through U+04FF count >= 8 and count / rune count >= 0.005. |
| `TEXT_LOW_DIACRITIC_DENSITY` | At least 200 letters and non-ASCII letters / letters < 0.02. This is density, not orthographic validity. |
| `SOURCE_PLACEHOLDER` | The legacy `OfficialPlaceholder` rule, including its Vietnamese folding, whitespace collapse, 200-letter ceiling, and `file`, `dinh kem`, or <80-letter condition. Empty text is not a placeholder. |

All eight are warnings. Emit warning `TEXT_EMPTY` alone for an empty aggregate instead of dividing by zero. Port placeholder folding into the diagnostic helper only; it never changes retained text. Freeze cases immediately below, at, and above every boundary. The legacy bad-character hard-fail uses `>=`, but its diagnostic reason uses `>`; this port takes the diagnostic rule. Existing policy configuration owns any later tuning, which requires a new rule version and qualified evidence.

### Reports and consumer handoff

`Evaluate` returns `model.QualityAccepted` only if all mandatory metrics pass or have independently qualified inapplicability, and no blocking finding exists; otherwise it returns `model.QualityQuarantined`. Neither successful callback execution nor a forged profile can bypass the baseline unknown geometry/fidelity. `needs_ocr` and `accepted_with_gaps` cannot originate in this port.

`Gate.Report` calls `Evaluate`, then constructs existing `formats.GateReport`: `SchemaVersion = gate-report-v1`, artifact/candidate hashes from the request, policy hash and threshold version from the constructor, empty `DecisionRef`, `formats.ReportSentinel()`, and evaluation arrays and outcome. Copy the three `ConversionRefs` values into the corresponding non-nil pointer fields. Their linkage to actual retained conversion bytes is verified by composition, not by pure `Evaluate`.

Encode with `formats.GateReportCodec(policy, limits)`; never use ordinary JSON as a substitute. After catalog registration, `formats.ValidateReportReference(report, ref, policy, limits)` substitutes the catalog-issued reference in a local `model.QualityReport`. Then `api.GateResult.Validate(profile)` checks outcome agreement. Never rewrite the retained sentinel. Model validation alone cannot authenticate a report or recalculate its scores.

`Gate.MissingReport` accepts only `CONVERSION_MISSING` or `RESOURCE_LIMIT`. All metrics are unknown with that reason, plus one blocking artifact finding, null conversion references, and quarantine outcome. For output overflow, including `GATE_REPORT_LIMIT` from evaluation, `Gate.Report` uses `Gate.LimitReport` to replace the entire detailed result with all-unknown metrics and one blocking `REPORT_LIMIT` finding, preserving the three conversion references. Both minimal helpers use the same pinned profile, sentinel, artifact scope, empty decision, and non-null arrays as regular reports. Invalid hashes or references fail; they are never repaired.

Determine size failures through explicit budget checks, not substring matching codec errors. Other codec failures return a fixed input or policy error without a fallback. M1.14 checks the actual catalog-resolved projection before mutation and can request `LimitReport` when that projection or an edit exceeds adapter limits. No partial report or partial writer rows survive. If even the minimal report fails codec or adapter bounds, return a fixed error and write no report; no helper promises success under arbitrarily small limits.

M1.14 owns actual report staging, catalog registration, projection, and `formats.GateRollup` composition:

- Require one artifact report per candidate ordinal, independent of source observation ordinal. Check original/candidate/policy/threshold bindings. Missing conversions have explicit missing ordinals and reports. Use `formats.GateRollupCodec`; it cannot prove relationships to unopened artifacts on its own.
- Apply approved precedence: explicit OCR work yields `needs_ocr`; otherwise any missing conversion, unknown mandatory metric, blocking finding, quarantined artifact, or unapproved gap prevents eligibility. No OCR or decision flag upgrades a baseline metric.
- Preserve the mandatory unknown geometry/fidelity in roll-up metrics. Other roll-up definitions require independently inventoried inputs; unknown remains unknown. Do not sum rates or use a successful artifact as evidence for missing artifacts.
- Build exact aggregate-text partitions from verified text object sizes. Use `formats.ValidatePartition` and, for synthetic scope comparisons, `formats.ValidateScopeBinding`. Codec shape checks alone do not prove full coverage. Source-span block IDs are not aggregate-text ranges.
- Named-gap acceptance remains blocked for this baseline. A qualified retained-range evaluator and real decision authority are separate prerequisites. `gap-scope-v1` is synthetic scope evidence, not authorization. Keep `DecisionRef` empty otherwise.
- Apply [writer projection](document-conversion-formats.md#projection-into-writer-rows), including token removal only in row projections, span-derived evidence resolution, non-null arrays, record ordering, and adapter size limits. Registration precedes persistence. No frozen `run` or store type is changed here.

## Residency screen boundary

**Decision:** Keep both egress and artifact classification inside the existing sandbox boundary.
**Why:** The frozen port says so. A capture worker with network access is not the no-network converter sandbox.
**Rejected:** Reinterpreting worker placement as sandbox isolation, silently screening each hop in-process, or bypassing screening after a timeout.
**Detail:** A new residency child must use `conversion/sandbox` supervision on `corpus-converter-v2`, with no network or credentials, read-only inputs, bounded scratch/output, and verified teardown. Existing `conversion.ChildHandler` implements conversion, not screening. A residency child, bundle, validation handler, and receipt adapter are new work. Do not claim real launcher execution from short tests with a direct or fake launcher.

Each redirect and certificate AIA request must receive its own admission before network access. Sandbox startup, screening, verification, and teardown consume the capture's existing deadline. Insufficient time refuses export. This design grants no longer budget or new retry budget. M1.14 must measure the per-hop cost before enabling capture; an in-process alternative would need an explicit contract decision, not a comment interpretation.

### Open residency formats

**Decision:** Treat residency serialization as a separate contract slice, not an extension of approved conversion formats.
**Why:** Outbound bytes precede any response and cannot form a valid `CaptureRef`. The envelope's two fields do not authorize arbitrary new persisted formats.
**Rejected:** Fake transport receipts; invented captures; adding residency keys to `policy-bundle-v1`; treating a new package location as format approval.
**Detail:** Proposed names `screen-request-v1`, `residency-report-v1`, and `residency-policy-v1` are all new and unapproved. No codec for them exists. R1 below blocks their implementation until exact types, canonical bytes, validation, retention, and fixtures are approved. Existing envelopes and `api.ResidencyRequest` remain unchanged.

The contract slice must freeze these semantics, not leave them to an implementer:

| Concern | Required binding |
|---|---|
| Request union | Distinguish canonical outbound request bytes from captured-artifact screening. The latter retains the complete valid `api.ResidencyRequest`; the former has no fabricated capture. Bind subject asset, policy, budget, operation, country, and source association. |
| Policy resolution | `PrepareExport` receives a digest, not an asset reference. Resolve a pinned local policy by digest, verify exact retained bytes, and reject missing policy. No network lookup or caller-selected replacement. The residency policy is separate from the quality bundle. |
| Subject and evidence | Bind the hash/size of exact bytes eligible for export separately from any extracted text used to classify them. Bind each text/metadata input and its conversion provenance to that subject. A normalized-text hash cannot authorize sending an original binary. |
| Results | Bind request, subject, policy, operation, and deterministic rule version. Local result contains class and fixed diagnostic counts only, with no excerpts or offsets. `ResidencyAdmission.Report` references the verified local result. Envelope `Manifest` hashes exact retained manifest bytes. |
| Identity and replay | Keys use the existing `screen-`/`residency-` prefix and 32 lowercase hex digits, issued and resolved by local storage. Syntax or truncating a content hash does not prove issuance. Changed subject, policy, budget, operation, or evidence invalidates reuse. Verify complete digest and size; never authorize from key syntax. |
| Storage | Immutable, private Vietnam storage; authorized resolution only. Define atomic staging, crash/retry behavior, cleanup, and retention ownership before wiring M1.15. Generic catalog registration is not permission to include residency reports in the accepted evidence closure. |
| Bounds | Cap policy bytes, rules, regex length, total decoded bytes, items, matches, output bytes, and sandbox resources before allocation. Use Go RE2 semantics and checked arithmetic. Every rule must finish within the shared budget; a match-count cap cannot stop remaining rules and yield public admission. |

### Classification and public admission

`model.ResidencyPublic`, `model.ResidencyPersonal`, and `model.ResidencyUnclassified` exist. Only the first can admit. Budget exhaustion, cancellation, incomplete input, unsupported media, unavailable deterministic text, or incomplete detector execution refuses admission. Return an unclassified local result only when it can be safely retained; otherwise return a fixed failure and no envelope. An error never means allow, including on retry.

**Decision:** An absence of detector matches cannot establish public, non-personal status.
**Why:** Phone, email, and identity-number patterns do not cover names or all identifying context. Conversion can omit image text, headers, comments, or attachments that still exist in exported originals.
**Rejected:** Identifier-only clearance, official-source/type clearance for scans, a two-match identity-number exemption, and a blanket official-signatory name exemption.
**Detail:** Baseline classification is unclassified unless positive personal-data evidence blocks it. Public admission needs an approved, versioned procedure proving public provenance and complete inspection of the actual export surface. Synthetic positive-admission fixtures may exercise that procedure through an explicit test-only dependency, never a production bypass. R2 owns the production policy.

Detector examples such as twelve-digit IDs, nine-digit IDs, email, phone, and tax-number contexts are proposals for blocking signals, not legal definitions. Freeze exact regexes, Unicode/word boundaries, URL percent decoding, header-name folding, supported body encodings, match counting, and unsupported-encoding rejection in R2. A literal name sentinel need not match an identifier detector; it must still never escape through a public admission.

No M1 residency override exists. `model.DecisionKind` contains retry, OCR, named-gap, exclusion, and alternate-source actions only. None clears residency. No new decision kind or migration is proposed here. Scans without complete in-country inspection remain unclassified and cannot go to Vision merely to discover whether they contain personal data. Never silently redact the canonical original to obtain admission.

The consumer verifies subject and policy against the retained result immediately before building any external payload. Changed prefixes, titles, metadata, filenames, URLs, or body bytes require another screen. OCR and embedding require separate bindings for their actual inputs. Provider credentials are controlled transport configuration, never screened corpus fields or logged data. No user question is eligible for this path.

### Private results and outward sinks

Screening observations, including inspected text, match offsets, and residency classification, stay inside Vietnam. Never copy them from a screen result into envelopes, logs, traces, error strings, Temporal history/search attributes, exports, releases, or batch manifests. Classification-specific reason codes and detector counts can reveal the class too; retain them only in the private result. Local `ResidencyAdmission` must never be serialized as a workflow payload. An admitted payload comes from the separately bound immutable subject, never from excerpts returned by the screen. Rejected subject bytes and their source offsets remain private.

Only existing reference envelopes cross an orchestration boundary. Opaque references are resolved in-country, not embedded in provider manifests. An outward failure uses one generic refusal category, independent of detector, class, filename, or matched value. Never wrap child stderr, parser/regex errors, input paths, policy fragments, or manifest content. The package has no logger. Operational counts may record refused work in aggregate without subject keys or classification breakdown.

Capture already uses generic failures such as `residency_rejected`; do not replace them with local classifications. Rejected or unscreened artifacts produce no external batch item, including no rejected hash, report reference, detector code, or reason. Detailed exclusion accounting stays private. A batch manifest contains only admitted payloads and their required non-personal processing metadata, not a rejection summary.

Quality reports retain exact provenance and are not a sanitizer. Before any conversion/report closure leaves Vietnam, composition must verify admission of everything actually exposed. Public evidence, protected real-fixture uploads, OCR payloads, and bulk embedding payloads each need this enforcement. Current `run` types do not prove that the ledger rejects unscreened OCR or embedding registrations; M1.14 and the owning batch slices must implement and test those consumers. This spec claims no such deployed enforcement.

## Verification and privacy sentinels

Use synthetic inputs only in Git and ordinary hosted CI. Sentinel helpers belong in test files or a shared test helper package; no production detector imports the fixture list. Assertion failures print a case ID and sink name, never the sentinel, received bytes, or classification.

| Suite | Required proof and negative control |
|---|---|
| Gate | Pinned-profile equality and order; non-null arrays round-trip through real codecs; mandatory unknown geometry/fidelity cannot pass even if made optional in an incoming profile; mutated evaluation is not consumed by `Gate.Report`. |
| Metric limits | Empty text, invalid UTF-8, non-NFC text, unsupported exclusions, partial spans with valid block IDs, foreign hashes, duplicate ownership, cycles, and missing parents cannot manufacture a pass. Test declaration caps, nested limits, exact limit/limit+1, and minimal-report failure. |
| Diagnostics | Golden findings and bytes; exact threshold grammar and integer boundaries; aggregate versus short-block behavior; each legacy trigger and adjacent non-trigger; changing one trigger in a disposable negative control fails its named case. No legacy import. |
| Provenance | Verify unchanged source span coordinates in retained reports and deliberate token removal only in projection fixtures. A sentinel in `Member`, `ScopeKey`, or an evidence path may remain in local evidence; verify that its closure cannot reach an outward sink. |
| Residency classification | Freeze separate cases for detector matches, no match without public proof, unsupported text/encoding, truncated inspection, budget/cancellation, malformed policy, corrupt manifests, and wrong subject/policy/operation. Disabled detectors must remove the corresponding local detection while still refusing public admission without positive proof. |
| Admission | Captured subject A cannot authorize B, original bytes cannot reuse normalized-text admission, and changed request body/metadata cannot reuse a receipt. Malformed, missing, stale, or tampered result yields zero export calls. Count redirect and AIA authorization calls. |
| Sink inventory | Capture exact serialized envelopes, every outward error, structured logs/traces, child stdout/stderr forwarding, export-spy arguments, and OCR/embedding manifest bytes. Assert allowed fields as well as sentinel absence. Private reports may contain class, never matched text or offsets; outward sinks contain neither. |
| Sensitivity | In a test-only control, inject one sentinel or a class/diagnostic field into each sink and require the detector to fail. In another control, bypass admission and require the export-count test to fail. A control must never enable a production switch. |

Fixtures cover the existing six API-envelope sentinel strings plus synthetic Vietnamese names, reserved-domain email, digit-shaped IDs, phone, and tax contexts. Put each independently into URL query/path, request header and body, HTML, DOCX text, filename/title, and evidence locators. Include percent/JSON escaped representations and encoded body variants actually supported by policy. A response header is not an outbound capture-request header; test it when later composed into an export.

M1.13 tests exercise real codecs and local screen adapters with spies. M1.14 must add actual Temporal SDK histories, retries, heartbeat/failure details, search attributes, batch preparation and submission, privacy-preserving test artifacts, and the real sandbox launcher. Fake histories or hand-built manifests prove no integrated exclusion. Failed, cancelled, missing, or skipped evidence is not a pass.

The [hosted matrix](../../instructions/verification.md#hosted-check-matrix) currently provides the `go` job for build, vet, and short tests. Repository hygiene and security checks also apply. `privacy-contracts`, `converter-sandbox`, and `vietnam-intake.yml` are planned I-lane work, not existing checks. Short tests with fake/direct launchers do not satisfy those evidence gates. No local build, test, generation, or network call is required for this design review.

## Owner questions and blocking handoffs

The available confirmation of Điều 20 is the author's reading of a [secondary mirror](https://hethongphapluat.com/luat-bao-ve-du-lieu-ca-nhan-2025.html/dieu-20), not the first-party PDF. The cited [Công Báo listing](https://congbao.chinhphu.vn/detail/tai-ve?id=45578&slug=91-2025-qh15) is the reported official record for Luật Bảo vệ dữ liệu cá nhân 91/2025/QH15, with reported effective date 01/01/2026. First-party article text and any exemption remain unverified for this design. The binding engineering policy is the repository's no-personal-data-egress rule. Do not infer an official-capacity exception or compliance determination from these citations.

| Item | Recommended answer | What it blocks |
|---|---|---|
| G1: Approve baseline consumer rules? | Approve the bounded threshold grammar, unknown metrics, diagnostics, and unchanged two-metric synthetic bundle above. Keep O7 production thresholds and O8 authority separate. | M1.13-2A implementation until this design is approved; no production qualification is granted. |
| R1: Approve new residency byte contracts? | Freeze all three separate residency formats in a contract slice, with exact types, limits, canonical fixtures, hash bindings, and private retention. No addition to the approved conversion closure. | Residency codec, storage, and screen-adapter implementation. |
| R2: What can establish public/non-personal status? | Obtain first-party legal confirmation and an owner-approved positive admission procedure; keep all uncovered content unclassified. No blanket name exemption, scan exception, or override. | Real-fixture upload, production capture/OCR/embedding, and any production public-admission rule. Synthetic denial tests can proceed after R1. |
| R3: How to meet the sandbox contract? | Use a dedicated screen child under the existing sandbox for both subject kinds; measure it within existing deadlines. | Executable `api.ResidencyScreen` and real capture enablement until child/bundle/receipt validation and launcher evidence exist. |
| R4: Where are private results retained? | Use private Vietnam storage outside accepted/exported closures, with explicit ownership, replay, and retention rules; no expiry in M1 until an approved cleanup contract exists. | Durable adapter and M1.15 integration; do not assume generic quality catalog ownership suffices. |
| H1: Who reconciles delivery records? | The primary updates the Vietnam plan, stale phase-1 queue entry, and formats implementation status, and records the named slices and hosted mapping. | Dependent implementation dispatch while shared records disagree; these files are outside this design's edit ownership. |
| H2: How are qualified and retained-range metrics added? | M2 freezes producers and evidence bindings in a separate contract design; M1.14 composes only supported outputs and keeps baseline quarantine. | Measured qualification and named-gap acceptance through this gate; not baseline gate implementation. |

## Slices and ownership

This work exceeds one implementation review. Each named slice is one PR with independent review, including adversarial review for residency and admission paths.

| Slice | Owned paths and handoff |
|---|---|
| M1.13-2A: baseline quality gate | `internal/corpus/quality/{gate,metrics,thresholds,findings,textsignals,report}.go`, matching tests, and `quality/testdata/`. Freeze the new constructor/report surface above, pure evaluation, diagnostics, minimal reports, and codec-backed fixtures. Depends on G1 and existing phase 1. |
| M1.13-2B: residency contract | A separately authorized continuation spec and its index/owning-plan links. Close R1, R4, and the synthetic portion of R2 before implementing formats. Freeze the request union, policy resolution, exact retained types and validation table. No runtime implementation in this PR. |
| M1.13-2C: residency codecs and engine | After 2B, `internal/corpus/quality/residency/{policy,manifest,report,engine}.go`, matching tests, and `residency/testdata/`. Deterministic bounded classification and private byte contracts only. No external provider or production public-admission rule without R2. |
| M1.13-2D: residency sandbox adapter | After 2C and R3, `internal/corpus/quality/residency/{screen,store,sandbox_linux}.go`, matching tests, and `residency/cmd/screen/main.go`. Implement the frozen screen and source seams using a dedicated sandbox child. A new brief must freeze its exact local storage and child protocol from 2B; no invented runtime or storage API is assumed here. |
| M1.14: composition and evidence | Existing plan ownership: staging, catalog and row projection, document roll-up, capture/batch consumers, real launcher, actual histories, and I-lane jobs. Close H1 before dispatch. Keep unsupported qualification and decision paths closed. |

Every implementation slice excludes `quality/api/`, `quality/formats/`, `model/`, `run/`, legacy code, and frozen conversion contracts. Required changes outside those boundaries need an explicit contract slice. The current documentation change owns only this spec and its index row. Independent Sol design review remains required; this improvement pass is not its own approval.

## Frozen-constraint disposition

| Reported constraint | Verified disposition |
|---|---|
| No evidence assets, so no `not_applicable` | No dedicated qualification input, but finding evidence assets do exist. No baseline inapplicability; do not trust incidental evidence. |
| No OCR flag or decision | Confirmed. Consumer applies OCR precedence; baseline named-gap rerun remains unsupported. |
| No page/region/cell/metadata inventory | Confirmed. Four metrics unknown; structure, full provenance, language, and corruption qualification also need evidence the baseline lacks. |
| No country parameter | Confirmed. Constructor binding is the extension point; baseline rejects an unqualified provider. |
| `CaptureRef` cannot describe outbound bytes | Confirmed. R1 must freeze a separate request union, without changing the typed capture request. |
| New screen/report formats | Confirmed, and residency policy is a third new format. Approval and exact codec contracts remain open in 2B. |
| No document-level finding scope | Incorrect. `model.GapDocumentRevision` exists. Artifact findings use `GapArtifact`; revision scope requires a resolved revision key. |
| No threshold grammar | Confirmed. G1 proposes a bounded consumer grammar, not a codec change. |
| No residency override action | Confirmed. No override; no speculative new action or migration. |
| Sandbox comment versus in-process egress | Conflict resolved by retaining the sandbox requirement. R3 blocks an adapter that cannot meet it. |
| Missing hosted check names | Confirmed. Use existing `go` for short tests; actual I-lane privacy/sandbox evidence remains M1.14 work. |
