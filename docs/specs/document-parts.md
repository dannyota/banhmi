Proposal. Not approved, not implemented.

# Vietnamese document parts

## Goal and limits

Make a Phụ lục or Biểu mẫu findable, citable, date-aware, readable, and downloadable without losing its parent instrument or source evidence.
Treat a legal part, its text transcription, and its official file as separate concepts.
An appendix can contain many forms; one file can contain many parts; one part can appear in several files.

This proposal covers Vietnam at **Tra cứu quy định**, `tracuuquydinh.vn`.
The owner requires Vietnam hosting on VNGCloud; the five other country services remain unchanged on AWS.
That settled instruction follows the current [website decisions](../design/website.md#decisions).
Keep country and subject context in the contracts, but add no country or subject in this work.
Keep `guide`, `corpus_status`, `quality_gaps`, `search`, and `document`.
Serve evidence only, with no answer model, generative model, or reranker.
Only the self-hosted query embedder handles Vietnamese questions; questions never enter third-party processing.
Vietnam questions are Vietnamese with diacritics or a document number; the serving layer validates the question before any embedding or corpus query. English is interface text only.
Public legal text uses Google Cloud Vision for OCR after the public-payload gate. Private Vietnam vStorage holds OCR outputs and original files under the [conversion decision](document-conversion.md#decisions); its OCR bake-off is optional later work.
Bulk document embedding runs on Hugging Face Jobs, pay as you go, with Runpod Flex as fallback under the [write-path decision](corpus-write-path.md#decisions). Kaggle and GitHub Actions free tiers serve evaluation and bake-offs only.

Do not build form filling, submissions, user uploads, legal advice, or automatic consolidation.
The write path computes amendment differences in its relations and validity stage, the database stores them, and the website design owns redline display. Parts and conversion do not own redline.
Provision-level validity, relation attribution, durable keys, and releases remain corpus redesign dependencies.
The website's exclusion of stored user PDF copies does not prevent archiving public official originals for corpus provenance.
This task writes only this file. Index updates and shared-document reconciliation belong to the later approved implementation.

## Current state and evidence

“Coded” below means present in source, not executed in this design pass.
“Recorded validation” means a repository record of checks against real documents, not a new check.
No deployed state or live corpus inventory was verified here.
The source refactor still needs generated stores, durable adapters, and corpus-specific hosted execution: [corpus source refactor](../ARCHITECTURE.md#corpus-source-refactor), [source status](../plans/corpus-redesign-source.md#source-status).

| Area | Evidence and state | Consequence |
|---|---|---|
| Document types | Coded Công Báo mapping accepts Luật, Nghị định, Thông tư, Quyết định, Văn bản hợp nhất, Thông tư liên tịch, Nghị quyết, Chỉ thị, Công văn, Công điện, Pháp lệnh, Sắc lệnh, Lệnh, Hiến pháp, Thông báo, Kế hoạch, and Quy định: [discover.go:148](../../pkg/ingest/congbao/discover.go#L148). VBPL retains source type names: [discover.go:146](../../pkg/ingest/vbpl/discover.go#L146). | This is supported vocabulary, not proof that every type has indexed documents. |
| Known holdings | Recorded validation names four indexed Luật: [banking statute record](../../PLAN.md#vn-banking-statutes). It records 344 indexed consolidations and an appendix retrieval check: [consolidation record](../../PLAN.md#consolidation-and-conversion-evidence), [appendix retrieval record](../../PLAN.md#consolidation-and-conversion-evidence). | Do not claim appendices are universally absent. Current counts by type and part remain unknown. |
| VBPL files | Coded selection keeps DOCX, DOC, PDF; filename rules recognize Phụ lục, Biểu mẫu, and Mẫu số as appendices: [detail.go:419](../../pkg/ingest/vbpl/detail.go#L419), [detail.go:493](../../pkg/ingest/vbpl/detail.go#L493). | Files can be captured without becoming independently searchable parts. Other file formats are skipped. |
| Other collectors | Công Báo labels every parsed download `main`: [detail.go:129](../../pkg/ingest/congbao/detail.go#L129). Vanban accepts PDF/DOCX and skips DOC; its appendix filename rules are narrower: [detail.go:99](../../pkg/ingest/vanban/detail.go#L99), [detail.go:150](../../pkg/ingest/vanban/detail.go#L150). | Role labels cannot prove a file contains only the main body. Legacy form files can be missed. |
| Extraction selection | Coded cascade selects main DOCX, HTML, main DOC, then PDF, returning after a usable result: [process_activities.go:80](../../pkg/pipeline/process_activities.go#L80). `pickFile` deliberately excludes appendices from primary text: [process_activities.go:1373](../../pkg/pipeline/process_activities.go#L1373). | A successful body does not imply attachment coverage. Selecting one main file can omit other main-labelled attachments. |
| Supplement gate | `supplementOnlyText` recognizes appendix/form openings and rejects them as body candidates: [process_activities.go:256](../../pkg/pipeline/process_activities.go#L256). Normalize repeats the rejection: [normalize_activities.go:298](../../pkg/pipeline/normalize_activities.go#L298). | A separate official form cannot safely enter through the body-text path. It needs a part-specific gate. |
| Tables | Active MuPDF returns concatenated page text: [fitz.go:47](../../pkg/extract/fitz/fitz.go#L47). The [HTML reader](../../internal/corpus/conversion/html.go) joins cells with spaces and separates rows with line breaks. | Cell boundaries, merged cells, and header associations are not preserved for display. |
| Unused DOCX reader | The [Go reader](../../internal/corpus/conversion/docx.go) emits pipe tables, flattens nested tables, and pads column spans. The extraction design identifies it as unwired: [extraction.md:43](../design/extraction.md#L43). | Wiring it unchanged would still lose table structure. |
| Appendix boundaries | Coded grammar recognizes standalone appendix labels and uppercase inline headings: [legalparse.go:91](../../pkg/pipeline/legalparse.go#L91), [legalparse.go:349](../../pkg/pipeline/legalparse.go#L349). Unclassified text attaches to the open node: [legalparse.go:600](../../pkg/pipeline/legalparse.go#L600). | Recognized appendices close the last Điều. A missed heading or bare Mẫu số can remain in the last Điều or its khoản. Text before any node can be dropped. |
| VBPL tree merge | Normalize recovers root appendices from selected binding text only; failures yield no appendices: [normalize_activities.go:167](../../pkg/pipeline/normalize_activities.go#L167). Any existing appendix root suppresses the entire text appendix set: [normalize_activities.go:192](../../pkg/pipeline/normalize_activities.go#L192). | Missing attachments and incomplete merges remain possible. Mixed OCR parts cannot inherit the tree's authority. |
| Search and citations | Index explicitly chunks appendix text and qualifies nested Điều citations: [index_activities.go:340](../../pkg/pipeline/index_activities.go#L340). It drops duplicate content and fragments under 20 runes: [index_activities.go:218](../../pkg/pipeline/index_activities.go#L218). Section kinds contain `phuluc`, but no form or table kind: [schema.sql:89](https://github.com/dannyota/banhmi/blob/1626a06ea0d1afc2760e80c47d951c976ad271d5/sql/silver/schema.sql#L89). | Embedded form text can be searchable under an appendix or wrong Điều, but lacks a distinct Mẫu số address. Short forms and identical forms under different citations can disappear. |
| Scans | OCR selects one PDF per document from `main`/`original_scan`, excluding documents with binding text: [ocr_all.go:747](../../pkg/pipeline/ocr_all.go#L747). Output stays non-binding: [ocr_all.go:302](../../pkg/pipeline/ocr_all.go#L302). | A scanned appendix can be missed when its parent body is already readable. |
| Storage and validity | Raw files retain URL, hash, label, size, and role: [schema.sql:78](https://github.com/dannyota/banhmi/blob/1626a06ea0d1afc2760e80c47d951c976ad271d5/sql/bronze/schema.sql#L78). Text uniqueness is document/authority/source: [schema.sql:233](https://github.com/dannyota/banhmi/blob/1626a06ea0d1afc2760e80c47d951c976ad271d5/sql/silver/schema.sql#L233). Normalization writes document validity: [normalize_activities.go:447](../../pkg/pipeline/normalize_activities.go#L447). | Existing text identity cannot hold several same-source forms independently. Optional section validity fields do not prove part validity coverage. |
| Read contract | `document` exposes files and source links: [api.go:230](../../internal/evidence/api/api.go#L230), [api.go:404](../../internal/evidence/api/api.go#L404). The adapter merges files by hash and excludes expiring links: [corpus.go:1726](../../pkg/mcp/corpus.go#L1726). | Downloads are not entirely absent. Missing contracts are part-to-file membership, part structure, and part validity. |

The approved website direction is still a proposal: [website decisions](../design/website.md#decisions).
Its Phase 3 records provision validity and misattributed amendment gaps: [corpus redesign dependency](../design/website.md#phase-3-depends-on-the-corpus-redesign).
This design extends those contracts to parts; it does not claim to repair them independently.

## Official examples and verification limits

These are source observations, not current-law conclusions or evidence of ingestion.
Use historical dates in evaluation so later amendments do not silently change expectations.

| Example | Official evidence | What it exercises |
|---|---|---|
| State Bank forms within an appendix | [08/2024/TT-NHNN, VBPL](https://vbpl.vn/TW/Pages/vbpq-toanvan.aspx?ItemID=167911) search-index text identifies Mẫu số TTLNH-01 and its reference at điểm a khoản 1 Điều 35. [Công Báo detail](https://congbao.chinhphu.vn/van-ban/thong-tu-so-08-2024-tt-nhnn-42292/50909.htm) opened and lists PDF and DOC. Its search-index excerpt suggests an appendix catalogue, but the [download URL](https://congbao.chinhphu.vn/tai-ve-van-ban-so-08-2024-tt-nhnn-42292-50909?format=pdf) redirected to the detail page. Direct VBPL and CDN file reads failed. The reference, catalogue, full appendix, and layout remain unverified against captured bytes. | Candidate fixture for letter/hyphen form codes and catalogue-versus-form boundaries. |
| Replacement confined to an appendix | [44/2025/TT-NHNN, VBPL](https://vbpl.vn/nganhangnhanuoc/Pages/vbpq-toanvan.aspx?ItemID=183788) opened. Điều 8 replaces the appendix of 07/2024/TT-NHNN; Điều 10 gives 2026-01-03 as the effective date. The page lists a separate appendix DOC. File bytes remain unverified. | The replacement operation targets only an appendix. The same circular also makes other amendments; it is not an appendix-only instrument. |
| A separately attached form | [19/2025/TT-BTP, VBPL](https://vbpl.vn/TW/Pages/vbpq-toanvan.aspx?ItemID=183542) opened. Its attachment list includes separate Biểu mẫu 01 and 02 PDFs. [Biểu mẫu 01 PDF](https://vbpl.vn/FileData/TW/Lists/vbpq/Attachments/183542/VanBanGoc_Bi%E1%BB%83u%20m%E1%BA%ABu%2001.pdf) yielded two pages of text. Screenshot retrieval failed, so visual layout is unverified. This is a format example, not a scope expansion. | One form has its own file. Table headers need structure; the form's identity differs from the parent instrument. |
| Official host does not prove issuance | [SBV attachment 351406](https://www.sbv.gov.vn/documents/d/sbv_portal/351406?download=true) opened as PDF text. Its 2018 instrument number and signing date are blank; it includes proposed replacement forms. Issued status is unverified. | Keep unverified or draft attachments out of current-law results even when the hostname is official. |

No file hash was computed here. None of these examples is an approved corpus golden until reviewers check captured originals and parent references.

## Recommended decisions

| Decision | Recommendation | Rejected option and reason |
|---|---|---|
| Part identity | Extend the shared evidence hierarchy with typed parts and explicit source anchors. | Independent documents for every attachment lose parent scope and legal effect. Flat appendix text loses form addresses. |
| Part identification | Consume conversion's normalized `ConvertedDocument` blocks, then apply Vietnamese grammar for legal boundaries. | Separate format extraction here duplicates conversion ownership. A generated parser or filename-only classifier cannot prove legal identity. |
| Authority | Assign authority and review state to each transcription and block. | Inheriting the body's binding flag would mislabel OCR and draft files. |
| Validity | Resolve part targets through the same evidence rules as provisions. | Copying the parent's current stamp hides replaced or repealed forms. |
| Downloads | Serve archived, hash-verified original bytes from Vietnam through an authenticated route. | Expiring source URLs cannot reproduce the cited file. Converting everything to PDF destroys editable originals. |
| Tables | Keep an ordered structural representation and a deterministic search rendering. | Pipe tables alone cannot preserve nested tables or spanning headers. |
| Rollout | Add bounded part cohorts to isolated candidate releases. | Blanket live normalization recreates section IDs and risks old citations: [normalize_activities.go:420](../../pkg/pipeline/normalize_activities.go#L420). |

## Part model and deterministic parsing

“Part” means a citable unit or a structural container within one document revision.
“Artifact” means captured official bytes. “Block” means conversion's ordered `heading`, `paragraph`, `list_item`, `table`, `image`, or `page_break` with source anchors.
Database-owned evidence keys identify parts. Human labels never serve as global keys.
Document parts (Phụ lục, Biểu mẫu, Mẫu số, tables, attached files) are first-class revisioned units.

| Part kind | Native identity and behavior |
|---|---|
| `body` | Container for the existing Chương/Mục/Điều/Khoản/Điểm hierarchy. Preserve existing citations. |
| `issued_text` | A named Quy chế, Quy định, or other text issued with an instrument. Give its own Điều tree a namespace without inventing a Phụ lục label. |
| `appendix` | Exact Phụ lục label, optional printed designator, and title. May contain provisions, forms, tables, or nested appendices. |
| `form` | Biểu mẫu, Mẫu số, Mẫu biểu số, or Biểu số with its exact code and title. Parent can be an appendix, issued text, or document. |
| `table` | Ordered rows, cells, headers, caption, and notes within a legal part. A table without a printed label has a technical anchor, not an invented legal citation. |
| `attachment` | A file part, including a spreadsheet. Expose file metadata, qualified cell-text search hits, and the official download link. Unresolved legal structure carries a gap; do not invent a form identity. |
| `excluded_block` | Instrument signature, recipient list, page furniture, or navigation. Retain source anchors and exclusion reasons, but omit from search chunks. |

If an appendix is itself one form without a separate form heading, keep one `appendix` with role `form`.
Do not create a duplicate “Mẫu số” node. A distinct printed form heading creates a child `form`.
Signature and recipient fields inside a blank form are form content and remain visible and searchable.
Excluding an instrument's signature block must never stop scanning for appendices after that block.
Keep the instrument's execution and publication evidence available for authority review even when excluded from search.

Each part needs parent identity, immutable revision reference, kind, role, exact label and code, title, source order, and citation components.
Also retain source observation, artifact hash, block/page anchors, extraction recipe, authority, confidence, review state, and coverage gaps.
Retain raw source bytes separately from NFC text; whitespace normalization serves matching, not citation rewriting.
Keep absence distinct from an empty field. An unnamed appendix has no invented printed number.
Processing changes create revisions; they do not create a new legal effective date.
Cross-revision identity requires source evidence or a reviewed match, never text similarity alone.
Consolidated documents retain their own identity and issue year; links to original parts need evidence, not matching form numbers.

Part identification runs as a versioned state machine over normalized `ConvertedDocument` blocks:

1. Enumerate every official attachment and body representation. Record unsupported formats and failed fetches as gaps.
2. Consume conversion's normalized `ConvertedDocument` blocks, artifact manifests, authority, source spans, and quality findings. Conversion owns format adapters, normalization, and extraction from bytes, including appendices and forms inside a file.
3. Select evidence per part, not once per document. Keep conflicting representations; do not splice mismatched versions into one form.
4. Recognize headings at block starts outside quotes, data cells, and catalogue rows. A heading in a layout-table cell needs a matching title/issuance block; ambiguous cells stay unclassified.
5. Parse appendix labels as `Phụ lục [số] [designator]`, case-insensitively. Accept decimal or Roman designators with letter suffixes. Preserve `01`, `I`, and `1a` separately.
6. Parse form headings as `(Mẫu [biểu] [số] | Biểu mẫu [số] | Biểu số) code`. Codes can contain letters, digits, periods, slashes, and hyphens, such as `TTLNH-01` or `06/TTTD`.
7. Recognize catalogue rows and “theo Mẫu số ...” prose as references, not boundaries. A catalogue can establish an expected part, but cannot supply its missing body.
8. Track quoted replacement blocks and their enclosing amendment clause. A quoted heading is replacement evidence, not a second enacted body node.
9. Return confirmed `issued_text`, appendix, and form boundaries and namespaces to conversion's `BuildStructure`. Open an `issued_text` namespace only with a matching title and verified issuance evidence. Its body builder closes the previous body region and restarts Điều numbering only inside the declared container. Never turn form field numbers into khoản automatically.
10. Preserve repeated page headers as source blocks, but suppress duplicate headings only with page-continuation evidence. Identical labels with different anchors stay distinct and unresolved until reviewed.
11. Reconcile source-tree and extracted parts individually by declared identity and evidence. One recognized appendix must not suppress other appendices.
12. Emit unclassified content with anchors and a gap when grammar cannot establish structure. Do not attach uncertain trailing material to the last Điều or invent native labels.

Return classified block ranges, namespace boundaries, required-artifact inventory (attachment expectations), exclusions, reference spans, and part-gate findings before conversion assigns final block ownership.
Conversion owns `BuildStructure`, final block ownership, and the document `QualityGate`. Each retained block has exactly one body/part/cell container or explicit unresolved region; references never duplicate ownership.
The part gate reports source association, non-placeholder content, boundary evidence, and conversion's text-quality findings. It permits short forms without Điều headings but cannot override conversion's gate or authority rules.
Workflows owns OCR submission through `OcrBatch` and quarantine. Scanned attachments remain eligible even when the parent body has binding text; returned OCR blocks repeat normalization, structure building, and gates.
OCR stays non-binding after review; a reviewer may add a separately identified verified transcription without relabelling OCR output.
Uncertain scan table structure stays an image/page reference plus OCR text, with a table gap.

### Citations and references

Build citations from exact source components in Vietnamese legal order.
The schematic form is `Mẫu số 01 Phụ lục II ban hành kèm theo Thông tư số <number>`.
Use the source's actual wording, including `kèm theo` when `ban hành` is absent.
An actual reference example is `Mẫu số TTLNH-01 Phụ lục kèm theo Thông tư số 08/2024/TT-NHNN`.
Keep release, legal date, artifact hash, and application URL as citation metadata, not invented statutory wording.
For unnamed or duplicate labels, show the source label plus a separate page/occurrence locator.
Never present parser counters as printed appendix numbers.

Parse references from a governing phrase such as `theo`, `quy định tại`, or `ban hành kèm theo` followed by a part selector.
Resolve “Thông tư này” to the enclosing instrument; inherit an omitted appendix only from explicit enclosing context.
Resolve a bare “Mẫu số 01” only when the document has one eligible target in that context.
Store source provision revision, exact reference span, parsed selector, target part revision or unresolved stub, and resolution evidence.
Return both the forward link and `referenced_by`; distinguish `uses_form`, `issues_part`, and an amendment target.
A file attachment is not automatically an `issues_part` assertion. Require a verified issued parent and a confirmed attachment association, supported by its issuance clause or issuance line.
An official hostname or issuance line alone does not prove issuance. Missing execution fields, draft markers, or conflicting publication evidence require review; blank signature fields inside a form do not make the parent a draft.
Ambiguous references remain visible without a guessed link. Cross-document links preserve the target's parent document.

## Part validity and relation evidence

Provisions and parts share one validity target type so amendment targets and validity intervals can point at either. Use the shared validity engine.
Each assertion needs legal `[effective_from, effective_to)` dates, observation time, source clause, exact span, target selector, and review state.
Use Vietnam calendar dates; an unknown boundary stays unknown. The interval is half-open and excludes its end date.
Map an explicitly inclusive last effective day to the following calendar date as the exclusive end; retain the original wording and conversion evidence.
Keep document status, part status, text authority, and coverage as separate fields.
An in-force parent alone cannot produce a verified in-force part stamp.

| Operation | Required evidence and effect |
|---|---|
| Issue/add | Resolve the issuance/addition clause, attached part, and applicable commencement clause. Start that part's interval. |
| Replace | Parse `thay thế <target> bằng <replacement>`. End the target revision at the evidenced date and link the replacement under its issuing document. Preserve the old citation. |
| Repeal | Parse `bãi bỏ <target>`. End only that target and descendants covered by the stated scope. Do not mark the whole document repealed. |
| Amend within a part | Resolve the named row, field, note, or subpart. Retain the exact change and mark the enclosing part partial when evidence supports that status. Defer synthesized consolidated text. |
| Suspend/resume | Apply the same provision contract. Without supported dates and scope, report unresolved validity rather than current status. |

Operators must bind the target after the operator, exclude recital history after `theo`, and separate quoted replacement text.
List replacements require explicit pairing or a reviewed ordered correspondence; unequal or ambiguous lists stay unresolved.
The 44/2025 example requires a target appendix in 07/2024 and a replacement appendix in 44/2025, not a whole-document replacement edge.
Whole-appendix replacement closes contained forms unless explicit savings or transition clauses preserve use.
Replacing one child form leaves siblings unchanged. Do not infer that a new file at the same URL replaces a form legally.
Do not infer that repealing an amending instrument revives an older form.
Conditional transition rules remain quoted conditions with applicability unknown unless the shared validity contract can resolve them.
Unknown or conflicting targets, dates, and effects produce gaps and cannot enter strict current-only results.
Part-specific confirmed effects override a parent summary; unresolved parent partial repeal cannot become a green child stamp.
Evaluate `as_of` against part evidence from the pinned release; when omitted, use today's Vietnam calendar date. Record the scope and basis of any inherited status explicitly.

## Files and table fidelity

Keep every selected official artifact byte-for-byte, including qualified XLSX, separate forms, and whole-document files. Capture and extraction admission do not expand the application download allowlist.
Record artifact identity, SHA-256, byte size, media type, source filename, stable source page, observed download URL, capture time, and observation reference.
An expiring fetch URL stays internal; public provenance uses a stable source page and the archived artifact reference.
Many-to-many part/artifact links carry page/block ranges, byte spans where available, and roles: original, transcription source, scan, or derived preview.
Byte spans identify the hashed artifact and offset basis; unavailable byte mappings stay explicit gaps, never offsets borrowed from a derivative.
Deduplicate identical bytes, not legal identities. A form found in two documents still has two legal contexts.
Keep original DOC when conversion produces DOCX. Label converted files and previews as derivatives with their own hashes and recipes.
If only a whole-document original exists, offer `Tải văn bản gốc chứa biểu mẫu`; never call a cropped or generated file the official original.
Expose each official format, rather than silently choosing an editable file over a signed scan.
Preserve links to unparsed attachments and state `Chưa trích xuất nội dung`; do not discard their existence.
Accept qualified XLSX cell-text blocks through conversion's shared format-admission policy. Show spreadsheets as file parts with metadata, search hits, and the official-source download link, without rich rendering in the first release.
Unqualified spreadsheet routes, presentations, and archives get metadata and source links only, with explicit format gaps. Format qualification belongs to [conversion owner question 1](document-conversion.md#risks-and-owner-questions); this design sets no competing admission policy.

Recommend `GET /van-ban/{document}/tep/{artifact}?release={release}` on the Vietnam origin, bound to a retained release and authorized evidence request.
MCP clients receive a stable application URL for the user to open in a signed-in browser, plus the source URL. Downloads require the existing web-session and account-state checks.
Do not accept MCP bearer tokens on the download route: they are restricted to `/mcp` by [AUTH.md:202](../design/AUTH.md#L202) and [AUTH.md:247](../design/AUTH.md#L247).
Do not put tokens, query text, or signed storage URLs in citations. Downloads require no source-site request and no query embedding.
The route resolves only catalogued artifacts, never arbitrary paths or caller-supplied URLs.
Serve bytes as attachments with safe filenames, declared media type, `nosniff`, and hash verification.
Before serving, require MIME, file signatures, and container structure to agree with the PDF/DOC/DOCX allowlist. Qualified XLSX extraction does not authorize application downloads; its official download remains a source link. Hash equality proves integrity, not type or safety.
Hosting defines configurable positive limits: maximum artifact bytes, concurrent download streams per client and overall, and stream timeout.
Serving checks the declared size before streaming and enforces actual bytes while streaming. Abort on a limit breach, timeout, or client disconnect and release the stream slot.
Keep mismatched, unsupported, or unassessed files archived but unavailable for application download; expose the official source link and an availability gap. Never execute macros during assessment or preview.
Render sanitized extracted blocks; never execute embedded macros, scripts, external images, or document links as part of previewing.
Missing or corrupt bytes yield `Tệp gốc chưa khả dụng` and a gap. Never substitute a newer file for a pinned hash.

The database contract must preserve these table fields, regardless of its physical representation:

| Field group | Required meaning |
|---|---|
| Identity and position | Parent part revision, table key, source order, source caption, artifact hash, page/block anchors. |
| Grid | Row/column coordinates, row/column spans, ordered cell blocks, nested tables, and header relationships. Empty, absent, and unreadable cells differ. |
| Legal content | Exact cell text, units, footnotes, note references, formulas as printed, and form instructions. Do not infer headers or recompute values. |
| Evidence | Authority, extraction recipe, confidence/review state per table or cell where available, and fidelity gaps. |
| Search rendering | Versioned deterministic row text with printed column labels, units, row labels, and attached notes; every range maps back to cells. |

Render non-spreadsheet tables as accessible HTML with explicit headers, spans, captions, and keyboard-scrollable overflow. Spreadsheet cell structure supports search mappings only in the first release.
Do not flatten a wide table into cards that detach numbers from their headings.
For PDF tables, publish structured cells only when deterministic extraction and review establish reading order and spans.
Otherwise show the official page reference and `Cấu trúc bảng chưa được xác minh` beside the transcription.

## Retrieval and evidence outputs

Index each form title, exact code, fields, instructions, and table rendering under its own part revision.
Index an appendix catalogue separately as a catalogue, linked to the child parts; a catalogue hit must not count as a form-body hit.
Use deterministic context containing document number/type/title, appendix label, form code/title, and native citation.
Do not add inferred purposes or synonyms to legal text. The embedding sibling owns models, token limits, fusion, and evaluation of ranking changes.

Keep one chunk for a short form. Split long forms at field groups or table row groups within the sibling's token budget.
Repeat header/units/notes as context with source pointers. Mark any unavoidable split of an oversized cell as a continuation.
Never mix different forms or validity intervals in a chunk. Do not drop a short form solely because it lacks body-length text.
Deduplicate representation copies within one part, not matching text under different legal citations.
Roll up by part identity; preserve form codes and distinguish a form from its referencing Điều in result diversity rules.
Retrieve titles and exact codes through dense plus lexical fusion. No reranker anywhere; no model-generated query expansion.
Metadata-only parts may return labelled discovery hits with `text_available=false`, no fabricated snippet, and no claim of full evidence.
Their existence does not satisfy a golden requiring fields, table content, or a usable original file.
Keep parent scope and subject context. Finding a generic form must not bypass the corpus scope gate.

Both transports use the shared evidence service. Proposed additive fields follow:

| Surface | Part contract |
|---|---|
| `search` | Add optional `part_kind` filter. A part hit includes `part_ref` (key, revision, kind, label, title, parent), native citation, parent document, part validity, authority, gaps, and `document` pointer. Standard/full detail adds bounded `referenced_by` and file availability; compact retains part identity and stamp. |
| `document` input | Add exact `part_ref`, `as_of`, `include=parts`, and `include=files`; retain existing document/citation lookup. Contradictory selectors fail explicitly. A part reference identifies its document and release. |
| `document` output | Add a bounded part tree, selected part blocks, tables, files, validity assertions, reference links, and gaps. Keep legacy `chunks` as a deterministic text projection. Paginate children, blocks, and references with release-bound cursors. |
| `files` | Add artifact reference, stable authenticated download URL, part membership/range, original/derivative role, and availability to the existing hash/format/origin fields. |
| `guide` | Explain form lookup, part citations, download authentication, OCR limits, and opening the referencing provision. |
| `corpus_status`, `quality_gaps` | Report observed/expected parts, extraction/index/file coverage, unresolved references, and table/validity gaps for the same release. |

Exact part lookup must return the part even if it has no chunks, with its missing-text gap.
Leave existing non-part fields and citation bytes intact. Capability metadata identifies support for the additive part contract.
Use part fields for part status and parent fields for document status; never silently reinterpret legacy document validity.
Keep standard responses bounded and expose truncation plus cursors. A full request must not return an unbounded appendix.
No new MCP tool or request-time source fetching is needed.

## Website presentation

Use the proposed [routes](../design/website.md#site-map-and-access-boundary) and [visual system](../design/website.md#visual-system).
Use Be Vietnam Pro for controls, Noto Serif for legal text, and Roboto Mono for citations and document numbers.
Keep self-hosted assets, device theme, legal text `lang="vi"`, keyboard access, and the four validity inks.
English may translate controls only; legal text, questions, codes, and citations stay Vietnamese.

| Surface | What the user sees |
|---|---|
| `/tra-cuu` | `Loại nội dung`: `Tất cả`, `Điều khoản`, `Phụ lục`, `Biểu mẫu`. A form card shows citation, part stamp, parent document/date context, a verbatim title or matched field, and actions in that order. |
| Result actions | `Xem biểu mẫu`, `Tải tệp gốc`, `Sao chép trích dẫn`, `Lưu vào hồ sơ`. Show format and size before download. `Được dẫn chiếu tại` opens the exact Điều/khoản/điểm and labels unresolved references. |
| `/van-ban/{document}` | Extend the contents tree with `Văn bản ban hành kèm theo`, `Phụ lục`, and `Biểu mẫu`. Use `?release=...&as_of=...&part={part_key}#phan-{part_key}` for a stable part target. Encode each selector as a URL component. Keep the parent document header visible. |
| Part reader | Show part citation and stamp above legal text, then tables, notes, `Được dẫn chiếu tại`, `Thay thế bởi`, and `Nguồn và tệp gốc`. Page/block links open the original evidence. The side rail keeps parent validity separate. |
| Citation copy | Copy the exact Vietnamese citation plus applies-on date, part revision/release, official source URL, and artifact hash. Offer the same evidence block through `Gửi sang AI`; do not send it automatically. |
| Dossier | Browser-local entries first, then `/ho-so` under the website phases. Pin the immutable part reference, release, legal date, citation, source, and artifact hash. A local pin does not create a server retention hold; show the release's availability deadline. Store no filled form, question text, or copied original file. |
| Dossier refresh | Preserve the pinned version. Show a replacement notice with links to both versions; never silently upgrade a form. An unavailable retained release remains explicitly unavailable. |
| `/mau/...`, `/nguon` | Generate approved sample cards and part coverage from one release. Samples make no live anonymous evidence call. |

Use `Còn hiệu lực`, `Còn hiệu lực một phần`, `Chưa có hiệu lực`, `Hết hiệu lực`, and `Đã bãi bỏ` only when part evidence supports them.
For replacement effective on or before `as_of`, use the red historical stamp and `Đã được thay thế` with the acting clause. Before that date, show the evidenced status for `as_of` and a separate future-replacement notice.
Use a neutral outlined `Chưa xác định hiệu lực` state for unknown part status; never reuse the parent's green stamp.
Show `Bản chép OCR, không phải văn bản gốc` and `Cần đối chiếu bản gốc` outside the stamp.
The original scan remains official evidence even when its transcription is non-binding.

### Login continuation

Extend `next` to validated same-origin relative reader and download paths, preserving pinned selectors across signed-out access and expired sessions.
The reader allows only `release`, `as_of`, and `part`; downloads allow only `release` and retain the document and artifact identifiers in the path.
Require a release on generated links. Resolve and preserve the selected legal date before creating a reader link; never recalculate that date during login.
Bound raw and decoded `next` to 2,048 bytes, each identifier to 256 decoded bytes, and `as_of` to a valid 10-byte `YYYY-MM-DD` date. Reject duplicate or unknown parameters and identifiers outside the shared key grammar.
Validate path shape and encoding before storing the canonical continuation in the login transaction; revalidate at login start and callback. Never trust a replacement callback destination.
Reject absolute/scheme-relative URLs, traversal, backslashes, control characters, encoded bypasses, and fragments in `next`. Preserve AUTH's separate internal OAuth continuation rules.
Carry the part selector in `part`, since the server cannot recover a browser fragment. After authentication and release-bound membership checks, restore `#phan-{part_key}` from that selector.
For downloads, recheck current account authorization, artifact membership, and retained hash before streaming the same artifact from the same release.
An invalid destination fails explicitly. An expired or unavailable release returns explicit unavailability after authentication, never the active release or newer bytes.
Reconcile the [site map and access boundary](../design/website.md#site-map-and-access-boundary) and [AUTH.md:67](../design/AUTH.md#L67) before implementing these routes. Their existing continuation allowlists do not support this contract.

## Interfaces

| Sibling | Required contract; internals remain with its owner |
|---|---|
| [corpus-write-path.md](corpus-write-path.md) | Enumerate and preserve attachment manifests; record part recipe outputs, unresolved gaps, and review assertions. `CorpusBuild` selects build-eligible revisions into its manifest. Publication binds parts, files, relations, and indexes to one release. Own amendment differences in the relations and validity stage. No blanket body normalization. |
| [document-conversion.md](document-conversion.md) | Own extraction from bytes to normalized `ConvertedDocument` blocks for every format, format admission, normalization, final block ownership, and the document gate. Parts returns classified block ranges, namespace boundaries, required-artifact inventory, exclusions, reference spans, and part-gate findings. Conversion supplies geometry, cell fidelity, provenance, and quality reports; parts identifies, cites, tracks, and displays parts. |
| [corpus-workflows.md](corpus-workflows.md) | Own OCR submission, quarantine, and decision delivery. `DocumentIngest`, `OcrBatch`, and `Reprocess` use intake execution records keyed by workflow ID and attempt fence, with no build ID. Every activity reading untrusted bytes runs on `corpus-converter-v2`: LibreOffice, MuPDF including text serialization, DOCX/HTML/XLSX parsers, image decoding, OCR request building, and converter-output parsing. Structure building over blocks with no raw-byte reads may use plain CPU workers. |
| [corpus-database.md](corpus-database.md) | Supply first-class revisioned parts, parent links, native citation components/bytes, ordered blocks, table rows/cells/spans, and many-to-many part-to-artifact byte spans. Provisions and parts share one validity target type. Own intake ledger, revision acceptance state and eligibility; link revision provenance to the intake execution ID, keeping legacy `document_revision.build_id` nullable. Store raw to normalized offset maps, reversible edit ledger, quality report (metrics, thresholds, outcome, reason codes), and review decisions linked to each document revision. Supply coverage denominators and release-bound reads; retain old mappings and referenced blobs. |
| [embedding-models.md](embedding-models.md) | Accept part text/context and exact-code fields, enforce a declared token budget, preserve part boundaries and source-range mappings, and score part goldens without a reranker. Own chunk-budget tuning, fusion, and model migration. |
| [vngcloud-hosting.md](vngcloud-hosting.md) | Provide Vietnam-local original/preview delivery, authenticated downloads with the positive byte/concurrency/timeout limits above, integrity checks, and artifact retention availability. After any restore, serving reconciles its active release with the writer's ordered activation records before accepting traffic; stale release control state never reactivates. No external document viewer or request-time third-party processing. Own storage, network, and capacity choices. |

A revision is build-eligible only in state `accepted` or `accepted_with_gaps`. A reprocessed revision remains a candidate until its before and after comparison passes and, when critical text changes, the owner adjudicates.
The shared quarantine decision contract is decision ID, document version ID, quality report hash, workflow fence token, actor, action, reason. Authority is the owner or reviewers the owner designates, under [write-path owner question 3](corpus-write-path.md#risks-and-owner-questions).
The owner reviews through AI agents calling the [review API first](corpus-review.md#decisions), behind mutual TLS with a private CA; the human console is deferred. Keep the decision contract, opaque reviewer key, and audit journal.
Actions: retry with another converter; accept as non-binding OCR; accept with named gaps (state `accepted_with_gaps`); mark as a coverage gap and exclude; attach an alternate official source. Parts supplies findings and never grants acceptance independently.

Vietnam corpus generation runs on isolated VNGCloud writer and worker capacity in Vietnam, orchestrated by Temporal Cloud. The small serving host runs no corpus worker or conversion process. The other five country services remain on AWS.
Originals, previews, and release-bound part artifacts use private Vietnam vStorage only after provider API, signing, IAM, retention, and recovery behavior passes implementation gates.
Online retention is the active release plus at least two prior releases, kept for at least 30 days, plus any release under an explicit pin or hold.
Retain each release's part mappings and referenced artifacts with that bundle. Browser-local dossier entries do not create server pins or holds.
Only one embedding model runs on the serving host at a time, under the [embedding runtime policy](embedding-models.md#migration-and-rollout-slices).
Before replacing the runtime, re-embed every release that stays in dense search, or retire older releases from dense search.
Retired releases remain available through `document` and lexical search, with a visible gap. Retention, pins, and holds still protect their evidence and recipe artifacts.
Dense search requires a matching query runtime (same model, tokenizer, recipe and dimensions). No dual-runtime capacity is reserved.
Start from `a-general-4x8` (4 vCPU, 8 GiB), with `a1-standard-2x8` as the cheaper fallback if measurement allows, under the [hosting decision](vngcloud-hosting.md#workload-and-sizing).
Size the dedicated serving vServer from measurement for PostgreSQL, one embedder, the Go server, auth, and the approval and audit service at 2 requests per second, bursts of 8, with concurrent service load. Grow only from measurement; [embedding question 1](embedding-models.md#owner-questions) owns latency.
If release or validity contracts are not ready, capture and assess candidates, but do not publish invented release IDs or current-part stamps.

## Evaluation and coverage

Use realistic Vietnamese cases with fixed legal dates and reviewed target part revisions.
Expected results name the part, referencing provision, required fields/table cells, artifact hash, and validity evidence as separate assertions.
Do not let a parent-document or relation-only match satisfy a form expectation.

| Proposed golden question | Required evidence |
|---|---|
| “Ngân hàng tôi muốn tham gia hệ thống thanh toán điện tử liên ngân hàng vào ngày 01/09/2024. Cần dùng mẫu đề nghị nào?” | TTLNH-01 in 08/2024 and điểm a khoản 1 Điều 35; verify captured originals before activation. |
| “Tôi cần xem biểu mẫu báo cáo công tác xử phạt vi phạm hành chính kèm Thông tư 19/2025/TT-BTP, gồm những cột nào?” | Biểu mẫu 01, table headers, original PDF, and extraction authority. Use as a format fixture unless scope review admits the document. |
| “Tôi đang giữ phụ lục của Thông tư 07/2024/TT-NHNN. Ngày 04/01/2026 có văn bản nào thay phụ lục này không?” | Exact appendix replacement, Điều 8 of 44/2025, commencement evidence, and old/new parts. |
| “Tôi cần Mẫu số 01 để làm báo cáo cho ngân hàng. Có thể xác định đúng mẫu chỉ từ số này không?” | Ambiguous form identities with parent documents and an explicit gap, without an arbitrary selected form. |
| “Bản mẫu này chỉ có bản quét. Tôi có thể tải bản gốc và xem phần chữ được nhận dạng không?” | Synthetic scoped fixture supplies the part reference; return original scan and labelled non-binding OCR separately. |

Add fixtures for duplicate codes across appendices, Roman/decimal labels, title-only parts, multipart files, form signatures, nested tables, merged headers, repeated page headings, missing appendices, scans beside readable bodies, and quoted amendment headings.
Add target-attribution negatives for recital documents, changed numbering, replacement lists, and partial repeal of one form.
Test the day before and the day of each effect, unknown dates, transition conditions, and release-pinned historical downloads.

Proposed release floors are acceptance targets, not measured results:

| Metric | Floor |
|---|---|
| Existing Vietnam retrieval suite | Preserve recall ≥ 0.90, MRR ≥ 0.66, current-law precision ≥ 0.99, abstention ≥ 0.95: [Makefile:69](../../Makefile#L69). |
| Form/appendix retrieval, fixed top 8 | Recall ≥ 0.95; mean reciprocal rank ≥ 0.80. Score two strata separately: requests for a form and requests for an appendix. Each needs at least 20 answerable cases; report overlap explicitly. |
| Part correctness on accepted goldens | 100% exact parent/citation/reference target, date-boundary status, original-file hash, and OCR labelling; zero false current stamps. |
| Parser and table fixtures | 100% expected part boundaries, retained fields, cell/span structure where declared supported, and explicit gaps elsewhere. |
| Ambiguous/missing/draft controls | 100% explicit uncertainty or blocking gap where expected; no fabricated part or usable-file claim. |

Measure recall against exact parts; count missing text and wrong revisions as misses. Keep known failures in the coverage report even when scored separately.
Measure part coverage with an independent expected set from attachment lists, appendix catalogues, issuance clauses, and sampled original pages.
Report expected, fetched, parsed, citable, indexed, downloadable, validity-resolved, and table-verified counts by source, document type, part kind, format, and scan status.
Deduplicate expectations by reviewed legal identity, not filename or parser output. Retain unresolved expected slots in denominators.
Require every declared part slot to have evidence or a named gap before publication; never equate accounted-for with successfully extracted.
Audit at least 30 randomly selected eligible documents per release, stratified by source and format, against full original pages; inspect all documents when fewer than 30 are eligible. Report misses and sample size.
Report sampled completeness separately from declared-slot coverage. A zero denominator means unknown or not applicable, never 100%.

## Rollout and hosted verification

Each slice requires an independent review and a GitHub-hosted result for its commit before the next dependent slice.
No build, test, generator, database, corpus job, or deployment ran for this proposal.
The hosted `generation` job runs disposable PostgreSQL checks: [replay helper](../../tools/ci/replay_generation.sh), [schema runner](../../tools/ci/run_schema_tests.sh).
Its presence does not prove execution. Extend hosted lanes only through an approved implementation plan.

| Slice | Deliverable | GitHub-hosted exit evidence |
|---|---|---|
| 1. Contracts and inventory | Part/block/artifact contracts, synthetic fixtures, bounded read-only inventory recipe. | Contract tests, existing citation snapshots, and inventory accounting tests; no live credentials in pull requests. |
| 2. Conversion handoff | Consume normalized `ConvertedDocument` blocks; return classifications, namespaces, attachment expectations, exclusions, reference spans, and part-gate findings behind a Vietnam capability. | Deterministic repeat runs prove each retained block has exactly one owner after `BuildStructure`. Include an issuing instrument and an unlabelled issued text that both contain Điều 1; prove distinct citations and single block ownership without inventing a Phụ lục label. Include qualified XLSX form/cell fixtures, table fixtures, and draft/quote/signature negatives. Keep all other jurisdiction fixtures unchanged. |
| 3. Candidate persistence | Implement sibling storage, intake provenance, and acceptance contracts with part-only revisions alongside body evidence. | Hosted generation, append-only clean/upgrade migrations, disposable-database constraints, exact hash/citation/provenance round trips; stale quarantine decisions and unadjudicated reprocessing cannot make revisions build-eligible. |
| 4. Part retrieval and transport | Candidate index, part validity projection, additive MCP/API contract, original-file route. | Fixed-candidate part goldens, unchanged Vietnam floors, transport parity, authorization and wrong-release download tests; declared/actual byte caps, per-client/overall concurrency, timeout and cancellation checks; qualified XLSX search with source links but no application download; search an old named release after re-embedding or explicit dense-search retirement with `document` and lexical access. |
| 5. Website and dossier | Parts in results/reader, accessible tables, spreadsheet file parts without rich rendering, citation copy, pinned dossier entries; reconcile WEBSITE and AUTH continuation contracts. | Synthetic browser checks at phone/desktop sizes and both themes; no external requests; correct OCR and historical stamps; presentations remain metadata/source links. Signed-out and expired-session reader/download flows preserve selectors and hashes; invalid destinations fail; expired releases show unavailability without substitution. |
| 6. Bounded real cohort | Protected workflow consumes a hash-pinned official-file manifest; reviewers approve its part expectations. | Real-document goldens, coverage report, before/after body-citation parity, zero unresolved publication failures, rollback rehearsal through sibling release contracts. |

Keep real document payloads out of Git under [security.md:7](../../instructions/security.md#L7); store only reviewed manifests and synthetic fixtures in the repository.
Fetch public fixtures into isolated hosted artifacts with expected hashes. A failed fetch or hash drift blocks validation rather than passing a skipped test.
Start with the reviewed example cohort, then expand by bounded manifests selected by source and format, not hardcoded parser exceptions.
Reuse unchanged body evidence and embeddings. Rebuild affected part chunks and the sibling's required candidate lexical revision.
Preserve legacy citations through explicit mappings; ambiguous mappings fail instead of pointing to another form.
Compare unchanged main-body citations and content before promotion. Any necessary body repair becomes a separately reviewed delta.
Publication, activation, and rollback belong to the write-path design; approval of this document grants no deployment permission.
Full-corpus coverage needs a measured baseline. No blanket live re-normalization or forced reindex belongs to this rollout.

## Risks and owner questions

| Risk | Required response |
|---|---|
| Official page lists draft or mismatched attachments | Verify issuance and parent association per artifact; keep conflicts visible. |
| Layout loss changes form meaning | Gate table fidelity; keep original downloads and source anchors available. |
| A missed effect makes an old form look current | Require part-specific validity evidence and negative target-attribution tests; unknown stays unknown. |
| Part growth increases response size and storage | Bound responses and extraction inputs; measure candidate part/file counts before expansion. Siblings own capacity and retention. |
| Historical links outlive retained bytes | Preserve cited blobs under the shared retention contract or report explicit unavailability. Never substitute current bytes. |

1. May file-only or OCR-only forms appear in ordinary search? Recommend yes as clearly labelled discovery evidence, with gaps; strict current-only filtering still requires resolved part validity.
2. Should browser-local dossier entries create server retention holds? Recommend no. Show the shared release-retention window and availability deadline. Longer guarantees remain [database owner question 1](corpus-database.md#risks-and-owner-questions); do not promise a different retention window here.

Unresolved facts: live holdings by document type and part, missing attachment volume, real table fidelity, downloadable original-byte hashes, and current deployed behavior.
The 08/2024 attachment could not be fetched; the 19/2025 form's visual layout could not be inspected.
The SBV attachment's issued status is unverified. No independent corpus evaluation or hosted result exists for this proposal.
