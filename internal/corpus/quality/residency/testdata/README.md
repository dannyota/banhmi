# Residency fixtures

Fixtures are hand-authored from the [residency contract](../../../../../docs/specs/quality-gate-residency.md),
independently of the Go codecs and engine. `digests.json` pins each fixture's
full SHA-256, exact byte size and owning spec section. Every digest hashes the
entire exact file, with no prefix, suffix or domain separator.

The index uses `types` for **Types and canonical bytes**, `results` for
**Results and positive test dependency**, and `handoff` for **Canonical fixtures
and review handoff**. The latter section also owns the base values for every
request and report. Conversion dependencies follow the linked frozen M1.12
contract. They are retained as individual bytes, so review does not require
decoding a base64 object-array wrapper.

The ordinary test encodes synthetic values through the real codecs and evaluates
them through the real engine. It requires equality with retained bytes and pins,
then decodes every canonical request, policy and report to the same typed value
and re-encodes it. It also checks each closure object's exact bytes. Independent
test SHA-256 checks every pin and byte count. Missing files, duplicate pins,
unpinned files and wrong negative classes fail. No test writes fixture files.

Canonical JSON files have no final newline. Raw subject files use `.bytes`.
`conversion-original.bytes` contains one complete HTML paragraph without a final
newline; `subject-http.bytes` retains HTTP CRLF framing. `conversion-null.json`
preserves the valid absent edit ledger. Source-decode and identity maps are
retained in `conversion-offset-map.json`. `conversion-block-canonical.json`
pins the preimage of the canonical block ID. The conversion quality report
retains both required quarantine findings and absent metric evidence arrays.
`conversion-edit-*` pins a nonempty NFC edit and its exact removed/replacement
bytes. The HTML and DOCX closure controls also cover NFC and CRLF edits, paired
DOCX package/member anchors, aggregate substitutions, unused members, omitted
maps, omitted edits, omitted quarantine findings, and retained conversion bounds.
`transport-receipt.json` also serves as the synthetic empty recipe.

Negative inputs use `.input`; `negatives.json` records every case ID and fixed
error class. The duplicate, missing and unknown cases target nested budget
fields. `negative-newline.input` intentionally ends in LF.
`negative-utf8.input` is the sole octal-authored binary fixture: one byte FF.
All other fixtures were written as text literals. Failures print no input or
classification.

The public-report fixture uses a same-package test oracle pinned to the retained
outbound request bytes and full digest before evaluation. The production
report codec rejects it. The base transport receipt is synthetic and asserts no
real capture.

## Implementation handoff

The retained files and digest pins are authored. `sha256sum` and `wc -c` supplied
the inventory; `od -c` checked canonical fixture endings. No build, test,
formatting or generation result is claimed. Hosted checks and independent
adversarial review remain required. Fixture authoring required no production
codec or engine change and exposed no unresolved byte-contract ambiguity.

Rule A and rule B use phase 1's standard Go HTML escaping, including `\u0026`
inside the email regex. Composed `á` and decomposed `a` plus U+0301 retain
different UTF-8 bytes. The proof preimage is the exact `synthetic-proof.json`
object; report evidence hashes use the exact evidence-array files. These are
whole-object hashes, without domain prefixes.

Fixture checks and retained bytes live in this residency directory. Frozen
ports, models, phase 1 codecs, source code and conversion code are unchanged.
No required 2C type was found outside the owned paths. Child protocol, durable
storage, admission resolution and generic adapter refusal belong to 2D.

## Exported API

