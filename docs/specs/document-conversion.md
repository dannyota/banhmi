Proposal. Not approved, not implemented.

# Document conversion into citable evidence

## Goal and limits

Preserve what an official document says, where it says it, and what conversion could not establish.
Produce source-linked blocks, body structure, and a quality report before any text enters a candidate corpus.

This proposal solely owns extraction from bytes to the block model for every format, including appendices and forms inside a file, plus normalization, body structure, conversion gates, quarantine, and evaluation.
[Document parts](document-parts.md) consumes the block model to identify, cite, and track Phụ lục, Biểu mẫu, Mẫu số, and attachments, and owns their display.
[Write path](corpus-write-path.md) owns orchestration and publication. [Database](corpus-database.md) owns persistence.
Relations, provision validity, subjects, chunking, and embedding remain outside this design. The write path computes amendment differences in its relations and validity stage, the database stores them, and the website design owns redline display.

Owner inputs: Vietnam first for Tra cứu quy định at `tracuuquydinh.vn`; banhmi remains the engine and corpus name.
The five other country services keep running unchanged on their existing AWS hostnames. Select conversion and grammar by country recipe; add no new country or subject now.
Use no answer model, generative conversion, generative correction, or reranker. Retrieval uses dense plus lexical fusion.
Only public official documents may reach cloud batch OCR. Conversion accepts no user questions or query logs.
Vietnam questions are Vietnamese with diacritics or a document number; the serving layer validates the question before any embedding or corpus query. English is interface text only.
Keep `guide`, `corpus_status`, `quality_gaps`, `search`, and `document` unchanged as the five MCP tools.
Assumption: immutable capture and durable revision adapters become available before enforcement. Until then, conversion produces shadow reports only.

## Current state and evidence

“Coded” below means inspected source. “Recorded validation” means a repository account of a real-document check, not a new check here.
No deployed converter version, current corpus count, or live service behavior was verified for this proposal.

