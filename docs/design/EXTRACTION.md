# Extraction

Turning a document's files into clean, citable legal text in the jurisdiction's binding language
(VN Vietnamese, MY English, ID Indonesian). Born-digital extraction stays deterministic; scanned or
failed PDFs use OCR run as a **batch** (`OcrAll`). The default OCR engine is **GCP Document AI**
Enterprise OCR (`ocr.engine: documentai`, `pkg/extract/docai/`); **EasyOCR** (Apache-2.0,
per-jurisdiction language from the registry: `vi` VN, `en` MY, `id` ID) remains available as the
`auto`/`local`/`kaggle` engine. Gemma 4 E4B OCR enhancement is MVP2, not current work. See
[SOURCES](SOURCES.md) for what we ingest, [SCHEMA](SCHEMA.md) for the tables. Examples below use the
VN pipeline (the reference jurisdiction).

## Principles

- **No AI as canonical parser.** Born-digital text is read deterministically. MVP1 OCR is extractive
  — the default engine is **GCP Document AI** Enterprise OCR (`ocr.engine: documentai`); **EasyOCR**
  (classic detect + recognize, per-jurisdiction language) remains the `auto`/`local`/`kaggle` engine.
  Neither invents or drops text; no generative OCR is wired into the current extraction path.
- **AGPL-3.0 (MuPDF) for born-digital extraction** — fine for banhmi (batch worker, not a network
  service; repo is public Apache-2.0). Document AI is a GCP managed service (no local dependency).
  EasyOCR is Apache-2.0 (PyTorch BSD).
- **Per-file quality gate.** PDFs are not assumed uniform — each file is checked: extract vs OCR.
- **NFC-normalized, never diacritic-folded.** "an toàn" must never become "an toan".
- A document may carry **many files**: prefer official **DOCX**, then official **HTML**, then
  official **DOC**, then **PDF/OCR**. All three — congbao, vbpl, and sbv_hanoi — are authoritative
  government sources; source + hashes carry provenance.
- **Do not transform when the source already did it.** For VBPL docs, the official provision tree
  (`doc/provision/tree/{id}`) feeds RAG sections first; go-fitz/OCR remains evidence and fallback.

## Engine selection (per file)

| Input | Engine | License | Notes |
|-------|--------|---------|-------|
| PDF | **go-fitz** (`github.com/gen2brain/go-fitz`, MuPDF via purego) | AGPL-3.0 (MuPDF) | `Text()` per page (~1 ms/page); quality-gated before binding use |
| DOCX | **go-fitz** | AGPL-3.0 (MuPDF) | `Text()` extraction |
| HTML body | **go-fitz** | AGPL-3.0 (MuPDF) | for vbpl body HTML |
| DOC | **LibreOffice headless → DOCX → go-fitz** | MPL/LGPL deps + AGPL-3.0 | legacy OLE `.doc`; DOC→DOCX conversion (not DOC→PDF) |
| Scanned / image-only | **GCP Document AI** (default) or **EasyOCR** (per-jurisdiction lang) | Document AI: GCP managed; EasyOCR: Apache-2.0 | extractive, **batched** (`OcrAll`), not inline; never the sole source of binding text |

go-fitz is a Go library linked into the app binary (MuPDF via purego FFI — no CGO, no Python).
**OCR is no longer inline:** PDF assessment is Go-side (run go-fitz, apply the content gate); a file
that fails is kept non-binding and **flagged `needs_ocr`**, and the separate `OcrAll` batch (below)
does all OCR in one pass. go-fitz returns 0 chars on scanner+signature-only PDFs, correctly detected
by the gate and routed to Document AI. 15-60x faster than the previous MarkItDown engine.
Rejected: `pdfcpu` (not a text extractor), `unipdf` (commercial), `rsc.io/pdf` / `ledongthuc` / `dslipak`
(CMap ceiling → garbled diacritics). OCRmyPDF/Tesseract was the previous OCR engine; EasyOCR replaced it
(better Vietnamese diacritics, complete transcription, no hallucination — see the bake-off note below).

## go-fitz extraction path

go-fitz (`github.com/gen2brain/go-fitz`) is the extraction engine for PDF, DOCX, HTML, and (via
LibreOffice DOC→DOCX conversion) legacy DOC files. MuPDF via purego FFI — no CGO, no Python in the
extraction hot path. The pipeline (`cmd/pipeline`) normalizes output to NFC, runs the same quality gate,
and records engine/source/checksum provenance in `silver.document_text`.