| API | Declaration |
|---|---|
| `ErrorKind`, `Error` | [policy.go:14](../policy.go#L14), [policy.go:17](../policy.go#L17) |
| `ResidencyLimits`, `Validate` | [policy.go:29](../policy.go#L29), [policy.go:53](../policy.go#L53) |
| `DetectorRule`, `ResidencyPolicy` | [policy.go:72](../policy.go#L72), [policy.go:78](../policy.go#L78) |
| `PolicyCodec` | [policy.go:98](../policy.go#L98) |
| `ScreenRequest`, `ScreenEvidence`, `ConversionEvidence` | [manifest.go:21](../manifest.go#L21), [manifest.go:37](../manifest.go#L37), [manifest.go:45](../manifest.go#L45) |
| `Codec`, `Encode`, `Decode` | [manifest.go:53](../manifest.go#L53), [manifest.go:63](../manifest.go#L63), [manifest.go:94](../manifest.go#L94) |
| `RequestCodec` | [manifest.go:129](../manifest.go#L129) |
| `ByteIdentity`, `DetectorCount`, `ResidencyReport` | [report.go:11](../report.go#L11), [report.go:17](../report.go#L17), [report.go:23](../report.go#L23) |
| `ReportCodec`, `ValidateReportBinding` | [report.go:43](../report.go#L43), [report.go:132](../report.go#L132) |
| `ResolvedObject`, `Engine`, `NewEngine`, `Evaluate` | [engine.go:26](../engine.go#L26), [engine.go:32](../engine.go#L32), [engine.go:42](../engine.go#L42), [engine.go:63](../engine.go#L63) |

## Contract map

The [binding contract](../../../../../docs/specs/quality-gate-residency.md) owns
requirements. These links identify authored code and tests, not passing evidence.

| Rule | Code | Tests |
|---|---|---|
| Ordered declarations; unchanged nested model values; rule A field spelling, duration integers and optional nulls | [manifest.go:21](../manifest.go#L21), [report.go:23](../report.go#L23) | [manifest_test.go:23](../manifest_test.go#L23), [report_test.go:27](../report_test.go#L27) |
| Rule B recursively sorted snake-case maps and exact five rules | [policy.go:98](../policy.go#L98) | [policy_test.go:12](../policy_test.go#L12), [policy_test.go:38](../policy_test.go#L38) |
| Canonical equality; missing, unknown, duplicate and trailing keys/values; null arrays; integer notation | [manifest.go:94](../manifest.go#L94), [manifest.go:320](../manifest.go#L320) | [manifest_test.go:47](../manifest_test.go#L47), [policy_test.go:38](../policy_test.go#L38) |
| UTF-8, NUL, paired surrogates, HTML/U+2028/U+2029 escaping; no normalization | [manifest.go:192](../manifest.go#L192), [manifest.go:397](../manifest.go#L397) | [manifest_test.go:47](../manifest_test.go#L47), [manifest_test.go:82](../manifest_test.go#L82) |
| Exact root digests/sizes, root/dependency accounting, path/hash/size consistency and self-reference refusal | [manifest.go:287](../manifest.go#L287), [manifest.go:458](../manifest.go#L458) | [manifest_test.go:140](../manifest_test.go#L140), [manifest_test.go:236](../manifest_test.go#L236), [engine_test.go:305](../engine_test.go#L305) |
| Vietnam/source/execution identity; closed operation/subject union; capture equality and key grammar | [manifest.go:136](../manifest.go#L136) | [manifest_test.go:101](../manifest_test.go#L101) |
| Exact immutable policy reference; closed mode and rule version; copied options and effective minimum limits | [policy.go:102](../policy.go#L102), [engine.go:42](../engine.go#L42), [engine.go:63](../engine.go#L63) | [policy_test.go:38](../policy_test.go#L38), [policy_test.go:145](../policy_test.go#L145), [engine_test.go:190](../engine_test.go#L190) |
| Evidence kinds, order, duplicate tuple refusal, subject hash and null/non-null conversion shape | [manifest.go:136](../manifest.go#L136) | [manifest_test.go:197](../manifest_test.go#L197), [engine_test.go:305](../engine_test.go#L305), [manifest_test.go:562](../manifest_test.go#L562) |
| Sorted deduplicated authenticated closure, smallest alias path, no unused objects, complete transport dependency | [manifest.go:458](../manifest.go#L458) | [engine_test.go:305](../engine_test.go#L305), [manifest_test.go:562](../manifest_test.go#L562) |
| Canonical typed conversion request/document/output closure; original, recipe, candidate and request bindings; output membership; raw text selection; preserved nullable arrays; no span discovery | [manifest.go:534](../manifest.go#L534), [manifest.go:606](../manifest.go#L606) | [manifest_test.go:562](../manifest_test.go#L562) |
| RE2 leftmost non-overlapping candidates, all five rules, rune and email boundaries, no sliding retry or folding | [engine.go:204](../engine.go#L204), [engine.go:249](../engine.go#L249) | [engine_test.go:89](../engine_test.go#L89), [engine_test.go:136](../engine_test.go#L136), [engine_test.go:231](../engine_test.go#L231) |
| Independent ordered views, repeated counts, no cross-field concatenation | [engine.go:63](../engine.go#L63), [engine.go:322](../engine.go#L322) | [engine_test.go:247](../engine_test.go#L247), [engine_test.go:433](../engine_test.go#L433) |
| Literal supported text/HTML, media parameters, opaque/incomplete coverage, evidence cannot upgrade coverage | [engine.go:63](../engine.go#L63), [engine.go:276](../engine.go#L276) | [engine_test.go:89](../engine_test.go#L89), [manifest_test.go:562](../manifest_test.go#L562) |
| HTTP framing, version, target form, folded/token headers, wire value order, sorted folded names, host once, body and EOF | [engine.go:322](../engine.go#L322) | [engine_test.go:247](../engine_test.go#L247) |
| One-pass path/query/form decoding, query plus handling, residual-escape refusal, supported encodings, JSON token-order strings and duplicate/root/depth checks | [engine.go:322](../engine.go#L322), [manifest.go:320](../manifest.go#L320) | [engine_test.go:247](../engine_test.go#L247), [manifest_test.go:220](../manifest_test.go#L220) |
| Unsupported encoding discards counts; malformed input or exhausted bounds returns no report | [engine.go:63](../engine.go#L63) | [engine_test.go:247](../engine_test.go#L247), [engine_test.go:305](../engine_test.go#L305), [report_test.go:141](../report_test.go#L141) |
| Request/subject/policy/source/owner/operation/rule/evidence report binding and five ordered counts | [report.go:77](../report.go#L77), [report.go:132](../report.go#L132) | [report_test.go:27](../report_test.go#L27), [report_test.go:63](../report_test.go#L63) |
| Exact class/reason/coverage/proof combinations, closed production constructor and synthetic proof digest | [engine.go:63](../engine.go#L63), [report.go:43](../report.go#L43), [report.go:122](../report.go#L122) | [engine_test.go:190](../engine_test.go#L190), [engine_test.go:231](../engine_test.go#L231), [report_test.go:63](../report_test.go#L63) |
| Shared original deadline, cancellation between work phases/views/regex calls, zero error result | [engine.go:63](../engine.go#L63), [engine.go:204](../engine.go#L204), [manifest.go:534](../manifest.go#L534) | [engine_test.go:360](../engine_test.go#L360), [engine_test.go:399](../engine_test.go#L399) |
| Payload-free private error classes, no cause chain, no logger; report has no text, locations or offsets | [policy.go:14](../policy.go#L14), [report.go:23](../report.go#L23) | [report_test.go:254](../report_test.go#L254), [engine_test.go:443](../engine_test.go#L443) |
| Exact canonical fixtures, full digest pins, negative byte extensions and case/class index | [manifest_test.go](../manifest_test.go) | `TestResidencyCanonicalFixtures`; hosted execution remains pending |

## Bounds

Every declaration ceiling has exact-limit, limit-plus-one, zero, negative and
integer-overflow controls in [policy_test.go:80](../policy_test.go#L80).
Operational pairs use a workload at its allowance and the same workload one byte
or item above a reduced allowance, or a second append after exact exhaustion.

| Bound | Ceiling | Operational limit and limit-plus-one test |
|---|---:|---|
| Policy bytes | 16384 | [policy_test.go:107](../policy_test.go#L107) |
| Request bytes | 65536 | [manifest_test.go:140](../manifest_test.go#L140) |
| Report bytes | 4096 | [report_test.go:102](../report_test.go#L102), including minimal-report failure |
| Subject bytes | 8388608 | [report_test.go:171](../report_test.go#L171), including exact subject bytes; [manifest_test.go:140](../manifest_test.go#L140) for the retained input budget |
| Distinct closure and aggregate view bytes | 67108864 | [report_test.go:171](../report_test.go#L171), separate counters; [manifest_test.go:140](../manifest_test.go#L140) for root accounting |
| Objects, including roots | 128 | [report_test.go:171](../report_test.go#L171), [manifest_test.go:140](../manifest_test.go#L140) |
| Nested items and views | 4096 | [policy_test.go:107](../policy_test.go#L107), [report_test.go:141](../report_test.go#L141), [report_test.go:171](../report_test.go#L171) |
| Evidence entries | 16 | [manifest_test.go:197](../manifest_test.go#L197) |
| Rules | 5 | [policy_test.go:107](../policy_test.go#L107) |
| Pattern bytes | 512 | [policy_test.go:107](../policy_test.go#L107), at the longest mandatory rule |
| Aggregate matches | 1024 | [report_test.go:102](../report_test.go#L102), [report_test.go:171](../report_test.go#L171) |
| Elapsed milliseconds | 5000 | [policy_test.go:80](../policy_test.go#L80); expired/cancelled operation controls in [engine_test.go:360](../engine_test.go#L360) and [engine_test.go:399](../engine_test.go#L399) |
| JSON depth | 64 | [manifest_test.go:220](../manifest_test.go#L220) |
| Path bytes | 512 | [manifest_test.go:236](../manifest_test.go#L236) |
| Aggregate decoded strings | Enclosing format byte limit | [manifest_test.go:220](../manifest_test.go#L220) |

## Proof and sensitivity boundaries

`Engine.proof` is private and nil in `NewEngine`. Assignments occur only in
`engine_test.go` and `manifest_test.go`. The oracle receives the complete canonical
request and full digest pinned before evaluation. Those bytes include subject size/hash, policy,
operation, owner, budget and evidence. The production report constructor and
public binding validator reject the public fixture independently. Test proof
cannot skip inspection, limits, cancellation, signals or incomplete coverage.

[report_test.go:254](../report_test.go#L254) tests returned error allowlists and
the existing request/result envelope serializers. Each sink has separate text,
numeric start/end, class, reason and detector-count injection controls. Envelopes
require exactly `Key` and `Manifest` and independently authored final bytes and
values. Controls also replace each existing field. Error sinks require
the fixed `ErrorKind` type, exact class and no unwrap chain. Each injected control
must fail its sink predicate. Assertions print only case and sink labels.
These serializer controls do not establish the future 2D adapter boundary.

Adversarial review should examine canonical field/byte bindings and fixture pins
first, then production proof reachability, conversion closure and alias handling,
HTTP parser normalization, aggregate bounds and cancellation. Later 2D review
must independently cover resolver proof, storage publication/crash order, child
verification, generic refusal and exclusion from public catalogs and exports.