| State | Evidence and consequence |
|---|---|
| Coded DOCX | The Go ZIP/XML parser emits paragraphs and Markdown tables, but flattens nested tables: [docx.go:19](../../pkg/extract/docx.go#L19), [docx.go:62](../../pkg/extract/docx.go#L62). The active pipeline instead calls MuPDF: [process_activities.go:311](../../pkg/pipeline/process_activities.go#L311). Do not call the Go parser the active path. |
| Coded HTML | Go `x/net/html` drops selected non-body elements, joins cells with spaces, and collapses whitespace: [htmltext.go:24](../../pkg/extract/htmltext.go#L24), [htmltext.go:48](../../pkg/extract/htmltext.go#L48). It returns text, not a lossless block tree. |
| Coded DOC | LibreOffice headless creates DOCX with a 120-second timeout; the pipeline then uses MuPDF: [fitz.go:143](../../pkg/extract/fitz/fitz.go#L143), [process_activities.go:321](../../pkg/pipeline/process_activities.go#L321). Output-file existence does not prove fidelity. |
| Coded PDF | MuPDF concatenates page text and serializes native calls: [fitz.go:47](../../pkg/extract/fitz/fitz.go#L47). The scan check inspects at most 12 pages; page probe errors are skipped: [process_activities.go:525](../../pkg/pipeline/process_activities.go#L525), [fitz.go:99](../../pkg/extract/fitz/fitz.go#L99). |
| Coded text gate | Replacement characters, private-use characters, mojibake, letter count, whitespace, and Vietnam diacritic density contribute to assessment: [extract.go:187](../../pkg/extract/extract.go#L187). Defaults include 50 letters, 0.02 diacritic density, and 0.6 score: [extract.go:27](../../pkg/extract/extract.go#L27). These are not transcription-accuracy measurements. |
| Coded OCR | `pkg/extract/docai` now calls Vision `DOCUMENT_TEXT_DETECTION`, `builtin/latest`, and retains only response text: [docai.go:278](../../pkg/extract/docai/docai.go#L278). Document AI is a historical path, not another active implementation: [EXTRACTION.md:99](../design/EXTRACTION.md#L99). EasyOCR has a CPU client and language/DPI settings: [easyocr.go:13](../../pkg/extract/easyocr.go#L13). OCR writes `IsBinding: false`: [ocr_all.go:290](../../pkg/pipeline/ocr_all.go#L290). |
| Coded cleanup and parsing | Cleanup discards gazette furniture before returning text: [process_activities.go:559](../../pkg/pipeline/process_activities.go#L559). Vietnamese parsing uses a line state machine, spacing repair, and quote state: [legalparse.go:27](../../pkg/pipeline/legalparse.go#L27), [legalparse.go:216](../../pkg/pipeline/legalparse.go#L216). Normalize prefers the VBPL tree: [normalize_activities.go:61](../../pkg/pipeline/normalize_activities.go#L61). |
| Recorded validation | Straight-quoted replacement text leaked false clauses: [PLAN.md:232](../../PLAN.md#L232). Footnote replacement glyphs, Cyrillic lookalikes, and residual OCR corruption were observed: [PLAN.md:389](../../PLAN.md#L389), [PLAN.md:398](../../PLAN.md#L398). The audit records 129 missing khoản already absent from source trees, plus inline subdivisions: [PLAN.md:637](../../PLAN.md#L637). |
| Coded persistence boundary | Legacy normalization deletes and recreates sections: [normalize_activities.go:420](../../pkg/pipeline/normalize_activities.go#L420). Canonical SQL adds immutable artifacts and revision recipes: [bronze/schema.sql:154](../../sql/bronze/schema.sql#L154), [silver/schema.sql:396](../../sql/silver/schema.sql#L396). Their production adapters remain a separate gate: [source plan:392](../plans/corpus-redesign-source.md#L392). |
| Coded execution and CI | The CLI composes without Temporal: [main.go:118](../../cmd/pipeline/main.go#L118). The SDK pilot is `CorpusRefresh`, not the proposed document topology: [workflow.go:22](../../internal/corpus/workflow/temporal/workflow.go#L22). A manual hosted generation lane exists: [corpus-source.yml:3](../../.github/workflows/corpus-source.yml#L3). Source presence does not prove a successful run. |

[EXTRACTION.md:18](../design/EXTRACTION.md#L18) records the DOCX, HTML, DOC, PDF preference and separate OCR batch.
[instructions/corpus.md:48](../../instructions/corpus.md#L48) requires quote-aware grammar, exact citations, and explicit source gaps.
The owner-specified `DocumentIngest` topology and single-runtime policy govern this proposal over the older alternatives in [CORPUS-PIPELINE-REDESIGN.md:133](../design/CORPUS-PIPELINE-REDESIGN.md#L133) and [CORPUS-PIPELINE-REDESIGN.md:203](../design/CORPUS-PIPELINE-REDESIGN.md#L203).

## Decisions

| Decision | Recommendation | Rejected option and reason |
|---|---|---|
| Conversion architecture | Small format adapters emitting one versioned block model. Keep existing tools as the measured baseline. | One universal Markdown converter hides layout loss and weakens source mapping. |
| Native Office content | Promote structured Go DOCX/HTML adapters only after comparison; add a bounded XLSX adapter. | Render every Office document to PDF first: loses native list and cell structure. |
| PDF | Retain MuPDF, expose structured text and page geometry in a separate process. | Replace the PDF stack immediately: no Vietnam benchmark supports the change. |
| OCR | Google Cloud Vision is the approved OCR engine. The OCR bake-off is optional later work, not a gate. | Automatic engine racing followed by the highest confidence: scores are not comparable across engines. |
| Acceptance | Independent hard gates plus scoped, reviewed gaps. | One weighted score can hide a missing page behind good text elsewhere. |
| Repair | Append reversible transforms and new processing revisions. | Silent spelling repair, generated text, or overwriting accepted evidence destroys provenance. |

Owner decision 21 selects Google Cloud Vision, which already processed almost all scanned source documents. Back up OCR outputs and original files in AWS S3, public legal text only.

## Activities inside DocumentIngest

The conversion input is one document version with a closed manifest of captured official artifacts and source observations.
Each attachment receives its own assessment even when the main body succeeds. A failed body does not discard a good attachment.
Fetch and immutable-original capture precede these activities and belong to the write path.
`DocumentIngest`, `OcrBatch`, and `Reprocess` use the database-owned intake ledger: execution records keyed by workflow ID and attempt fence, with no build ID.
Revision origin provenance is `document_revision.intake_execution_id`; any legacy `document_revision.build_id` remains nullable. `CorpusBuild` selects build-eligible revisions into its manifest.

| Activity | Input | Immutable output |
|---|---|---|
| `DetectFormat` | Original reference, SHA-256, declared bytes/media type, filename, source role, detector recipe | Hashed sniffed format/package inventory, dimensions/page count where available, mismatch findings, detector name/version/policy |
| `Convert` | Original, detected-format/inventory hash, frozen detector and converter recipes | Raw block artifact, page/region inventory, diagnostics, derivatives, OCR-needed region manifest |
| `Normalize` | Raw blocks plus normalizer recipe | NFC block artifact, reversible edit ledger, encoding evidence, exclusion candidates |
| `BuildStructure` | Normalized blocks, source-tree observation, body grammar, parts-parser contract | Body tree, quoted spans, source gaps, parts result reference, block ownership map |
| `QualityGate` | All preceding hashes, source metadata, parts findings, gate policy | Per-artifact and per-document metrics, authority, outcome, reason codes, scoped gaps |

`DocumentIngest` sends scanned pages or failed deterministic regions to shared `OcrBatch`; conversion never calls OCR inline.
An OCR result re-enters normalization, structure building, and the gate with its page manifest and non-binding authority.
Mixed PDFs retain deterministic and OCR spans separately. A provision containing any OCR span cannot claim wholly binding text.
Failing documents wait in quarantine for a review signal. `Reprocess` starts from the stored original with a new recipe.
Only revision states `accepted` and `accepted_with_gaps` are build-eligible. A gate outcome alone cannot accept a revision; fenced database acceptance records eligibility.
The write-path and workflow designs own fan-out, retries, deadlines, queues, signals, and cancellation.
Activity envelopes use `{input_manifest_ref, digest, recipe_ref, budget_ref}`; final output uses `{block_manifest_ref, structure_ref, provenance_ref, gate_report_ref, outcome_code}`. No document bodies enter Temporal history.
Only the fenced persistence adapter commits outputs and completion together. A converter has no database access.

## Intermediate document model

Call the artifact `ConvertedDocument`, with a versioned schema and canonical serialization for hashing.
Preserve both raw converter output and normalized output. A plain-text rendering is a derivative, never the interchange format.
The database links versioned, hash-bound converter output, raw to normalized offset maps, the reversible edit ledger, quality reports (metrics, thresholds, outcome, reason codes), and review decisions to the document revision.
References resolve to immutable bytes, schema version, byte size, and SHA-256; acceptance rejects missing objects, digest mismatches, or incompatible versions.
Writer-only records retain opaque reviewer keys, authorization attestations, decision expiry, fences, and rejected candidates; actor identity and the audit journal stay in Vietnam. Published evidence retains accepted blocks, source maps, edit provenance, authority, scoped quality/gap reports, decision references, and scoped reasons with hashes; omit operator identities and control records.

| Field | Contract |
|---|---|
| Envelope | Original hash, observation reference, artifact role, media type, byte size, schema version, country/language recipe, detector name/version/policy and result hash, converter name/version/image digest, settings digest, font/model hashes, output hash |
| Pages | Physical index starting at 1, printed page label separately, dimensions, rotation, render hash, region inventory, extraction status. Reflowable formats use null physical pages; rendered preview pages have their own artifact identity. |
| Blocks | Ordered `heading`, `paragraph`, `list_item`, `table`, `image`, `page_break`; revision-local block ID, parent container, order, exact raw text, normalized text reference, language, authority, source spans, warnings |
| Runs and style | Run offsets, font name and fingerprint, size, bold/italic, indentation, alignment, numbering definition, script, writing direction, and geometry when present. Style suggests boundaries; it does not prove a legal label. |
| Tables | Ordered rows/cells, zero-based coordinates, positive row/column spans, explicit header links where known, nested blocks, empty versus absent versus unreadable state, notes and units. Preserve source formulas and cached values separately. |
| Images | Original embedded-object reference or source page/region, dimensions, transform, optional OCR result reference. Never invent a caption or infer a signature's authenticity. |
| Confidence | Nullable engine score plus score kind, engine version, and scope. Separate deterministic check results from OCR confidence and review assertions. Missing confidence is unknown, not 1. |
| Findings | Machine reason, affected artifact/page/block range, severity, expected coverage, evidence references, review state. A gap has no invented text. |

Source spans name their coordinate space: original bytes, decoded package member, raw converter text, or normalized text.
All byte offsets use half-open `[start,end)` ranges and identify the hashed artifact. Text offsets count UTF-8 bytes, not characters.
Retain page rectangles in a declared coordinate system, XML member/path and token ordinal, PDF object references where available, and HTML source token offsets.
For compressed DOCX/XLSX, member offsets are not original ZIP offsets. Record the original member's compressed range when available, plus the decoded-member hash and range.
For PDF text in compressed streams, ligatures, OCR, and DOC-to-DOCX derivatives, exact original text byte mappings may be unavailable.
Set the mapping to null with `BYTE_MAPPING_UNAVAILABLE`; keep page/object/member anchors and the complete original hash. Never substitute derivative offsets.
Range bounds, parent references, source order, transform chains, and table spans must validate before persistence.
Both body and parts parsers consume the same normalized blocks; neither reparses lossy Markdown.

## Per-format conversion

Sniff magic and package contents before trusting a URL suffix. HTML returned as a DOC download is a mismatch, not a Word file.
Inventory all members, pages, text boxes, footnotes, tables, and attachments before selecting searchable content.
Every converter preserves unsupported regions as findings. A successful exit cannot erase omissions.
Prefer DOCX, official HTML, DOC, then born-digital PDF only among candidates for the same legal version that pass the gate.
Preserve all renditions. Conflicting official text requires review; format preference cannot settle a textual conflict.

| Format | Chosen path and settings | Traps and deterministic checks |
|---|---|---|
| DOCX | Extend the Go ZIP/XML adapter to emit blocks from WordprocessingML. Read numbering, styles, relationships, headers/footers, footnotes/endnotes, drawings, and table merge data. | Check member integrity, paragraph/run counts, numbering resolution, row/column merges, and every text-bearing member. Preserve tracked insertions/deletions separately; unresolved revision view is quarantined. Do not flatten nested tables or silently ignore text boxes. |
| HTML | Extend Go `x/net/html`; decode from captured bytes using evidenced charset. Apply a versioned source body selector. Keep DOM order, list attributes, tables, image references, and source token map. | Preserve the full capture. Reject navigation-only/placeholder bodies, missing selectors, malformed table geometry, and unresolved external content. Never execute scripts or fetch images/styles during conversion. |
| Legacy DOC | LibreOffice headless to DOCX in a fresh profile, then the structured DOCX adapter. Keep the original and converted package. Use `--headless --nologo --nodefault --nofirststartwizard`, an isolated `UserInstallation`, and a fixed output filter. | Detect OLE versus disguised HTML/RTF first. Fail on timeout, crash, missing/empty output, unresolved fonts, missing text regions, or invalid DOCX. Compare identity, numbering, table inventory, and rendered source/derivative pages before first acceptance of each new recipe. |
| Born-digital PDF | MuPDF structured text XML/JSON plus page geometry, fonts, image coverage, and bounded page renders. Preserve extraction order and a separately computed reading order. Disable OCR and error-ignoring modes. | Check every page, not the first 12. Compare glyph/text inventory, missing glyphs, duplicate/invisible layers, page/region coverage, column order, and table regions. Wrong ToUnicode maps or plausible wrong letters still need source comparison; fluent text cannot certify itself. |
| Scanned or mixed PDF | MuPDF renders requested pages/regions at 300 DPI to lossless images. Shared `OcrBatch` reads engine and language hints from the country recipe; Vietnam uses Google Cloud Vision with `vi`. Preserve the complete response, boxes, breaks, confidence, and request identity including hints. | A raster region covering at least 80% of page area flags a suspected scan; smaller unexplained raster regions also need assessment. Logos alone do not make a page a scan. Probe failure blocks acceptance. Check result/page bijection, dimensions, orientation, omissions, stamps, overlapping text, and line order. Embedded OCR layers remain non-binding. |
| PNG/JPEG/TIFF and other admitted images | Bounded image decoder, explicit frame inventory, lossless working image, recorded orientation/deskew transform, then `OcrBatch`. | Every TIFF frame counts as a page. Reject decompression bombs, unsupported color/frame data, cropped text, or unresolved orientation. Never claim upscaling recovered lost characters. Preserve signatures/stamps as image regions. |
| XLSX | Bounded Go Open XML adapter emits cell-text blocks for search. Read sheets, strings, styles, merged ranges, visibility, drawings, formulas, and saved values without evaluation. | Preserve leading zeros, number/date formats, units, sheet order, hidden content, and sparse cells. Never recalculate formulas, links, or macros. Missing saved results or ambiguous display formats become gaps. Parts shows a spreadsheet as a file part with metadata, search hits, and the official-source download link, without rich rendering in the first release. Extraction does not expand its application download allowlist. |
| PPT/PPTX/ODP | Metadata and source link only. No presentation text extraction in the first release. | Keep captured originals and an explicit extraction gap; do not interpret slides as an Điều tree. |
| XLS/ODS, RTF/ODT | Retain originals. Future LibreOffice-to-XLSX or DOCX routes require separate qualification; not admitted initially. | Return `UNSUPPORTED_FORMAT` and preserve the attachment. Do not turn spreadsheets into plain CSV. No verified corpus inventory proves every listed format occurs. |
| Encrypted, macro-enabled, archive, or unknown input | Preserve and quarantine. Request another official representation through review. | No password guessing, macro execution, recursive archive crawling, or generic executable fallback. A separately approved static macro-content adapter would need its own recipe and tests. |

MuPDF documents structured text output and bounded rendering in [mutool draw](https://mupdf.readthedocs.io/en/latest/tools/mutool-draw.html).
LibreOffice documents [headless conversion and isolated profiles](https://help.libreoffice.org/latest/en-US/text/shared/guide/start_parameters.html).

Freeze exact executable versions, image digests, dependency lockfiles, fonts, locale, timezone, parser settings, and model hashes before a bake-off.
The current Go dependency pins include go-fitz `v1.28.1`: [go.mod:15](../../go.mod#L15). Engine label `mupdf/1` is not a sufficient recipe identity.
Resolve actual LibreOffice/MuPDF binary builds in the hosted baseline manifest; no verified deployed binary versions are available here.
Cloud aliases such as Vision `builtin/latest` are not immutable pins. Keep the request, provider response bytes/hash, endpoint, requested model, and reported version if supplied.
Reusing a captured response is deterministic replay. A fresh cloud call is a new attempt with a new output identity, even when the requested model name is unchanged.

## Legacy encodings and reversible normalization

Recommend a separately packaged UVConverter `1.1.3b` helper for qualified TCVN3/VNI conversions, with a pinned source archive and binary digest.
Its publisher documents TCVN3, VNI, and other encodings, Linux support, and GPL licensing: [UniKey source](https://www.unikey.org/source.html).
The publisher also documents font-dependent uppercase behavior: [UniKey manual](https://www.unikey.org/support/ukmanual.html).
Treat helper packaging and license compliance as a release gate; separate process execution does not establish license compatibility.

1. Retain raw bytes, raw run text, font identity, declared charset, and original render. Inspect each run; mixed encodings need mixed treatment.
2. Select an encoding only from a qualified font/charset mapping plus a valid byte sequence. TCVN3 and VNI are different mappings; VNI typing rules are not the VNI file encoding.
3. Enumerate only allowlisted candidates: Unicode, TCVN3, VNI, evidenced Windows-1258, and separately qualified legacy mappings. Compare deterministic decode/encode round trips and diagnostic glyph cases.
4. Require one evidenced mapping with zero unmapped symbols in accepted text. A round trip alone cannot identify the correct encoding. Check retained source glyphs or an independent official transcription for ambiguous runs.
5. Store chosen mapping/version, alternatives, round-trip result, before/after bytes, and reviewer evidence when needed. Ambiguous mapping, unsupported fonts, or irreversible upstream character loss yields `ENCODING_AMBIGUOUS` or `TEXT_CORRUPT`.
6. Apply Unicode NFC only after decoding. Do not use compatibility normalization, accent folding, translation, or dictionary completion. [Unicode normalization](https://www.unicode.org/reports/tr15/) defines NFC; NFC does not repair a wrong encoding.

No TCVN3/VNI recovery is claimed validated yet. Add real mixed-font and uppercase fixtures before enabling those mappings.
Do not reconstruct lost PDF character codes from plausibility. Prefer another official text or non-binding OCR when source glyph mapping cannot be proved.

| Normalization | Rule and evidence retained |
|---|---|
| Unicode/diacritics | Validate UTF-8 and Vietnamese combining-mark sequences; compose to NFC. Flag unexpected replacement/private-use characters. Never strip a suspicious glyph merely to pass a gate. |
| Whitespace | Preserve block, cell, list, and page boundaries. Normalize CRLF and matching-space variants in a derived view. Log each changed range. Do not collapse form blanks or indentation that determines hierarchy. |
| Hyphenation | Remove a soft hyphen only with an evidenced line wrap. Join hard-hyphen line breaks only under a reviewed rule with source geometry; otherwise retain them. Never rewrite document-number hyphens or numeric signs. |
| Header/footer furniture | Mark as excluded only from native header/footer structure or a qualified repeated margin pattern with page geometry. Require repetition on at least three pages for a new geometric candidate; otherwise review. Retain excluded blocks and the rule. |
| Footnotes, stamps, signatures | Preserve their region, text, and links. Repeated form headers and signature fields remain content. Parts parser decides instrument exclusions; legal notes never disappear as page furniture. |
| Spacing/mojibake repairs | Existing repair patterns become explicit transforms with source evidence and regression cases. Do not reuse query-time accent restoration to repair evidence. |

The edit ledger records operation, rule/version, input/output artifact hashes, input/output ranges, removed bytes, replacements, and source anchors.
Store many-to-many mappings for composed characters, joins, and splits. Reversing the ledger must reproduce raw converter text byte-for-byte.
The immutable original separately recovers the exact downloaded file. Neither reversal nor text quality proves that extraction saw every original glyph.

## Body structure building

Use a deterministic grammar over block boundaries, labels, numbering definitions, indentation, and quote state.
Recognize Phần, Chương, Mục, Điều with inserted letter suffixes, numbered Khoản, and lettered Điểm including `đ`.
Require a valid enclosing context; a table row, preamble list, or phrase “Điều kiện” cannot open an Điều.
Preserve the printed label and raw citation bytes separately from a normalized matching key. Never renumber missing subdivisions.
Match spaced document numbers such as `116/ 2025/QH15` in the derived matching view; preserve original bytes and reject bare dates as document identifiers.

Maintain directional curly-quote depth, straight-quote state, and amendment replacement scope after `như sau:` across blocks and page breaks.
Retain quoted replacement headings as quoted content under the acting provision. They do not become top-level provisions.
Keep exact spans for operators, recital history after `theo`, and replacement text so the relations stage can apply its own grammar.
Unbalanced or ambiguous quote termination yields `QUOTE_BOUNDARY`; do not guess a reset that swallows the next genuine provision.
Preserve older outline-only and narrative instruments under their printed structure. Do not fabricate Điều labels for them.

Reconcile the source tree against the converted blocks, not against ordinal arithmetic alone.
Keep source tree nodes as observations. Missing source subdivisions remain `SOURCE_STRUCTURE_GAP` findings with their original provenance.
A separate derived subdivision requires an explicit label in captured text and an exact span; record a new recipe revision and comparison.
If text and source tree conflict, preserve both and quarantine the disputed mapping. Do not splice bodies from different legal versions.
When text lacks a subdivision, retain the enclosing text and gap; never infer the missing clause's words or citation.

The parts parser returns classified block ranges and namespace boundaries before the body builder assigns final ownership.
Each retained block belongs to one body/part/cell container or an explicit unresolved region. References can point across containers.
The body builder may parse an issued text's Điều tree within a namespace supplied by the parts parser.
Only the parts parser recognizes appendix/form labels, resolves duplicate form codes, and decides where those parts start and end. Conversion assigns final block ownership from those ranges.

## Quality gate and authority

Produce one machine-readable report per artifact and a document roll-up. Preserve numerator, denominator, excluded ranges, policy version, and evidence for every metric.
These thresholds are proposed launch targets, not measured accuracy. Calibrate on the fixed evaluation set without deleting hard cases or quietly lowering thresholds.
Measure all pages and regions. Ratios never excuse one missing legal word, citation, table cell, or unaccounted page.

| Metric | Definition and proposed threshold | Failure handling |
|---|---|---|
| `page_accounting` | Processed or explicitly classified pages / independently enumerated pages = 1. Require exactly one terminal outcome per expected OCR page, region, or tile item, keyed by original hash, page index, and region/tile ID (null for a whole page). A page is complete only when all expected regions or tiles are accounted for. | Missing/duplicate page, region, or tile outcome or unknown count quarantines the artifact. |
| `text_coverage` | Text-bearing regions represented by text / inventoried text-bearing regions = 1 for full acceptance. Inventory native text objects separately from output; image-only and unexplained regions remain candidates, not blank pages. Record per-page characters and nonblank-page counts. | Any unexplained region blocks full acceptance. Explicitly bounded omissions can become reviewed gaps. A render/geometry check and sampling address blind spots shared by inventory and converter. |
| `diacritic_validity_rate` | Orthographically valid Vietnamese letter/mark clusters / eligible Vietnamese clusters ≥ 0.995 overall; retained searchable spans require zero unresolved malformed clusters. Record per-block minima. | Quarantine ambiguous decoding. A valid cluster is not proof of the right word. No eligible clusters means not applicable with a reason, never a score of 1. |
| `corruption_rate` | Unexplained replacement, control, private-use, or qualified mojibake symbols / text code points = 0 in accepted searchable spans. Vietnam prose with ≥ 200 letters and accent density < 0.02 requires review. | Isolate corrupt ranges or quarantine; do not delete symbols to reach zero. Legitimate foreign names/formulas need classified spans. |
| `structure_completeness` | Explicit source structural labels mapped exactly once / independently inventoried labels = 1; no orphan children, duplicate ownership, unresolved quote scope, or unexplained ordinal gap. | Record genuine source gaps separately. A narrative document has a qualified narrative profile, not a fabricated tree. |
| `citation_parse_rate` | Exact local provision labels and parseable native citation mentions / inventoried candidates = 1 for labels, ≥ 0.99 for mentions. Every emitted citation resolves to its own span. | Unparsed mentions remain text with findings; critical ambiguous labels quarantine the affected structure. This metric does not resolve amendment targets. |
| `table_integrity` | Accounted source cells / inventoried source cells = 1; zero overlaps, invalid spans, lost units/notes, or blank-to-missing substitutions. | Unknown scan cell inventory is unknown quality, not a pass. Keep region/image plus non-binding text and a table gap until reviewed. |
| `metadata_agreement` | Exact normalized số ký hiệu and ngày ban hành agree with independently captured metadata when present; document type and issuer have no unresolved conflict. | Conflict quarantines association. Missing fields require review, not invented values. Compare issue date, not effective date; metadata does not establish validity. |
| `provenance_completeness` | Every retained block has original hash, recipe, authority, and usable source anchor; every edit reverses exactly. Threshold = 1. | Broken provenance quarantines. A declared unavailable byte mapping may coexist with a valid page/member anchor. |

Page count is meaningful for PDF/images. DOCX/HTML use native element inventories; XLSX uses sheet/cell inventories.
Apply metadata agreement to the instrument. An attachment without its own number/date uses the parts parser's verified association, not fabricated header fields.
Synthetic preview page counts never masquerade as source page counts. Empty forms use the parts gate and retain their blank fields.
Confidence is diagnostic only. OCR scores cannot satisfy missing provenance, missing pages, or identity checks.

| Outcome | Admission and visibility |
|---|---|
| `accepted` | All applicable checks pass, all required regions are accounted for, and source/authority checks permit the selected evidence. No unresolved conversion gap. |
| `accepted_with_gaps` | A reviewed decision identifies bounded excluded or unresolved regions. Only independently sound spans enter retrieval. Keep original attachments, gap locations, and expected coverage in the release. A wholly missing body yields a metadata-only gap record, not an empty-text success. |
| `quarantined` | Identity, provenance, security, encoding, quote scope, or unbounded completeness failure prevents safe admission. Candidate stays unavailable; retain prior accepted evidence through the release policy. |

Binding is an evidence-authority flag, not a declaration of legal force. Deterministic text needs confirmed official association, a passing gate, and no OCR origin.
Scans and embedded OCR layers remain non-binding even after human review. For mixed evidence, propagate authority per span and the weakest authority to combined excerpts.
Passing OCR can be `accepted` as a transcription while carrying a permanent binding-text gap; conversion completeness and legal authority are separate axes.
Never return a truncated provision as complete. If an omitted condition or exception could change the retained text's scope, exclude the whole affected provision from retrieval and retain its source locator and gap.
Do not label an official-hosted draft, unrelated attachment, or supplement as binding merely because conversion succeeds.
Known validity and relation defects remain owned by the corpus redesign: [WEBSITE.md:206](../design/WEBSITE.md#L206).

## Quarantine and review

| Reason family | Codes and default response |
|---|---|
| Input | `SOURCE_PLACEHOLDER`, `FORMAT_MISMATCH`, `UNSUPPORTED_FORMAT`, `ENCRYPTED_INPUT`: request an official alternative or record a coverage gap. |
| Execution/security | `CONVERTER_CRASH`, `RESOURCE_LIMIT`, `UNSAFE_PACKAGE`: keep diagnostics and retry only a bounded approved recipe; unsafe content cannot be waived into execution. |
| Fidelity | `TEXT_CORRUPT`, `ENCODING_AMBIGUOUS`, `PAGE_MISSING`, `READING_ORDER`, `TABLE_LOSS`: compare source regions and another qualified converter. |
| Structure/identity | `QUOTE_BOUNDARY`, `SOURCE_STRUCTURE_GAP`, `LABEL_AMBIGUOUS`, `METADATA_CONFLICT`: preserve competing observations; require exact evidence for resolution. |
| Provenance | `HASH_MISMATCH`, `ANCHOR_INVALID`, `BYTE_MAPPING_UNAVAILABLE`: hash/anchor failures block; unavailable exact byte mapping is advisory when another usable anchor exists. |

The [review API comes first](corpus-review.md#decisions), behind mutual TLS with a private CA; the owner reviews through AI agents. The human console is deferred.
Return the official source link, original, inert preview, old/candidate blocks, tree, changed spans, metrics, failed ranges, authority, and recipes. Keep the decision contract, opaque reviewer key, and audit journal.
Allowed actions: retry with another converter; accept as non-binding OCR; accept with named gaps (state `accepted_with_gaps`); mark as a coverage gap and exclude; attach an alternate official source.
Acceptance actions rerun mandatory gates against the hashed candidate and exact retained/excluded ranges. Retry starts a new attempt; coverage-gap exclusion accepts no text. Technical review cannot waive these checks.
Alternate sources require document number, issue date, issuer/type, and version association evidence. They create new observations and hashes; they never replace the captured original.
A reviewer cannot mark OCR binding, waive a hash mismatch, invent a missing clause, or approve corrupted text by raising its confidence.

The shared append-only decision contains decision ID, document version ID, quality report hash, workflow fence token, actor, action, and reason. `workflow_fence_token` binds the execution's `attempt_fence`.
Also bind country, candidate digest, expected decision epoch, expiry, original/output hashes, exact scope, evidence references, replacement recipe/source reference, and owner authorization. Actor identity stays in the Vietnam operator audit store; the writer receives an opaque reviewer key, outside Temporal.
Authority is the owner or reviewers the owner designates. [Write-path owner question 3](corpus-write-path.md#risks-and-owner-questions), “Who may accept source gaps and promote a release?”, recommends owner-only decisions initially; technical adjudication grants no gap-approval authority.
Persist before sending `quarantine.decision.v1 {decision_ref, expected_epoch, expected_report_hash}`. The signal contains no other fields.
`ApplyDecision` checks country, version, candidate digest, report hash, expected epoch, workflow fence token, expiry, and authorization under the current lease. `ApplyEvidence` atomically records acceptance and its receipt; a decision receipt or signal alone is not evidence acceptance.
Duplicate signals return the existing receipt. Wrong epochs, stale fences, expired approvals, and changed inputs cannot accept a revision. Timeout and decision compete through the expected ledger epoch; only one commits.
Workflows own durable delivery/reconciliation after commit-before-signal crashes and the expiry timer under [workflow owner question 1](corpus-workflows.md#risks-and-owner-questions).
Publish scoped gaps through `quality_gaps`, document provenance, search-result warnings, and corpus coverage counts. Excluded content must not remain searchable by accident.
Quarantine does not silently withdraw an old release; if the defect affects published evidence, escalate to the release owner for an explicit withdrawal or replacement decision.

## Versioned reprocessing

Compute conversion identity from original SHA-256, detector name/version/policy and result hash, converter name/version/image, settings, fonts/models, and block-schema version.
Compute normalized-output identity from conversion-output hash plus normalizer version and mapping-table digest.
Structure and gate identities also include their input hashes, grammar, parts parser, source-tree/metadata observations, and policy versions.
For multi-artifact documents, hash the ordered input manifest. Distinguish processing revision from document version and legal effective time.
Never reuse a hash-only OCR text cache as proof of compatibility with a new recipe.

Select reprocessing by old detector/converter/normalizer recipe digest, format, reason code, and explicit immutable document set. `Reprocess` reads stored originals, not today's URL content.
Write new candidate outputs beside accepted ones. A reprocessed revision is not build-eligible until `ApplyComparison` records a passing before/after comparison and, when critical text changes, the owner adjudicates.
Compare detected format/inventory, text edits, labels, provision coverage, tables, authority, gaps, metadata, anchors, and changed downstream input hashes.
Require exact synthetic goldens, the fixed real hard-document set, and no unexplained lost content or citations before accepting the recipe.
Every changed legal number, date, negation, citation, authority flag, or part boundary needs explicit owner adjudication supported by technical review; aggregate character accuracy cannot waive it.
Fenced `ApplyEvidence` validates comparison and required adjudication references atomically with acceptance. Unchanged output may reuse blobs; changed output needs new revision references and a measured downstream rebuild through `CorpusBuild`.
Only release promotion makes accepted revisions searchable. Never call the legacy destructive Normalize path against accepted evidence for this migration.
Rollback selects the previous complete release; retain originals, maps, edit ledgers, outputs, reports, comparisons, recipes, and decision references with every referencing release, pin, hold, or live intake attempt.

## Converter isolation

Official files are untrusted input. Malformed fonts, PDF streams, Office macros, XML entities, ZIP paths, linked objects, and oversized images can exploit parsers or exhaust resources.
Every activity that reads untrusted bytes runs on `corpus-converter-v2`, the converter sandbox queue: LibreOffice, MuPDF including text serialization, DOCX, HTML and XLSX parsers, image decoding, OCR request building, content normalization, converter-output parsing, local OCR, and bake-off challengers.
Use disposable non-root containers on isolated writer workers. Structure building over the validated block model, which reads no raw bytes, may run on plain CPU workers on `corpus-parser-v2`. Only the supervisor and persistence adapter have control-plane access.
Use a read-only root and input mount, one private bounded scratch/output directory per attempt, dropped capabilities, no privilege escalation, and a restrictive syscall profile.
Deny network access and host metadata access. Never mount credentials, host sockets, shared writable caches, or database connections into converters.
Disable macros, Java, external links, document refresh, and formula recalculation where applicable. Network denial remains mandatory even when application settings claim to disable links.
Preflight input ZIP paths, symlinks, member counts, expansion, XML depth, and image pixels; enforce those limits while streaming because headers can lie.
Check output types, paths, sizes, and manifests before reading outputs. Apply the same bounded parsers to converter outputs as to originals.
Use a fresh LibreOffice profile per attempt. Kill the complete process group/container at timeout; do not rely only on killing the parent process.

Proposed positive defaults, to be measured on hosted workers: 128 MiB original; 512 MiB expanded package; 10,000 members; XML depth 128; 500 pages/frames; 40 megapixels per rendered page.
Give one native conversion 2 vCPUs, 4 GiB RAM, 2 GiB scratch, 120 seconds for Office or 300 seconds for PDF, and 256 MiB structured output.
Render incrementally with at most two page images resident. Bound total render bytes to 1 GiB and local OCR to 120 seconds/page and 15 minutes/document.
The OCR recipe and handoff carry provider-specific encoded-image and serialized-request byte limits. [Vision limits](https://docs.cloud.google.com/vision/quotas) are 20 MB per image file and 10 MB per JSON request, including base64 overhead.
Use conservative recipe caps of 20,000,000 encoded-image bytes and 10,000,000 total serialized-request bytes for Vision. Measure actual bytes after encoding; pixel and aggregate-render caps do not replace these limits.
An oversized page yields `RESOURCE_LIMIT` unless a qualified tiling recipe preserves original page/region anchors, tile transforms, overlap deduplication, and complete coverage. No silent resizing or truncation to fit.
Exceeding any bound creates `RESOURCE_LIMIT`; do not truncate output and call it complete. A larger approved worker profile creates a new recipe.
These are batch-worker limits, not serving-host allocations or measured throughput promises.

Separate the OCR API client from sandboxed image decoding and request building. It sends validated, hash-bound request bytes with a provider-scoped runtime identity, approved endpoints, and bounded response storage; it cannot parse images or fetch embedded URLs.
`OcrBatch` owns batching, retries, and provider quotas. Transport batches may contain multiple items. Sandbox request construction enforces the total serialized size for each selected batch before submission.
The supervisor validates and hashes outputs before a trusted adapter persists them. Logs contain IDs/reasons/counts, not full documents or secrets.
Container isolation limits damage but does not prove a parser safe; patched pinned images and hosted sandbox escape/limit checks remain release requirements.

## Converter evaluation and fixtures

No tool change is justified by advertised language support alone. Compare transcription and structure on the same captured originals.
No OCR or document conversion runs on the serving host. OCR challengers below are optional later work; qualifying Google Cloud Vision outputs still requires the quality and provenance gates.

| Candidate | First-party evidence and license | Evaluation decision |
|---|---|---|
| Current Go adapters, LibreOffice, MuPDF | [LibreOffice MPL-2.0 and bundled terms](https://www.libreoffice.org/licenses/); [MuPDF AGPL/commercial](https://mupdf.readthedocs.io/en/latest/license.html). Repository license: [LICENSE:1](../../LICENSE#L1). Dependencies need a separate manifest. | Baseline. Prefer native structure from Go; keep LibreOffice as the legacy bridge and MuPDF for PDF. Confirm licenses before distributing worker images. |
| Docling standard pipeline | [Project documentation](https://github.com/docling-project/docling) lists document/table/layout support, local execution, and MIT code; model licenses are separate. | Challenger for PDF reading order and tables. Run CPU first, GPU only on batch infrastructure. Disable VLM, generative enrichment, remote services, and automatic model downloads. Do not replace the canonical block contract with vendor output. |
| Tesseract with `vie` | [Engine documentation](https://tesseract-ocr.github.io/tessdoc/) gives Apache-2.0; [language data](https://tesseract-ocr.github.io/tessdoc/Data-Files-in-different-versions.html) includes Vietnamese. Pin and check traineddata licenses/hashes separately. | Optional later open OCR challenger. CPU, 300 DPI, `vie` with `eng` only for documented mixed text; LSTM engine, compare automatic page segmentation with a declared single-column profile. Retain TSV/hOCR boxes. |
| PaddleOCR conventional OCR | [Project license](https://github.com/PaddlePaddle/PaddleOCR) is Apache-2.0; [OCR documentation](https://www.paddleocr.ai/main/en/version3.x/pipeline_usage/OCR.html) lists `vi` and CPU/GPU configuration. | Optional later challenger. Pin one non-generative detection/recognition model and character dictionary; measure CPU and batch GPU. Exclude PaddleOCR-VL and chat extraction. Model/runtime packaging and weight terms need explicit review. |
| EasyOCR | [Publisher](https://github.com/JaidedAI/EasyOCR) documents Apache-2.0, language models, and CPU operation. | Optional later challenger; freeze weights and box-to-line ordering. No automatic download during conversion. |
| Google Cloud Vision / Document AI OCR | [Vision OCR](https://docs.cloud.google.com/vision/docs/ocr) exposes document text structure. [Document AI processor list](https://docs.cloud.google.com/document-ai/docs/processors-list) lists Vietnamese and versioned Enterprise Document OCR. Both are proprietary cloud services. | Use Google Cloud Vision. Optionally compare captured responses with one explicitly versioned Document AI OCR processor later. Disable generative/custom extractors. Measure request cost, quotas, and full-response fidelity; region/version availability remains a hosted check. |

Freeze 80 real documents in eight primary strata of ten: native DOCX, HTML, legacy DOC/fonts, born-digital PDF, multi-column/table PDF, scanned/mixed PDF, images, and spreadsheet/other Office.
Add hard-case tags across strata: footnotes, stamps, split diacritics, repeated furniture, straight/curly quotes, missing subdivisions, and malformed files.
Use 40 calibration documents and 40 held-out documents, split by instrument family so paired formats do not leak across sets.
Treat this as a pilot sample, not a statistical proof of corpus-wide accuracy. Missing real strata block qualification of that format.

Seed candidates below have unverified attachment bytes and SHA-256 values; page verification limits are explicit.
Do not claim the reported repository defect still exists in a newly fetched file; match the original hash before attributing a regression.

| Candidate | Official source | Purpose |
|---|---|---|
| 09/2020/TT-NHNN | [Công Báo listing](https://congbao.chinhphu.vn/detail/tai-ve?id=32321&slug=09-2020-tt-nhnn) | Link redirects to a listing, not artifact bytes. Candidate for gazette furniture, body/appendix boundary, and form blanks. |
| 50/2024/TT-NHNN | [VBPL original listing](https://vbpl.vn/nganhangnhanuoc/Pages/vbpq-van-ban-goc.aspx?ItemID=171687) | Page contents and claimed DOCX/PDF pairing unverified pending protected capture; independent review reported HTTP 403. Candidate for amendment-clause spans. |
| 08/2024/TT-NHNN | [Công Báo DOC/PDF listing](https://congbao.chinhphu.vn/van-ban/thong-tu-so-08-2024-tt-nhnn-42292/50909.htm) | Legacy conversion, long body, form/table handoff. |
| 13/VBHN-NHNN, issued 2020 | [Công Báo DOC/PDF listing](https://congbao.chinhphu.vn/van-ban/van-ban-hop-nhat-so-13-vbhn-nhnn-32599.htm) | Consolidation notes, table cells, reported residual OCR corruption. |
| 329/2025/NĐ-CP | [VBPL text listing](https://vbpl.vn/nganhangnhanuoc/Pages/vbpq-toanvan.aspx?ItemID=185119) | Page contents and claimed lookalike glyphs/nested labels unverified pending protected capture; independent review reported HTTP 403. |
| 143/2025/QH15 | [Công Báo DOCX/PDF listing](https://congbao.chinhphu.vn/van-ban/luat-so-143-2025-qh15-468703/61729.htm) | Quoted replacement text versus recital spans. |
| 02/2023/TT-NHNN | [VBPL attachment listing](https://vbpl.vn/nganhangnhanuoc/Pages/ivbpq-lichsu.aspx?ItemID=160118&do=word) | Lists two XLSX forms beside DOCX/PDF; qualifies spreadsheet capture without assuming cell fidelity. |

The fixture manifest must contain stable official URL, captured attachment URL, capture time, media type, byte size, SHA-256, country, document version, stratum, and expected findings.
An absent hash means “unverified candidate”, never a runnable fixture. Hosted capture computes hashes from bytes and preserves changed URLs as separate observations.
Keep real originals, derived text, and OCR responses in an access-controlled fixture artifact store outside Git; [security instructions:7](../../instructions/security.md#L7) forbid tracked real document payloads.
Track manifests, metric summaries, synthetic minimal cases, and golden artifact hashes. Goldens include text, blocks, cell topology, source spans, exclusions, quotes, citations, and authority.
The versioned critical-token policy covers document/provision identifiers, every numeric value with its unit/currency, dates, negation/modality terms, citation labels, and authority markers.
Goldens annotate exact source and expected-text spans for each category. Report exact-match failures by category; reviewers cannot redefine the categories during scoring.
Two reviewers independently annotate real goldens; adjudicate disagreements against rendered originals and recorded official alternatives. Model output cannot author the gold truth.

Report per-format and worst-document character error rate, exact numeric/date/negation matches, label precision/recall, reading-order inversions, cell exactness, gap detection, false acceptance, runtime, peak RAM, and cost/page.
Proposed deterministic-text floors: character error rate ≤ 0.1% per document; zero wrong critical tokens; 100% expected citations, ownership, and declared table topology on goldens.
For OCR, target ≤ 1% character error rate per accepted transcription and zero unflagged critical-token errors on goldens; failure narrows accepted spans or quarantines, never changes authority.
Require zero false binding labels, invented labels, lost source gaps, or missed pages in the hard set. Measure false quarantines and reviewer minutes without relaxing correctness floors.
Choose a replacement only if it passes every protected case, improves its declared failing stratum, and has measured resource/cost results. Keep the current tool otherwise.

## Interfaces

| Sibling | Required contract |
|---|---|
| [Write path](corpus-write-path.md) | Immutable originals and closed artifact manifests; fenced result persistence; `DocumentIngest`, shared `OcrBatch`, `Reprocess`, and `CorpusBuild` handoffs; quarantined-wait/review delivery; release acceptance of explicit gaps. |
| [Workflows](corpus-workflows.md) | Own workflow topology and signal protocol. Accept versioned activity contracts and immutable report/decision references; make no converter-specific workflow branch without the frozen recipe. |
| [Database](corpus-database.md) | Own intake ledger, revision acceptance state and eligibility, versioned hash-bound conversion provenance, durable decisions, and fenced acceptance. Provide first-class revisioned provisions and document parts with one validity target type, ordered blocks, table rows/cells/spans, and part-to-artifact byte spans. Preserve nullable exact-byte mappings; separate writer-only audit records from published evidence. |
| [Document parts](document-parts.md) | Consume conversion's versioned normalized `ConvertedDocument` blocks; return classified block ranges, namespace boundaries, required-artifact inventory, exclusions, reference spans, and part-gate findings. Own classification, parsing of part boundaries, citations, tracking, and display; never extract from raw bytes. Depend on the qualified conversion adapter for each format. XLSX emits cell-text blocks for search; spreadsheets remain file parts without rich rendering. Presentations: metadata and source link only. |
| [Embedding](embedding-models.md) | Consume only accepted span text with authority, gaps, citations, and recipe hashes. Preserve part boundaries and downstream input diffs; conversion makes no model, chunk-size, or fusion choice. |
| [Hosting](vngcloud-hosting.md) | Receive verified releases and published originals/previews/quality reports for Vietnam-local serving, without writer-only audits or a runtime conversion/OCR dependency. Provide bounded authenticated artifact delivery. After any restore, reconcile the active release with the writer's ordered activation records before accepting traffic; never reactivate stale release control state. |

Corpus generation runs on AWS as the single writer per country dataset, orchestrated by Temporal Cloud; serving databases on VNGCloud and on AWS receive verified releases.
The [write-path decision](corpus-write-path.md#decisions) approves Temporal Cloud Developer, pay as you go with no minimum, and the isolated AWS writer. Bulk document embedding runs on Hugging Face Jobs, pay as you go, with Runpod Flex as fallback; Kaggle and GitHub Actions free tiers serve evaluation and bake-offs only.
Online retention is the active release plus at least two prior releases, kept for at least 30 days, plus any release under an explicit pin or hold.
Only one embedding model runs on the serving host at a time, under the [embedding runtime policy](embedding-models.md#migration-and-rollout-slices).
Before replacing the runtime, re-embed every release that stays in dense search, or retire older releases from dense search.
Retired releases remain available through `document` and lexical search, with a visible gap. Retention, pins, and holds still protect their evidence and recipe artifacts; conversion artifacts follow those references.
Dense search requires a matching query runtime (same model, tokenizer, recipe and dimensions). No dual-runtime capacity is reserved.
Use one `s2-general-4x8` (4 vCPU, 8 GiB) and resize only from measurement, under the [hosting decision](vngcloud-hosting.md#workload-and-sizing).
Size the dedicated vServer from measurement for PostgreSQL, one embedder, and the Go server at 2 requests per second, bursts of 8, with concurrent PostgreSQL and application load. Grow only from measurement.
Hosting defines configurable positive maximum artifact bytes, concurrent download streams per client and overall, and stream timeout.
Serving checks declared size before streaming and enforces actual bytes while streaming. Converter admission limits do not replace download limits.

## Rollout and GitHub-hosted proof

Each slice needs an independent review and a GitHub-hosted result tied to a commit. No build, test, converter, database, or cloud operation ran for this proposal.
Synthetic pull-request jobs use no credentials, pinned actions/images, read-only repository access, disposable storage, and an always-reported aggregate result.
Protected real-fixture jobs fetch only immutable allowlisted hashes, isolate converter network access, and publish restricted reports. Never expose fixture credentials to untrusted pull-request code.
Optional later OCR comparisons run on protected batch infrastructure; GitHub verifies imported response manifests and reports. Mock responses prove adapters, not provider fidelity.
Missing fixture artifacts, hash mismatch, unavailable cloud runs, and skipped required jobs remain failures or explicit incomplete gates, not passes.

| Slice | Change | Hosted exit evidence |
|---|---|---|
| 1 | Wrap existing converters with the block envelope and shadow quality report. Add no new serving behavior. | Exact legacy text parity; explicit unknown geometry/cells; complete page/error accounting; immutable report hashes. Unknown fidelity cannot pass the new gate. |
| 2 | Native DOCX/HTML/XLSX blocks, normalization ledger, and sandboxing for every adapter. Parts grammar consumes this contract after adapter qualification. | Synthetic tables/cell-text, offsets, reversible edits, ZIP bombs, no-egress checks, process-group timeouts, encoded-image/JSON byte caps including base64, and tiled page accounting with missing and duplicate tile outcomes and multi-item transport batches. Capture and hash real fixtures; XLSX stays in shadow until qualified, without adding rich rendering. |
| 3 | Body grammar and parts handshake in shadow; database provenance and decision adapters before enforcement. | Quote boundaries, missing subdivisions, mixed OCR, metadata conflicts, short forms, block ownership, and captured OCR replay. Hosted round-trip of blocks, offset maps, ledgers, reports, and decisions; reject bad hashes/versions and prove audit records do not enter published evidence. |
| 4 | Protected converter qualification and real gold adjudication; OCR bake-off is optional later work. | Per-stratum comparisons, resource/cost reports, version manifests, two-reviewer goldens, and zero protected-case regressions. No default tool switch without evidence. |
| 5 | Append-only review/reprocessing integration and then quality enforcement in an isolated dataset; depend on database intake/provenance/acceptance adapters. | Hosted sqlc/migration generation and disposable-database checks: intake without build IDs, candidate exclusion, before/after plus owner adjudication, duplicate signals, wrong epochs, stale fences, expired approvals, timeout/decision races, commit-before-signal and acceptance-before-ack crashes. Reconcile receipts without duplicate acceptance. |
| 6 | Vietnam candidate release through the write path. | Fixed-corpus retrieval evaluation, citations/parts/gaps parity, measured conversion delta, original-download integrity, and rollback rehearsal. Read and lexically search an old named release retired from dense search, or search its re-embedded replacement with the single matching runtime; restored stale control state cannot serve before writer-activation reconciliation. |

The current hosted generation lane is the starting point, not conversion proof: [corpus-source.yml:69](../../.github/workflows/corpus-source.yml#L69).
The integrating owner updates the documentation index and current-state records. Other countries receive no corpus rebuild, recipe switch, or deployment in these slices.

## Risks and owner questions

Plausible wrong words can pass syntax checks. Golden comparisons, critical-token review, independent source renditions, and ongoing audited samples reduce that risk but do not eliminate it.
Font loss in DOC/PDF can be irreversible; explicit gaps are preferable to guessed Vietnamese. Table and quote ambiguity can cause more damage than a low overall error rate suggests.
Cloud model drift prevents exact re-execution without saved responses. Strict gates also increase quarantine workload; report backlog and reviewer time before expanding formats.
Exact deployed tool versions, real fixture hashes, Vietnamese accuracy, worker throughput, cloud cost/quotas, and license compatibility remain unverified.

1. Approve the proposed hard gates and an 80-document, two-reviewer qualification set? Recommend yes; qualify formats separately and retain gaps where a stratum lacks evidence.
2. Approve the pinned UVConverter helper and continued MuPDF/LibreOffice packaging review? Recommend yes before distributing worker images; do not assume process isolation resolves license obligations.
3. Require a second technical reviewer before owner adjudication of critical changes? Recommend yes for changed legal numbers, dates, negations, citations, authority, or part boundaries. Gap approval remains [write-path owner question 3](corpus-write-path.md#risks-and-owner-questions); technical review does not delegate that authority.