- **Cascade:** DOCX → HTML body → DOC (LibreOffice `soffice --headless --convert-to docx`) → source
  PDF → OCR. Source-specific file flags such as VBPL `relatedType` never override this order.
- **HTML:** persist and try only text-bearing bodies. VBPL can return an empty `*_content.html` shell,
  so `hasContent`/byte length is not enough.
- **Mojibake gate:** the content gate hard-fails on Cyrillic (`U+0400–U+04FF`, the cp1251 double-encode)
  in addition to the existing PUA/UTF-8-marker checks — Latin-script legal text never contains Cyrillic.
  `corpus_status`/`quality_gaps` flag it too.
- **DOC:** pure Go DOC parsers produce garbage on real VN government DOC files; LibreOffice remains the
  only reliable DOC reader. Legacy OLE `.doc` is converted to DOCX by LibreOffice headless with an
  isolated profile (DOC→DOCX, not DOC→PDF), then go-fitz extracts the DOCX.
- **DOCX/HTML:** go-fitz output becomes binding only if the standard text gate passes. If it extracts
  non-binding `needs_review` text, keep that row and do not OCR an `original_scan` PDF over it; the scan
  remains provenance and OCR is only for missing/failed text paths.
- **PDF:** run simple Go-side assessment: go-fitz `Text()` per page plus the content gate. Passing PDFs
  are accepted immediately. go-fitz returns 0 chars on scanner+signature-only PDFs — correctly detected
  by the gate. Conversion failure or gate failure flags the file `needs_ocr` for the `OcrAll` batch
  (no inline OCR).

## PDF quality gate (per file)

Two deterministic phases; failing either flags the file for OCR.

- **Phase 1 — extraction assessment (Go):** try go-fitz and classify explicit source
  placeholders (`Đang cập nhật file đính kèm`). Conversion failure flags `needs_ocr`. Official
  placeholders are kept non-binding and rechecked by Fetch.
- **Phase 2 — content (Go gate):** bad-char (U+FFFD) ratio, diacritic density,
  TCVN3/VNI private-use-area mojibake signature, visible UTF-8 mojibake markers, whitespace ratio, and
  overall confidence. Pass → keep; fail → flag `needs_ocr`.
