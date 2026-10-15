Approved by the owner: R1 byte contracts, R3 sandbox approach, R4 private retention,
and synthetic R2 admission. The 2C codecs and engine are coded; the 2D adapter remains unimplemented. Production R2 remains open. The [Vietnam plan](../plans/vietnam.md#m113-initial-quality-and-residency-gate)
owns delivery. This continuation of [quality gate and residency](quality-gate.md#residency-screen-boundary)
freezes M1.13-2B requirements for 2C and 2D. It grants no legal determination or production clearance.

# Residency contract

## Types and canonical bytes

All new types live in `internal/corpus/quality/residency`. Existing `quality/api`, `model`,
`quality/formats`, conversion formats and sandbox protocols stay unchanged. Tables below give
declaration order. Comma-separated fields have the stated Go type; there are no omitted fields.
Rule A means `encoding/json.Marshal` with exported field spelling, no tags, no indentation and no
final newline. Rule B uses explicit snake-case maps with recursively byte-sorted keys. Both use
standard Go escaping, including HTML characters and U+2028/U+2029. Integers use shortest decimal
notation; `time.Duration` is integer nanoseconds. No floats, exponent notation or negative zero.
No Unicode normalization occurs in retained bytes. All strings must be valid UTF-8 without NUL.
Reject unknown/missing keys, duplicates at every depth, trailing values, null arrays, invalid UTF-8,
unpaired surrogate escapes, depth above 64, and any byte difference after canonical re-encoding.
Optional pointers encode `null`; empty arrays encode `[]`. Decode never repairs bytes.
Every digest is lowercase SHA-256 over the entire exact object; every size is its exact byte count.
Use phase 1's root/dependency accounting and path/hash/size consistency rules, without changing its codecs.

| Type | Ordered fields and Go types | Encoding |
|---|---|---|
| `ScreenRequest` | `SchemaVersion, Country, SourceID, OwnerKey, Operation, SubjectKind string`; `Subject model.AssetRef`; `MediaType string`; `Policy model.AssetRef`; `Budget model.ResourceBudget`; `Capture *api.ResidencyRequest`; `Evidence []ScreenEvidence` | A, `screen-request-v1` |
| `ScreenEvidence` | `Kind string`; `SubjectHash model.Digest`; `Asset model.AssetRef`; `Conversion *ConversionEvidence` | A, nested |
| `ConversionEvidence` | `Request, Document, Outputs model.AssetRef` | A, nested |
| `ResidencyPolicy` | `SchemaVersion, Country, RuleVersion, Admission string`; `Limits ResidencyLimits`; `Rules []DetectorRule` | B, `residency-policy-v1`; keys `schema_version,country,rule_version,admission,limits,rules` |
| `DetectorRule` | `ID, Pattern string` | B, keys `id,pattern` |
| `ResidencyLimits` | Every field in the limits table is `int64`, in that table's order | B, snake-case keys in the limits table |
| `ResidencyReport` | `SchemaVersion string`; `Request model.AssetRef`; `Subject ByteIdentity`; `Policy model.AssetRef`; `Country, SourceID, OwnerKey, Operation, RuleVersion string`; `EvidenceHash model.Digest`; `Complete bool`; `Class model.ResidencyClass`; `ReasonCode string`; `Counts []DetectorCount`; `AdmissionProof *model.Digest` | A, `residency-report-v1` |
| `ByteIdentity` | `SHA256 model.Digest`; `ByteSize int64` | A, nested; excludes source paths |
| `DetectorCount` | `ID string`; `Matches int64` | A, nested |

`api` above is `quality/api`. The frozen nested model field orders are `AssetRef{Path,SHA256,ByteSize}`,
`ResourceBudget{MaxItems,MaxInputBytes,MaxPages,MaxElapsed,MaxConcurrent}` and
`ResidencyRequest{Artifact,Policy,Budget}`. `Artifact` is the complete unchanged `CaptureRef`,
including country, source, capture key, asset, media type, request/response hashes and transport receipt.
Validate every existing model value with its validator and the additional constraints below.

2C exposes `RequestCodec(limits ResidencyLimits) Codec[ScreenRequest]`,
`PolicyCodec(limits ResidencyLimits) Codec[ResidencyPolicy]`, and
`ReportCodec(policy ResidencyPolicy, limits ResidencyLimits) (Codec[ResidencyReport], error)`.
The new package's `Codec[T]` has phase 1's `Encode(T) ([]byte,model.Digest,error)` and
`Decode([]byte) (T,model.Digest,error)` contracts. It does not reuse inaccessible codec internals.
`ValidateReportBinding(request ScreenRequest, report ResidencyReport, policy ResidencyPolicy) error`
re-encodes all three values and verifies every binding below. Shape validation alone never admits.

`NewEngine(policy ResidencyPolicy, limits ResidencyLimits) (*Engine,error)` copies validated options.
`(*Engine).Evaluate(ctx context.Context, request ScreenRequest, objects []ResolvedObject)` returns
`(ResidencyReport,error)` without I/O. `ResolvedObject{Ref model.AssetRef; Bytes []byte}` supplies the
exact bounded closure, sorted/deduplicated as bundle Objects below. Evaluate authenticates every byte
and binding before inspection; it never trusts a supplied report. Context is only for cancellation/deadline.
2D exposes `NewScreen(ScreenOptions) (*Screen,error)`; Screen implements both existing residency ports.
`ScreenOptions` fields are `Country,SourceID,OwnerKey,Root string`, `PolicyHash model.Digest`,
`Budget model.ResourceBudget`, `Limits ResidencyLimits`, `Recipe sandbox.Recipe`, `Launcher sandbox.Launcher`.
Root is private runtime configuration. The immutable local policy set is `Root/policies/<digest>`.
Constructor validates the pinned policy and identity; methods verify the policy again before use.
Screen constructs supervisors with the frozen ceilings below and current remaining deadline.
No constructor accepts a positive-admission callback. PrepareCapture's full signature is
`PrepareCapture(context.Context, api.ResidencyRequest, []ScreenEvidence) (api.ResidencyRequestEnvelope,error)`.
Captured assets/evidence resolve only from pre-staged `Root/inputs/<digest>` immutable bytes by hash/size;
M1.14 copies these privately before preparation. Unlisted paths never become filesystem access paths.
`ResolveAdmission` accepts only `capture_export` results. Captured consumers use
`ResolveCaptureAdmission(context.Context, model.ResidencyEnvelope, api.ResidencyRequestEnvelope)`
returning `(model.ResidencyAdmission,error)` and require the exact expected request and `artifact_export`.
Both compare retained country/source/owner/policy/budget to construction before returning any admission.

## Request union and evidence

Country is exactly `vn`. SourceID is a configured source identifier matching `[a-z][a-z0-9_-]{0,63}`.
OwnerKey must validate as an `execution-` envelope key. It names the original local execution,
not the retry worker. `Operation` and `SubjectKind` are closed pairs:

| SubjectKind / Operation | Required shape |
|---|---|
| `outbound_http` / `capture_export` | Null Capture, empty Evidence, MediaType exactly `application/http`; Subject names the exact `source/http.ExportBytes` output. No fabricated response or transport receipt. |
| `captured_artifact` / `artifact_export` | Non-null Capture; Subject equals Capture.Artifact.Asset; Policy/Budget equal Capture.Policy/Budget; Country/SourceID equal Capture.Artifact country/source; MediaType equals Capture.Artifact.MediaType. CaptureKey validates with prefix `capture`. |

`PrepareExport` cannot receive source or execution identity. 2D therefore constructs one adapter per
resolved source execution with immutable Country, SourceID, OwnerKey, pinned policy digest and budget.
It compares method arguments to that binding, copies the exact bytes, and issues the request envelope.
No source identity is inferred from a URL. M1.14 supplies this constructor binding after resolving
the source job; source ports and `Capture.Capture` stay unchanged. The same adapter handles redirects
and AIA but stages a separate subject per distinct serialized request. No user query enters this path.
2D also exposes `PrepareCapture(ctx, api.ResidencyRequest, []ScreenEvidence)` returning the existing
request envelope; it uses the same constructor binding. There is no caller-provided classification.

Resolve Policy by the pinned digest from a local, operator-installed immutable policy set. Require
`Policy.Path = policies/<digest>`, exact hash and size, and equality with the configured policy bytes.
Missing, corrupt, wrong-country or replaced policies reject; no network or fallback policy lookup.
Admission is exactly `closed`; RuleVersion is exactly `residency-signals-v1`. Rules are exactly the
five rows below, sorted by ID. Changed patterns, missing rules or additional rules reject in production.
A new detector set needs a new rule version and reviewed contract, not a runtime regex setting.

Subject paths are `subjects/<digest>` for outbound bytes. Captured logical paths stay unchanged.
Evidence order is `(Kind,Asset.SHA256,Asset.Path)` with no duplicate tuple. Each SubjectHash equals
Subject.SHA256. `metadata_utf8` has null Conversion and contains exact metadata bytes supplied by
the trusted producer. `converted_raw_utf8` has non-null Conversion and Asset equals the bound
`blocks-v1` document's RawText, never its NormalizedText. These are the only evidence kinds.
Metadata is diagnostic evidence, not proof that all export metadata was supplied.

For converted evidence, resolve all three references and their complete typed conversion closure
using the [frozen conversion contract](document-conversion-formats.md#closure-traversal-and-bounds).
Inside the screen child check canonical bytes, complete sorted output membership, hashes/sizes,
request Artifact equality with Capture.Artifact, document Original equality, recipe/candidate/request
bindings and `Document.RawText == Asset`. Require the document and all output members to belong to
that conversion's output manifest; never use a span hash as an object-discovery mechanism. Preserve
M1.12 nullable arrays unchanged. These checks establish binding, not extraction completeness or quality.
No report can authorize an original using only an extracted-text hash. Missing provenance rejects.
Converted evidence never upgrades an opaque or unsupported subject to completely inspected.

Every later OCR, embedding or release consumer must screen the actual payload and exposed metadata.
`artifact_export` covers only the exact Subject bytes, not an entire object closure. A title, filename,
prefix, URL or manifest added to an export requires its own bound subject. Later composite export
formats need their own contract; neither operation here is a blanket approval for those exports.

## Synthetic inspection and blocking rules

**Decision:** Freeze conservative signals and leave production admission closed.
**Why:** Pattern absence cannot establish that a person is unidentifiable or that extraction is complete.
**Rejected:** Name exemptions, scan exemptions, implicit public-source clearance and redaction overrides.
**Detail:** The following rules are blocking engineering signals only, not a legal definition of personal data.

Regexes use Go `regexp` RE2 semantics with leftmost-first, non-overlapping matches per rule per view.
Patterns below are literal regex strings, before JSON escaping. No caller-selected flags apply.

In ID order `email`, `id12`, `id9`, `phone`, `tax`, the exact patterns are:

```text
[A-Za-z0-9.!#$%&'*+/=?^_\x60{|}~-]+@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+
[0-9]{12}
[0-9]{9}
(?:\+84[ .-]?[0-9](?:[ .-]?[0-9]){8}|0[0-9](?:[ .-]?[0-9]){8})
(?i:ma[ \t]+so[ \t]+thue|mã[ \t]+số[ \t]+thuế|mst)[ \t]*[:=]?[ \t]*[0-9]{10}(?:-[0-9]{3})?
```

For every match require both adjacent runes, when present, outside `unicode.IsLetter`,
`unicode.IsNumber`, `unicode.IsMark` and `_`. Do not use ASCII `\b`. Email also rejects an adjacent
ASCII email local-part character on the left or `[A-Za-z0-9.-]` on the right. Enumerate RE2 candidates
first, discard failed boundaries, then count. No sliding retry inside a rejected candidate.
ASCII digits only; no digit folding, name lexicon, transliteration, NFC rewrite or case folding beyond tax.
Unicode category behavior follows the repository's pinned Go toolchain; toolchain changes require fixtures.

Each view is inspected independently, with no concatenation across fields. Visit views in this order:
subject views below, then evidence asset bytes in Evidence order. Metadata and converted raw text
must be UTF-8. For a supported text artifact, one subject view contains its exact bytes. Supported
artifact media types are `text/plain` and `text/html`, with no parameter or only `charset=utf-8`
(ASCII case-insensitive media/parameter comparison). Inspect HTML literally, including tags, comments
and attributes; no DOM extraction or entity decoding. HTML is always incomplete because literal
inspection does not cover rendered/encoded content. Every other artifact media type is opaque/incomplete.
DOCX/PDF/image evidence can supply blocking signals, but cannot supply Complete=true.

Outbound HTTP inspection uses `http.ReadRequest` on bounded ExportBytes, consumes one request and EOF,
and rejects malformed framing, duplicate Content-Length, simultaneous Content-Length and Transfer-Encoding,
folded headers, trailers, userinfo, fragments and invalid percent escapes. Require HTTP/1.1 and an
origin-form target beginning `/`; CONNECT and `*` are unsupported. Header names must be ASCII HTTP
tokens. Fold ASCII A-Z to a-z, group case variants, sort names bytewise and preserve value wire order.
Views are method, raw target, decoded target, host, then each folded header name followed by each value,
then body views. Keep Host once in the host view. Values trim only surrounding SP/HTAB as HTTP parsing
does; no other header decoding or Unicode folding. Scan Authorization/Cookie fields if present;
they confer no admission and must not contain provider credentials in corpus-supplied bytes.

Decode target path with `url.PathUnescape` once. Decode the whole query with `url.QueryUnescape` once
(including `+` to space), join decoded path and query with `?` only if `?` was present. Raw target and
decoded target are separate views even when equal. A decoded view containing another `%HH` escape
is unsupported, rather than recursively decoded. Invalid decoded UTF-8 rejects inspection.
Transfer-Encoding is absent or exactly `chunked`, which ReadRequest removes before body inspection.
Content-Encoding is absent or exactly `identity` after ASCII folding; gzip, br, deflate and multipart
are unsupported. Empty body needs no Content-Type. Nonempty body accepts UTF-8 `text/plain`,
`application/json` or `application/x-www-form-urlencoded`, with only the optional UTF-8 charset.
Plain body supplies one view. Form body supplies raw bytes then one QueryUnescape view, with the
same residual-escape rejection. JSON supplies the raw view then each decoded key and string in lexical
token order, including repeated string values; reject duplicate object keys, null root, multiple roots, invalid UTF-8,
unpaired surrogates and depth >64. Numbers/booleans/null inside a JSON object or array need no extra view.
Root must be an object or array. JSON numbers remain exact tokens, never float conversions.

Matches count per rule per view; repeated raw/decoded appearances count again. Counts never deduplicate
across fields. Scan all five rules and all views. Check budgets before allocating or appending, and
cancellation between views and regex calls. Stop at the first exhausted bound with an error, no report.
Unsupported HTTP body media, encoding, target form or residual escapes yields UNSUPPORTED_ENCODING
with zero counts, discarding partial counts. For incomplete HTML or opaque artifacts with valid evidence, inspect
available evidence for signals; a positive count blocks, otherwise remain unclassified and incomplete.
Malformed HTTP/JSON/UTF-8, corrupt evidence and truncated input are errors, never partial reports.

## Results and positive test dependency

Request in the report is `requests/<request digest>` with exact canonical request size. Subject binds
the request subject's hash and size only; Policy equals its reference. Country, SourceID, OwnerKey and Operation equal the request.
RuleVersion equals policy.RuleVersion. EvidenceHash is SHA-256 of the rule A Evidence array, including
`[]`. Counts contains all five IDs in policy order, each between zero and the match ceiling.
No inspected text, location, offset, filename, free-form diagnostic or clock field occurs in a report.

| Outcome | Complete / Class / ReasonCode / AdmissionProof |
|---|---|
| Any positive count after completed bounded inspection | Coverage flag / `personal_data` / `SIGNAL_PRESENT` / null |
| Supported and fully inspected, no match, no test proof | true / `unclassified` / `NO_POSITIVE_PROOF` / null |
| Incomplete HTML or opaque subject, no match | false / `unclassified` / `INCOMPLETE_SURFACE` / null |
| Unsupported encoding | false / `unclassified` / `UNSUPPORTED_ENCODING` / null; all counts zero |
| Exact synthetic positive proof, complete inspection, no match | true / `public_non_personal` / `SYNTHETIC_PROOF` / proof digest |

Complete means all supported views were inspected, not legal clearance. Nontext artifacts without
evidence use INCOMPLETE_SURFACE. Unsupported body encoding takes precedence over all other outcomes.
Report codecs reject every other combination, including public with a match or incomplete coverage.
`ResidencyAdmission` is constructed only after verified resolution: ArtifactHash=Subject.SHA256,
PolicyHash=Policy.SHA256, class/reason copied locally, Report=`reports/<report digest>` and exact size.
No self-reference sentinel is needed because the report contains no ResidencyAdmission or own reference.
The outward ResidencyEnvelope.Manifest is the report digest, not the sandbox output-manifest digest.

**Decision:** Positive proof exists only in Go test compilation, with no production selection mechanism.
**Why:** A synthetic oracle is not production R2 authority.
**Rejected:** Environment flags, build tags enabling production admission, exported fixture constructors,
workflow fields, policy admission modes, command flags and importable fixture-provider packages.
**Detail:** Engine and resolver each have a private nil-by-default proof dependency. Only same-package
`*_test.go` can install it; no exported constructor or non-test assignment can set it. Both independently
require exact request bytes/hash, subject size/hash, policy, operation, owner and evidence hash in an
authored test oracle. Proof digest hashes rule A `{Version string, Request model.Digest}` with
Version=`synthetic-residency-proof-v1`. The oracle pins the full request digest before invocation.
Production constructors always leave the dependency nil and reject public reports even if forged
in private storage. Test proof cannot override detector matches, malformed bytes, bounds or incomplete input.
M1.14's test composition can substitute an ExportScreens/ResidencyScreen pair in its own `*_test.go`;
2C/2D exercise the real engine and adapter through same-package test helpers. A test screen executable
is a Go test binary with that helper, never `cmd/screen`. Production child always uses the closed engine.
Real-launcher production-child tests therefore expect refusal; accepting fixture runs identify the test child.

## Bounds and errors

These are frozen synthetic ceilings, not measured production O7 values. All limit fields are positive
and at most the table value. Effective bounds are the fieldwise minimum of policy and trusted caller
limits. Never accept a larger declaration as allocation authority. Request budget is retained unchanged;
its MaxInputBytes additionally caps Subject bytes, MaxItems caps evidence/closure/view counts,
MaxPages caps pages (no page decoding in this version), MaxConcurrent caps the single active child,
and MaxElapsed caps the entire operation. Large frozen source budgets do not enlarge these ceilings.

| ResidencyLimits field / policy key | Maximum |
|---|---:|
| `MaxPolicyBytes` / `max_policy_bytes` | 16384 |
| `MaxRequestBytes` / `max_request_bytes` | 65536 |
| `MaxReportBytes` / `max_report_bytes` | 4096 |
| `MaxSubjectBytes` / `max_subject_bytes` | 8388608 |
| `MaxDecodedBytes` / `max_decoded_bytes` | 67108864 |
| `MaxObjects` / `max_objects` | 128 |
| `MaxItems` / `max_items` | 4096 |
| `MaxEvidence` / `max_evidence` | 16 |
| `MaxRules` / `max_rules` | 5 |
| `MaxPatternBytes` / `max_pattern_bytes` | 512 |
| `MaxMatches` / `max_matches` | 1024 |
| `MaxElapsedMS` / `max_elapsed_ms` | 5000 |

MaxDecodedBytes separately caps distinct retained closure bytes (including roots) and aggregate
inspection-view bytes (including duplicate views). MaxItems counts all nested array entries in a
manifest and all views in one inspection; MaxMatches caps the sum of all rule counts. Cap individual
strings at their enclosing format byte limit; paths at 512 bytes. Charge lengths before reads, decode
base64 only after its encoded-length check, and use subtraction before addition. Closure reads deduplicate
by digest with equal sizes; reject different hashes for a logical path or root self-reference. Enforce
JSON size and aggregate decoded-string size independently. Policy decoding uses trusted limits first,
then validates its declared limits. Bounds that cannot hold the mandatory rules or minimal report fail.

Private error class `ErrorKind string` is one of `INPUT`, `POLICY`, `BINDING`, `LIMIT`, `CANCELLED`,
`DEADLINE`, `STORAGE`, `SANDBOX`. Codecs/engine return only that class with no wrapped cause or payload.
All public adapter errors, including ResolveAdmission failures, become the singleton `ErrRefused`
whose exact text is `residency_rejected`, with no unwrap chain or class-specific fields. No package logger.
Errors return zero values/envelopes. A valid nonpublic report may return a reference envelope and a local
nonpublic admission; source already turns it into generic refusal. Retry never treats an error as allow.

## Dedicated child protocol

Use unchanged `sandbox.ChildMain` with a new residency `ChildHandler`, not `conversion.ChildHandler`.
Only `input.json` exists in the read-only input directory. InputType is `sandbox.TypeJSON`.
New ephemeral rule A `ScreenBundle{Version,Mode string; Request,Policy json.RawMessage;
Objects []ScreenObject; Report *ScreenObject}` uses Version=`screen-bundle-v1`, Mode=`screen` or
`verify`. `ScreenObject{Ref model.AssetRef; Data string}` holds canonical strict standard base64
(padding required, re-encode equality, no whitespace). Request/Policy are their exact canonical JSON.
Objects covers every subject/evidence/transport/conversion dependency, sorted uniquely by SHA256;
Ref uses the byte-smallest logical path for aliases. Resolve aliases by equal hash/size. No unused objects.
Screen mode has null Report. Verify mode has exactly the previously produced report object.
Request/Policy root bytes are not duplicated in Objects. Each bundle is at most 96 MiB, including
base64 and metadata; decoded closure still has the independent 64 MiB ceiling.

Screen handler verifies inputs and emits only `report.json` plus the unchanged sandbox `manifest.json`:
`{"Outputs":[{"Path":"report.json","Type":"json","Size":N}]}` where N is report byte length.
Supervisor Run performs its existing execute child and fresh structural validation child. The adapter
checks Complete, no Finding, exact one-output canonical receipt manifest, manifest digest, expected
`conversion-<issued suffix>` key and pinned output hash/size via Receipt.Open. It rejects every retention
or unconfirmed-accounting marker. This conversion-prefixed key is an internal sandbox requirement only.
It never becomes a conversion-catalog input or outward envelope.

A second Supervisor Run on a verify bundle recomputes inspection and the report in a fresh screen
child and requires exact byte equality. It emits only rule A `ScreenVerdict{Version string;
Request,Report model.Digest}` at `verdict.json`, Version=`screen-verdict-v1`, at most 512 bytes,
with its own unchanged sandbox manifest. The adapter verifies that receipt and both bindings.
Structural sandbox validation alone is insufficient. Both receipts close on every path; close failure
refuses admission. Keep report bytes opaque in the parent until semantic verification succeeds.

Total screen deadline is min(caller deadline, entry time + Budget.MaxElapsed, entry time + effective
MaxElapsedMS). PrepareExport staging, each hop, resolution, both runs, input copying and teardown also
consume the original caller deadline; no deadline or retry allowance is reset. Each Run uses current
remaining time minus 1 second as WallTime and requires at least 2 seconds remaining before launch.
StartupTime=1 second, TeardownTime=1 second, CPUTime=2 seconds per Run (4 seconds total), VCPUs=1,
MemoryBytes=512 MiB, ScratchBytes=192 MiB, OutputBytes=16 KiB, TempFiles=32, InputBytes=96 MiB,
ExpandedBytes=64 MiB, Members=128, XMLDepth=64, Pages=1, Megapixels=1, ExpansionRatio=1.
Lower trusted resource settings may refuse work; never enlarge them to fit. The same caller context
covers retention and receipt close. After teardown or deadline failure, no envelope is issued.
Use corpus-converter-v2 and the existing ContainerLauncher isolation, pinned image and scratch quota.
Short adapter tests use a private fake runner/receipt seam as conversion does; DirectLauncher remains
unavailable outside sandbox tests. No new sandbox method, output type, port or conversion bundle mode.

## Private store, identity and replay

**Decision:** 2D owns a separate local filesystem store in Vietnam, with no M1 expiry.
**Why:** Conversion catalog edges and accepted closures cannot safely own classification records.
**Rejected:** Bronze registration, report evidence aliases, shared release buckets and timestamp expiry.
**Detail:** A configured private root on one Vietnam host uses a durable local filesystem with fsync,
atomic same-filesystem directory rename and advisory locks. No network filesystem in M1. Runtime
service UID alone accesses it: directories 0700, files 0600, no symlink/hardlink/special-file traversal.
Children receive only attempt input copies, never the store root. Backups and recovery stay in Vietnam.

Store layout: `requests/<request digest>/` contains canonical `request.json`, `policy.json`, copied
closure objects at `objects/<digest>` and `identity.json`. Identity uses rule A
`{Version string, Key string, Manifest model.Digest, OwnerKey string}`, Version=`screen-identity-v1`.
Key is `screen-` plus 16 cryptographically random bytes in lowercase hex, never a digest truncation.
The result subdirectory `result/` contains `report.json`, `verdict.json` and `identity.json`; its
identity has Version=`residency-identity-v1`, Key=`residency-<same issued suffix>`, Manifest=report hash,
and the same OwnerKey. Result identity binds through its containing request directory and verdict.
Logical request/report refs are `requests/<digest>` and `reports/<digest>`, resolved only by this store.

Bounded lookups use a private index `keys/<suffix>` containing the request digest as exactly 64 ASCII
bytes, no newline. A store-wide advisory lock serializes publication and index repair, never child work.
Lock waiting is cancellable and deadline-bound. Stage in `staging/<random suffix>`, verify all bytes,
fsync each file then directory, lock, compare any existing request, and rename to the final request
directory without replacement. Fsync the parent, publish and fsync the index atomically, then issue the
envelope. Check suffix uniqueness under the lock before publication; a collision discards no retained
record and retries randomness within the deadline. Index entries cannot authorize missing records.

Results use the same staging/fsync/lock/compare/rename sequence into `result/`. Re-read and verify the
request before publication; retain verdict and report only after both runs and confirmed receipt close.
Concurrent identical requests reuse the original key and owner. Concurrent results must match exact
bytes or fail BINDING, never overwrite. Replay verifies identity, request/policy/subject/dependencies,
verdict and report hashes/sizes and current constructor binding on every resolution. Never authorize
from key grammar, class alone, a cached pointer, or a stale policy. A changed budget, operation, owner,
source, subject or evidence produces a different request; it cannot reuse the old envelope.

Crash before rename leaves unissued staging. Crash after request rename but before index publication
allows an identical Prepare call to repair only the index from the verified identity under lock.
Crash after result rename replays that complete result. A missing, partial or corrupt final record fails
closed and is never repaired by replacing bytes. A request with no result may retry screening within
the caller's existing work budget. Response loss after commit returns the original envelope on replay.
No expiry, background garbage collection or orphan deletion in M1; retain even abandoned staging.
Transient sandbox scratch alone is removed by receipt/input cleanup. Capacity exhaustion refuses work.
Original execution owns request and result retention; execution completion, lease reclamation and new
workers never transfer ownership or remove records. A future cleanup contract must cover references,
crashes, backups, legal retention and owner approval before deletion is enabled.

Never call RegisterConversionOutputs for private requests, policies, reports, verdicts or private
evidence copies. Never attach them to GateReport evidence, accepted Outputs, document-revision edges,
batch manifests or export inventories. Existing acceptance can traverse arbitrary typed evidence refs;
it is not a residency firewall. M1.14 must give catalog/export composition no private-root capability
and test private-ref injection and identical-hash aliases. Public source bytes separately retained in
the ordinary catalog remain separate objects; private copies confer no publication authority.

## Canonical fixtures and review handoff

2C retains byte fixtures without final newlines and pins full hashes in `testdata/digests.json`.
Negative bytes use `.input` and a case-ID/error-class index. Fixture generation is hosted only.
Use Country=`vn`, SourceID=`fixture`, OwnerKey=`execution-` plus 32 `1` characters, all ceiling values
above, and the exact five rules. Base outbound subject bytes are `GET / HTTP/1.1\r\nHost: example.invalid\r\n\r\n`;
Budget is `{MaxItems:4096,MaxInputBytes:8388608,MaxPages:1,MaxElapsed:5000000000,MaxConcurrent:1}`.
Build refs from those exact bytes, the canonical policy and request, in dependency order. Evidence=[];
Capture=null. Base report has all zero counts, Complete=true, NO_POSITIVE_PROOF, unclassified and null
AdmissionProof. Positive variant changes only the report class/reason/proof under the pinned test oracle.
Captured base uses bytes `abc`, MediaType=`text/plain`, capture suffix of 32 `2` characters, transport
receipt bytes `{}`, RequestHash=hash(base HTTP bytes), ResponseHash=hash(`abc`), and empty Evidence.
Its Subject and transport paths are `objects/<digest>`. Neither transport fixture asserts real capture.

| Cases | Required expected result |
|---|---|
| Canonical policy; outbound/captured bases; public test report; incomplete HTML; each evidence kind and null conversion | Exact encoded bytes, field order, sizes and hashes; same decode/re-encode bytes. Converted evidence uses the existing M1.12 synthetic HTML fixture and frozen conversion closure. |
| Missing/unknown/duplicate nested keys; whitespace/newline; reordered keys; null arrays; wrong optional shape; float/exponent/-0; invalid UTF-8/surrogate; changed pattern/version | INPUT or POLICY; zero decoded value/digest. Pin `<>&` escaping and composed/decomposed Vietnamese bytes separately. |
| `000000000001`, `000000001`, `a@example.invalid`, `0900000000`, `MST: 0000000000` as independent plain views | Exactly one match for id12, id9, email, phone and tax respectively; zero other counts except tax input also matches phone once. SIGNAL_PRESENT. |
| Each signal bounded by letters, marks, numbers or underscore; newline-separated signals; duplicate raw/decoded URL views; `%40`, `%2540`, malformed `%`, `+` in query/form, JSON `\u0040` | Pin boundary rejection, separate-view counts, one-pass decoding, residual escape refusal and decoded-string detection. No cross-field match. |
| Name-only UTF-8; clean scan; DOCX with converted text; unsupported gzip/charset; HTML entity spelling | No positive admission; incomplete/unsupported cases cannot use test proof. Name-only text remains NO_POSITIVE_PROOF. |
| Every bound at limit and limit+1, shared nested rows/strings/objects/views/matches, overflow declarations, timeout/cancel in each phase | Exact-limit valid when all other constraints hold; over-limit yields fixed LIMIT/DEADLINE/CANCELLED internally and generic refusal externally. No truncation or partial public report. |
| Wrong subject/policy/source/owner/operation/budget/evidence; wrong report/manifest size/hash; forged issued key; corrupt/missing verdict; changed receipt inode; stale policy | BINDING internally, zero export calls; normalized text cannot authorize original bytes. |
| Disabled detector in a test-only control; removed/mismatched positive oracle; forged public report resolved by production constructor | Corresponding local count disappears only in the control; no proof still refuses. Production always rejects public. |
| Store races, crash at each fsync/rename/index step, response loss, corrupt committed record, reclaimed execution | Original identity/owner and exact bytes replay; no overwrite, partial issuance, deletion or catalog row. |

2D tests both Supervisor runs, four child phases, canonical bundle/manifest/verdict receipts, strict
base64, extra objects/files, no network/credentials, retained processes, close failures and exhausted
per-hop deadlines. 2C/2D test all local outward sinks with three independent controls: text; numeric
start/end offsets without text; class, class-specific reason and detector count separately without either.
Assert structured allowlists and exact serialized sinks, not substring absence alone. Controls must fail
the named sink assertion, never merely a parser/setup assertion. Test bypassed admission yields a
detectable export call. Private closure injection must fail even under an ordinary-looking path/hash alias.
Print only case ID, sink and detection status; retain no raw failure capture in artifacts or summaries.
The [parent sink matrix](quality-gate.md#verification-and-privacy-sentinels) owns all later consumers.
Independent design review examines canonical bindings, test-proof reachability, private publication
and crash ordering first. Independent adversarial review and hosted B checks gate 2C/2D; M1.14 adds
real ContainerLauncher, Temporal and per-hop evidence. This contract claims no executed test evidence.
