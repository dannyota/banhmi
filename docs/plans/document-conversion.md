# Document conversion implementation plan

> **For agentic workers:** Use the assigned implementation worker and a separate read-only reviewer. Follow [workflow](../../instructions/workflow.md) and [hosted verification](../../instructions/verification.md). Steps use checkboxes to record evidence.

**Goal:** Relocate the existing pure Go HTML and DOCX readers into their approved home while preserving legacy text and error behavior.

**Architecture:** `internal/corpus/conversion` owns format mechanics. `pkg/extract` retains public compatibility functions and the existing Vietnamese DOCX heading policy. A function parameter supplies heading detection without adding country modules or runtime selection.

**Tech stack:** Go `1.26.3`, standard ZIP/XML libraries, existing `golang.org/x/net/html`, and existing `golang.org/x/text/unicode/norm`. Add no dependencies.

**Spec:** [Document conversion](../specs/document-conversion.md), limited to the owner-approved source relocation under [repository layout](../design/repository-layout.md#path-migration-and-compatibility).

## Scope and status

This plan defines one compatibility-only source slice, not M1 acceptance or the block-model rollout.
The reader relocation and compatibility tests are merged. Independent review found no blocking
defects, and all required hosted checks pass. Real-corpus parity and milestone acceptance remain gates.
The [Vietnam plan](vietnam.md) owns milestone acceptance and later infrastructure gates.

Preserve all six jurisdictions' existing callers, recipes, and deployments. Do not switch the active
DOCX path from MuPDF to the Go reader. Add no parser improvements, block envelope, source maps,
quality enforcement, runtime switches, country modules, OCR/native adapters, SQL, generated code,
migrations, dependency changes, corpus access, or cloud operations.

No local tests, builds, formatting commands, generation, installs, or app execution are allowed.
Workers edit and inspect only. The primary owns Git, hosted evidence, and integration.

## Ownership and interfaces

One implementation worker owns the following six files for this slice. Preserve unrelated edits.

| File | Responsibility |
|---|---|
| `internal/corpus/conversion/html.go` | HTML parser, `blockElems`, `tidyLines`, and final NFC normalization |
| `internal/corpus/conversion/html_test.go` | Relocated HTML assertions and exact-output characterization |
| `internal/corpus/conversion/docx.go` | ZIP reader, private XML parser, table helpers, and injected heading detection |
| `internal/corpus/conversion/docx_test.go` | Relocated private-parser cases, neutral policy injection, and ZIP/error cases |
| `pkg/extract/htmltext.go` | Public `HTML` compatibility function only |
| `pkg/extract/docx.go` | Public `DOCX` compatibility function and unchanged `legalHeading` expression |

The worker also owns replacement compatibility tests at `pkg/extract/htmltext_test.go` and
`pkg/extract/docx_test.go`. The primary owns this plan's integration status, the spec, documentation
index, layout, and Vietnam delivery records. CI workflows and `tools/ci` belong to the separate CI lane.

New package APIs:

```go
func HTML(s string) (string, error)
func DOCX(data []byte, isHeading func(string) bool) (string, error)
```

Retain `extract.HTML(s string) (string, error)` and `extract.DOCX(data []byte) (string, error)`.
The first delegates to `conversion.HTML`; the second passes `legalHeading.MatchString` to
`conversion.DOCX`. Keep the regexp exactly `^(Điều|Khoản|Điểm|Mục|Chương|Phần)\s+[\dIVXLC]`.

The new private parser is `parseDocxBody(r io.Reader, isHeading func(string) bool) (string, error)`.
It calls a non-nil predicate after NFC normalization and `strings.TrimLeft(s, " \t")`, exactly where
the existing heading check runs. A nil predicate emits ordinary paragraph spacing. It must not alter
the emitted paragraph text. Keep helpers private; add no options framework or exported parser API.

`extract.Normalize` currently calls only `norm.NFC.String`. `conversion.HTML` calls the same library
function directly after `tidyLines`; do not import `extract` or duplicate a normalization algorithm.
Leave `pkg/extract/extract.go` unchanged. Conversion imports no country, pipeline, store, provider,
network, or legacy extraction package.

## Review focus

- Exact newlines and trailing whitespace remain observable output, especially before headings and tables.
- DOCX heading detection receives NFC text with only leading spaces/tabs removed; nil detection adds no legal policy.
- ZIP member matching still accepts backslashes and chooses the first matching body; malformed XML remains an error.
- HTML retains its current discarded elements, entity decoding, cell spaces, and blank-line collapse.
- Shared conversion preserves native Unicode text for `vn`, `my`, `sg`, `id`, `th`, and `kh`; synthetic checks do not prove real-corpus parity.

## Task 1: Characterize both public readers

- [x] Replace weak substring-only coverage with exact expected output where practical, while retaining every existing table, heading, grid-span, paragraph, discarded-element, entity, and empty-output assertion.
- [x] In legacy public-API tests, call only `extract.HTML` or `extract.DOCX`, never the new function for the expected value. Build DOCX ZIPs in memory from synthetic XML; do not track document payloads or use external files.
- [x] Assert HTML input as the interpreted Go string `"<p>A &amp; B</p><p>e\u0301</p>"` returns `"A & B\né"` with nil error. Assert discarded-only markup returns `""` with nil error. Preserve the existing full HTML sample with exact text `"Điều 1. Phạm vi điều chỉnh\nThông tư này quy định & hướng dẫn.\nKhoản Nội dung"`.
- [x] Assert a DOCX body containing paragraphs `Quy định chung` and `Điều 1. Phạm vi điều chỉnh` returns `"Quy định chung\n\nĐiều 1. Phạm vi điều chỉnh\n"`. Paragraphs `"A"` and `" \tĐie\u0302\u0300u 1"` must return `"A\n\n \tĐiều 1\n"`, proving normalized detection without trimming emitted text.
- [x] Assert public DOCX failure returns empty text and wraps `zip.ErrFormat` for invalid ZIP bytes. A ZIP without the body returns exactly `"docx: word/document.xml not found"`. Malformed body XML returns empty text, retains the `"parse docx body: "` prefix, and wraps `*xml.SyntaxError`. Preserve existing `"open word/document.xml: "` and `"decode w:t: "` error paths during the move.
No separate pre-move checkpoint was published. Its unavailable evidence is not counted as a pass;
the final hosted suite below covers the committed characterization and compatibility tests.

## Task 2: Move HTML and DOCX mechanics

- [x] Move the HTML body and private helpers to `internal/corpus/conversion/html.go`; replace the final `Normalize` call with the equivalent existing NFC library call. Retain a thin public shim at `pkg/extract/htmltext.go`.
- [x] Move DOCX ZIP/XML/table mechanics to `internal/corpus/conversion/docx.go`. Inject the predicate in the existing heading branch with a nil guard. Retain only the wrapper and existing Vietnamese regexp at `pkg/extract/docx.go`.
- [x] Relocate existing HTML tests to `internal/corpus/conversion/html_test.go`. Relocate DOCX private-parser tests to `internal/corpus/conversion/docx_test.go`, pass a named synthetic heading matcher, and use `HEADER 1` for generic heading-policy coverage. Keep Vietnamese policy assertions in the old public-API tests.
- [x] Assert the generic parser returns `"A\nHEADER 1\n"` with nil predicate and `"A\n\nHEADER 1\n"` with the named matcher. Add an NFC/leading-whitespace matcher assertion. Preserve exact Markdown row padding, escaped pipes, tabs, breaks, nested-table flattening, and trailing newlines using synthetic cases.
- [x] Exercise the new exported DOCX function with in-memory packages using both `word/document.xml` and `word\document.xml`, an absent body, invalid ZIP bytes, and malformed XML. Assert fixed outputs/errors independently of the wrapper. Retain first-match ordering; add no ZIP or XML hardening in this slice.
- [x] Add table-driven exact-output HTML and DOCX cases with country/text pairs `(vn, "Điều 1")`, `(my, "Section 1")`, `(sg, "Section 2")`, `(id, "Pasal 1")`, `(th, "มาตรา 1")`, and `(kh, "Article 1")`. Each single DOCX paragraph returns the supplied text plus `"\n"` with nil policy. Legacy DOCX retains its existing extra blank line only for the Vietnamese sample. HTML returns the supplied text without a trailing newline.
- [x] Inspect the diff for algorithm changes, error wrapping, import direction, duplicate parser bodies, stale private-helper calls, and changed callers. Keep all callers on the old public functions. Neutralize source-specific comments in the new package without changing behavior.

## Implementation and evidence checklist

- [x] Obtain independent plan review before implementing this slice. This is the repository review gate, not a new owner architecture approval.
- [x] Primary inspect the implementation diff and inventory changed imports and country effects. Obtain independent code review and fix confirmation; the reviewer edits no implementation files.
- [x] GitHub runs `go build ./...`, `go vet ./...`, `go test -short ./...`, hosted formatting checks, repository hygiene, and every required aggregate child under the [hosted check matrix](../../instructions/verification.md#hosted-check-matrix). Record the full tested SHA and run URL/attempt. Missing, skipped, cancelled, or failing required checks block merge.
- [x] Confirm both `internal/corpus/conversion` and `pkg/extract` tests execute in the hosted short suite. Inspect test results for the exact-output, policy-injection, ZIP, XML, and six-country synthetic cases. Hosted formatting must report no drift for the changed Go files.
- [x] Record static import inspection and hosted ordinary-build results. Tagged native adapters are unchanged; this slice neither replaces nor qualifies them. Do not claim a new transitive-import enforcement job exists.
- [x] Primary integrate the reciprocal spec/plan links and index/current-state records. Keep M1 acceptance, real-document conversion qualification, corpus parity, and deployed behavior pending.

Later slices own block contracts, immutable provenance, source maps, resource limits, sandboxing,
country recipe binding, protected real fixtures, persistence, and runtime cutover. None is accepted by
this relocation. The handoff reports exact paths/APIs, affected jurisdictions, review findings,
hosted evidence or missing runs, and these remaining gates.

## Hosted evidence

[PR #9](https://github.com/dannyota/banhmi/pull/9) merged as
`dfa35280fc71cd4af61d0e46f0ac4630c41b004c`.
[Run 38237357323](https://github.com/dannyota/banhmi/actions/runs/38237357323), attempt 1,
checked head `aa58f2226775d88afdb1dad6d1f5c9c1bb8689ec` using merge checkout
`92cffd69a253d1da1880aaf671959c360066b0b0`. All eight jobs and both aggregates passed.
The short suite ran both conversion and legacy extraction tests with synthetic inputs.
Independent implementation, corpus-integrity, and integration reviews passed.
[Master run 38237880056](https://github.com/dannyota/banhmi/actions/runs/38237880056), attempt 1,
passed on the landed commit. No separate pre-move checkpoint or local execution ran.
Real-document parity for all six jurisdictions and M1 acceptance remain unverified.
