# Retained conversion codecs

This package implements the M1.13 phase 1 byte contracts. It performs no I/O,
implements neither quality nor residency gates, and grants no review authority.

Every codec exposes `Encode(value)` and `Decode(bytes)`, returning canonical
bytes or decoded values together with the exact SHA-256 digest. Constructors
take trusted `Limits`. Report constructors also take the pinned `PolicyBundle`
and apply the stricter of caller and policy bounds. Unknown fields, duplicate
keys, trailing data and byte differences after re-encoding reject.

| Format | Type and constructor | Canonical rule | Fixture |
|---|---|---|---|
| `normalizer-v1` | `Normalizer`, `NormalizerCodec` | A | `testdata/normalizer-v1.json` |
| `parser-v1` | `Parser`, `ParserCodec` | A | `testdata/parser-v1.json` |
| `candidate-input-v1` | `CandidateInput`, `CandidateInputCodec` | A | `testdata/candidate-input-v1.json` |
| `candidate-output-v1` | `CandidateOutput`, `CandidateOutputCodec` | A | `testdata/candidate-output-v1.json` |
| `policy-bundle-v1` | `PolicyBundle`, `PolicyBundleCodec` | B | `testdata/policy-bundle-v1.json` |
| `gate-report-v1` | `GateReport`, `GateReportCodec` | A | `testdata/gate-report-{complete,pre-conversion,needs-ocr}.json` |
| `gate-rollup-v1` | `GateRollup`, `GateRollupCodec` | A | `testdata/gate-rollup-{complete,pre-conversion,needs-ocr}.json` |
| `gap-scope-v1` | `GapScope`, `GapScopeCodec` | A | `testdata/gap-scope-v1.json` |

All manifest and report digests hash the entire encoded object. `TextHash` hashes
the rule A array of normalized-text hashes in candidate order. Expected fixture
digests are pinned in `testdata/digests.json`. JSON byte fixtures have no final
newline. Negative fixtures are indexed by `testdata/negatives.json`.
All negative fixture payloads use `.input`, including deliberately invalid JSON.
`candidate-input-reprocess.json` pins a non-null predecessor and an empty HTML
selector for DOCX. The first-pass input pins the null predecessor shape.
Both inputs use `observation-` and `capture-` with 32 lowercase hexadecimal
characters. Execution keys use the same suffix with `execution-`. Document
version, artifact and artifact scope keys use the lowercase local-key grammar.
The predecessor revision uses the country, document UUID and full digest grammar.

`BaselineNormalizer` derives its rule, version and transforms from the five
exported M1.12 constants. Rules sort by rule/version; transforms sort by name.
`ValidateNormalizationBinding` checks decoded map and ledger membership.
It accepts frozen M1.12 nullable collections, including the unchanged-text
converter's `null` edit ledger, without changing retained bytes or digests.
Proposed formats still require non-null arrays.

`PolicyFromJSONB` reads policy numbers without float conversion and returns rule
B bytes and their digest. It accepts JSONB whitespace and key ordering, while
rejecting duplicate keys, nulls, unknown keys, floats and exponent notation.
`PolicyProfiles` explicitly maps the policy's snake-case requirements to two
independent `model.QualityMetricProfile` values bound to the policy digest.

Both profiles require mandatory geometry and fidelity. Other known checks,
thresholds, not-applicable permissions and gap reasons remain configurable. The
spec does not enumerate a complete default profile or gap-reason allowlist; the
fixture's reasons are synthetic. No production list is invented here.

`DefaultLimits` returns proposed, unmeasured values under owner gate O7:

| Bound | Default |
|---|---|
| `max_blobs` | 1024 |
| `max_rows` | 10000 |
| `max_json_bytes` | 4194304 |
| `max_object_bytes` | 67108864 |
| `max_elapsed_ms` | 60000 |

All bounds must be positive; `max_blobs` cannot exceed 1024. Codecs bound encoded
JSON, aggregate decoded strings, collection entries, root objects and directly
referenced assets. Consumers enforce full closure counts, frozen work budgets
and elapsed time before object reads. O7 approves production values before
deployment and real-document acceptance, rather than before synthetic codecs.

`ReportSentinel` is exactly `reports/self`, 64 zero hash characters and zero
bytes. Codecs validate a local copy with its computed content reference.
`ValidateReportReference` verifies catalog hash and size, substitutes the actual
catalog reference in a copy and calls frozen `QualityReport.Validate`. Retained
bytes keep the sentinel. Reports keep unknown geometry/fidelity and blocking
findings; conversion success cannot make them eligible.

`ValidateCandidateBinding`, `ValidateRollupBinding` and `ValidateScopeBinding`
compare decoded selections. `ValidatePartition` proves retained/excluded
coverage against supplied aggregate text references, including explicit gaps
for empty or missing text. Partial conversion has no complete output manifest;
the consumer supplies decoded document text references to that partition check.
`ValidateSpanReferences` resolves span hashes only through typed closure assets.
Consumers still verify retained object bytes, sandbox membership, source
inventory, provenance, execution identity and decision authority.
Predecessor identity and comparison requirements resolve through acceptance
history; the predecessor's source version may differ from the current version.

`ProjectQualityRows` emits metrics, findings and gaps in order, with null fields
outside each record kind. It resolves qualification and span evidence through
the catalog, deduplicates by hash and sorts evidence. `ProjectReportSpan` clears
token ordinals and substitutes `document` only for an empty block ID. Model
coordinate names remain in report JSON; `WriterSpace` maps SQL coordinates.
Logical and catalog paths have separate conflict checks. Hash-size consistency
and resource budgets apply across both namespaces. Span object, member and
reason strings must be valid UTF-8 and contain no NUL characters.
`ProjectArtifactParent` and `ProjectRollupParent` bind parent content fields to
catalog report bytes. Targets and origin execution remain consumer bindings.
Row projection enforces the 1000-child and 4 MiB child-array ceilings; the writer
must also enforce 4 MiB on its final transport envelope before mutation.

Short tests pin canonical round trips, digest stability, all negative fixtures,
Go escaping, nested duplicate keys, exact integer precision, configurable
bounds, normalizer derivation, policy snapshots, candidate/report selections,
sentinel substitution, mandatory metric rejection, OCR precedence, coverage
partitioning and quality-row projection. These tests use synthetic evidence.
Hosted build, vet, short tests and format checks remain the verification gate.

Adversarial review should examine canonical bytes and absence shapes, duplicate
keys at every depth, hash preimages, root/dependency accounting, arithmetic
overflow, catalog paths and sizes, span coordinates, complete ordinal selection,
qualification evidence preservation, exact partitions and indexed null fields.