- **Thresholds live in `config.setting`** (a new key/value config table, seeded from CSV, operator-tunable
  — same `origin` split as other [config](SCHEMA.md#config--tunable-policy-seed--operator-overrides) tables). Starting points: bad-char > 0.01,
  diacritic density < 0.02, < 100 chars/non-blank page → OCR.
- **Verdict → `silver.document_text`** (`authority`, `extract_engine`, `extract_confidence`, `is_binding`,
  `needs_review`). A born-digital pass that fails the gate is kept non-binding and the source file is
  flagged `needs_ocr`; the `OcrAll` batch fills the `ocr_extractive` text later (OCR is no longer inline).
- **Placeholder source text** (`Đang cập nhật file đính kèm`, empty converted body) is classified as
  source-unavailable, kept non-binding, and schedules a bounded Fetch recheck.
- **Supplement/form-only text** (for example appendix report forms) is kept non-binding; Normalize also
  rechecks binding text quality before building sections so old bad rows do not become chunks.
- **Congbao PDF page furniture** (`CÔNG BÁO/Số .../Ngày ...`, form feeds, adjacent page numbers) is
  stripped immediately after go-fitz extraction so Silver sections and Gold chunks do not index
  gazette headers.

## OCR engine & batch — `OcrAll` (mirrors `EmbedAll`)

OCR is a **batch backfill**, the twin of bulk embedding. Extract never OCRs inline; it flags gate-failed
scans, and `OcrAll` OCRs every flagged file in one job.

- **Engine** `ocr.engine`: `documentai | auto | local | kaggle`. **`documentai`** (the default) uses
  **GCP Document AI** Enterprise OCR (`pkg/extract/docai/`) — processor `banhmi-ocr` in
  `asia-southeast1`, auth via ADC, input/output cached in a GCS bucket. `auto` → **Kaggle GPU** when
  `KAGGLE_API_TOKEN` is set (and ≥ `ocr.kaggle.min_batch` scans), else **local EasyOCR (CPU)**.
- **EasyOCR runs as a Python tool** (`tools/easyocr_ocr.py`): render pages with PyMuPDF
  (300 DPI) → `EasyOCR(['vi'], batch_size=32, paragraph=True)` → text + per-box confidence. The same core
  logic is embedded as the Kaggle kernel (`go:embed`) with dual-T4 sharding.
- **Kaggle batch** (`pkg/rag/ocr/kagglebatch`, reuses `danny.vn/kaggle`): upload the scan **PDFs** as a
  dataset → push the EasyOCR kernel → poll (heartbeat) → download **plain `ocr.jsonl`** keyed by `sha256`
  (`{sha256, pages, text, confidence}`; uncompressed — OCR text is KB–MB, not the GB an embedding dump is)
  → auto-delete kernel + input dataset.
- **Persist:** `UpsertDocumentText` → `authority='ocr_extractive'`, `is_binding=FALSE`,
  `extract_engine='easyocr/<ver>'`, `extract_confidence`, `needs_review` per the gate. OCR text is never
  the sole source of binding legal text.
- **OCR floor (serving):** a document with NO binding text at all still serves its best *usable*
  non-binding transcription — Normalize falls back to it (same quality bar as binding selection, so
  gate-failed extractions stay rejected) and Index chunks it. `is_binding` stays FALSE; every hit is
  badged non-binding/needs-review through text provenance. Unusable-OCR docs remain unindexed (disclosed
  via `quality_gaps`).
- **Trigger:** `OcrAll` activity, run via `cmd/pipeline -ocr-all` (twin of `-embed-all`).

**Bake-off note (why EasyOCR, not a VLM):** on real SBV scans, VLM doc-parsers — PaddleOCR-VL,
DeepSeek-OCR, Qwen2.5-VL — drop regions (e.g. the document number, the national heading) and make
*plausible-but-wrong* substitutions (`xác thực` → `các thực`), which for legal text is worse than nothing.
EasyOCR's classic detect+recognize transcribes everything, fixes the old Tesseract diacritic errors, and
never hallucinates — at ~5 s/page on GPU vs ~30 s/page for a VLM.

## Deferred OCR enhancement (MVP2)

Gemma 4 E4B is deferred to MVP2. It must not be wired, evaluated, or deployed for MVP1 extraction.
If reopened, it is an OCR enhancer, not the first OCR pass. It receives only the page/crop image, the
EasyOCR draft, and task-specific instructions.

- **Trigger:** low-confidence EasyOCR words/lines, missing legal markers, bad document number/date,
  parser collapse, or OCR-vs-image QA failures.
- **Image input:** start from EasyOCR boxes; merge hard words into line/span crops; retry on padded
  high-DPI crops before sending unresolved spans to Gemma.
- **Output:** strict JSON with task, page, bbox/span, suggestion, confidence reason, and evidence text.
- **Promotion:** deterministic metadata corrections may auto-promote when corroborated by source
  filename/metadata or another OCR pass. Body-span suggestions remain `needs_review` evidence unless a
  later human or deterministic rule verifies them.
- **Forbidden:** full legal body replacement, table replacement, legal-effect truth, and any suggestion
  without page/crop provenance.

## Supporting schema

- `config.setting` (shipped) — key/value gate thresholds (`origin` seed/user), seeded from `deploy/seed/`.
- **`needs_ocr` selection** — `OcrAll` collects scans whose born-digital pass failed the gate, derived
  from existing signals (`silver.document_text` non-binding + `needs_review`, no `ocr_extractive` row yet,
  with an `original_scan`/PDF `bronze.raw_file`). No new table beyond a query is required.

Figure extraction/OCR is out of MVP scope for the current regulatory corpus. Add it only after a
specific corpus need is demonstrated and the design is approved.

Follow repo conventions: surrogate `BIGINT` PK, natural-key `UNIQUE`, FKs within the same schema only.

## MVP1 vs deferred

| In MVP1 | Deferred |
|---------|----------|
| go-fitz DOCX/HTML/PDF + LibreOffice DOC→DOCX bridge | footnotes, list labels, OMML math, `pStyle` headings |
| Born-digital PDF gate: go-fitz → gate → flag `needs_ocr` | figure extraction/OCR |
| Per-PDF two-phase gate + `config.setting` | PP-StructureV3 table reconstruction from images |
| **Document AI (default) or EasyOCR (per-jurisdiction lang) batched (`OcrAll`)** for scanned/failed PDF body text | Gemma 4 targeted OCR enhancement |

## Vietnamese gotchas

- **Type0/Identity-H without ToUnicode** and **TCVN3/VNI legacy fonts** → mojibake; the gate catches them
  (low diacritic density, PUA runes) and flags the file for OCR.
- Always **NFC** after extraction (some renderers emit NFD).
- **Multi-column** gazette appendices need reading-order QA — handled by go-fitz (MuPDF) for
  born-digital PDFs, with EasyOCR (`paragraph=True` reading-order grouping) only for scanned/failed files.
