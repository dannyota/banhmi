# Extraction

Turning a document's files into clean, citable Vietnamese legal text. Born-digital extraction stays
deterministic; scanned or failed PDFs use **EasyOCR** (Apache-2.0), run as a **batch** (`OcrAll`) on the
local CPU or a Kaggle GPU. Gemma 4 E4B OCR enhancement is MVP2, not current work. See [SOURCES](SOURCES.md)
for what we ingest, [SCHEMA](SCHEMA.md) for the tables.

## Principles

- **No AI as canonical parser.** Born-digital text is read deterministically. MVP1 OCR is extractive
  **EasyOCR** (classic detect + recognize, Vietnamese) — it transcribes every region and never invents or
  drops text; no generative OCR is wired into the current extraction path.
- **Permissive parser path** (Apache-2.0 / BSD / MIT). EasyOCR is Apache-2.0 (PyTorch is BSD). No GPL
  (poppler) or AGPL (MuPDF/`go-fitz`) PDF parsers.
- **Per-file quality gate.** PDFs are not assumed uniform — each file is checked: extract vs OCR.
- **NFC-normalized, never diacritic-folded.** "an toàn" must never become "an toan".
- A document may carry **many files**: prefer official **DOCX**, then official **HTML**, then
  official **DOC**, then **PDF/OCR**. All three — congbao, vbpl, and sbv_hanoi — are authoritative
  government sources; source + hashes carry provenance.
- **Do not transform when the source already did it.** For VBPL docs, the official provision tree
  (`doc/provision/tree/{id}`) feeds RAG sections first; MarkItDown/OCR remains evidence and fallback.

## Engine selection (per file)

| Input | Engine | License | Notes |
|-------|--------|---------|-------|
| DOCX | **local MarkItDown** | MIT + permissive deps | emits GFM Markdown |
| DOC | **LibreOffice headless → PDF → MarkItDown** | MPL/LGPL deps + MIT MarkItDown | legacy OLE `.doc`; deterministic fallback after HTML |
| HTML body | **local MarkItDown** | MIT + permissive deps | for vbpl body HTML |
| PDF, born-digital | **local MarkItDown** (`pdfminer.six`) | MIT | quality-gated before binding use |
| Scanned / image-only | **EasyOCR (`vi`)** | Apache-2.0 | extractive, GPU-capable; **batched** (`OcrAll`), not inline; never the sole source of binding text |

MarkItDown is installed in the Go app container and invoked locally per document. **OCR is no longer
inline:** PDF assessment is Go-side (run MarkItDown, apply the content gate); a file that fails is kept
non-binding and **flagged `needs_ocr`**, and the separate `OcrAll` batch (below) does all OCR in one pass.
Rejected: `pdfcpu` (not a text extractor), `unipdf` (commercial), `rsc.io/pdf` / `ledongthuc` / `dslipak`
(CMap ceiling → garbled diacritics). OCRmyPDF/Tesseract was the previous OCR engine; EasyOCR replaced it
(better Vietnamese diacritics, complete transcription, no hallucination — see the bake-off note below).

## MarkItDown path

MarkItDown is the only Markdown converter for DOCX, HTML bodies, born-digital PDFs, and LibreOffice
rendered legacy DOC PDFs. The Go worker normalizes output to NFC, runs the same quality gate, and
records engine/source/checksum provenance in `silver.document_text`.

- **Version `0.1.6`** (PDF via pdfminer.six): fixes a PDF memory leak (batch-relevant), aligns PDF tables,
  and handles partial-numbered lists + deeply-nested HTML. Azure DocIntel / Content Understanding
  converters are cloud → intentionally unused; the in-package OCR layer is unused too (OCR stays the
  separate EasyOCR batch).
- **Known limit — no layout geometry:** pdfminer.six PDF text drops x/y coordinates, so heading /
  marginal-note association can be ambiguous (e.g. Malaysian Act section titles, see
  [`MALAYSIA.md`](MALAYSIA.md)). MarkItDown has no local knob for this. Where structure fidelity needs
  geometry, run a separate **pdfplumber (MIT)** coordinate pass *alongside* MarkItDown — MarkItDown stays
  the canonical text→Markdown converter; pdfplumber only supplies positions for the structure parser.
  **pdfplumber is already bundled by MarkItDown 0.1.6** (transitive dep), so this pass adds no new dependency.
- **Cascade:** DOCX → HTML body → DOC rendered to PDF → source PDF → OCR. Source-specific file flags
  such as VBPL `relatedType` never override this order.
- **HTML:** persist and try only text-bearing bodies. VBPL can return an empty `*_content.html` shell,
  so `hasContent`/byte length is not enough. The MarkItDown helper forces `charset=utf-8` (via
  `StreamInfo`) for HTML inputs: vbpl serves charset-less bodies and MarkItDown's auto-detection
  otherwise mis-guesses Vietnamese UTF-8 as cp1251/cp1252 (a `<meta charset>`/BOM hint is **not**
  honoured — it must be the explicit `StreamInfo` charset).
- **Mojibake gate:** the content gate hard-fails on Cyrillic (`U+0400–U+04FF`, the cp1251 double-encode)
  in addition to the existing PUA/UTF-8-marker checks — Latin-script legal text never contains Cyrillic.
  `corpus_status`/`quality_gaps` flag it too.
- **DOC:** legacy OLE `.doc` is rendered by LibreOffice headless with an isolated profile, then the
  temporary PDF goes through MarkItDown and the same quality gate.
- **DOCX/HTML:** MarkItDown output becomes binding only if the standard text gate passes. If it extracts
  non-binding `needs_review` text, keep that row and do not OCR an `original_scan` PDF over it; the scan
  remains provenance and OCR is only for missing/failed text paths.
- **PDF:** run simple Go-side assessment: MarkItDown conversion plus the content gate. Passing PDFs are
  accepted immediately. Conversion failure or gate failure flags the file `needs_ocr` for the `OcrAll`
  batch (no inline OCR).

## PDF quality gate (per file)

Two deterministic phases; failing either flags the file for OCR.

- **Phase 1 — extraction assessment (Go):** try local MarkItDown and classify explicit source
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
  stripped immediately after MarkItDown conversion so Silver sections and Gold chunks do not index
  gazette headers.

## OCR engine & batch — EasyOCR, `OcrAll` (mirrors `EmbedAll`)

OCR is a **batch backfill**, the twin of bulk embedding. Extract never OCRs inline; it flags gate-failed
scans, and `OcrAll` OCRs every flagged file in one job.

- **Engine** `ocr.engine`: `auto | local | kaggle`. `auto` → **Kaggle GPU** when `KAGGLE_API_TOKEN` is set
  (and ≥ `ocr.kaggle.min_batch` scans), else **local EasyOCR (CPU)**. Mirrors `embed.engine`.
- **EasyOCR runs as a Python tool** (`tools/easyocr_ocr.py`, like MarkItDown): render pages with PyMuPDF
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
- **Trigger:** `OcrAllWorkflow` / `OcrAll` activity (external task queue, heartbeated), run via
  `cmd/worker -ocr-all` (twin of `-embed-all`).

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

## New schema (design)

- `config.setting` — key/value gate thresholds (`origin` seed/user), seeded from `deploy/seed/`.
- **`needs_ocr` selection** — `OcrAll` collects scans whose born-digital pass failed the gate, derived
  from existing signals (`silver.document_text` non-binding + `needs_review`, no `ocr_extractive` row yet,
  with an `original_scan`/PDF `bronze.raw_file`). No new table beyond a query is required.

Figure extraction/OCR is out of MVP scope for the current regulatory corpus. Add it only after a
specific corpus need is demonstrated and the design is approved.

Follow repo conventions: surrogate `BIGINT` PK, natural-key `UNIQUE`, FKs within the same schema only.

## MVP1 vs deferred

| In MVP1 | Deferred |
|---------|----------|
| MarkItDown DOCX/HTML/PDF + LibreOffice DOC bridge → Markdown | footnotes, list labels, OMML math, `pStyle` headings |
| Born-digital PDF gate: MarkItDown → gate → flag `needs_ocr` | figure extraction/OCR |
| Per-PDF two-phase gate + `config.setting` | PP-StructureV3 table reconstruction from images |
| **EasyOCR (`vi`) batched (`OcrAll`), local-CPU or Kaggle-GPU** for scanned/failed PDF body text | Gemma 4 targeted OCR enhancement |

## Vietnamese gotchas

- **Type0/Identity-H without ToUnicode** and **TCVN3/VNI legacy fonts** → mojibake; the gate catches them
  (low diacritic density, PUA runes) and flags the file for OCR.
- Always **NFC** after extraction (some renderers emit NFD).
- **Multi-column** gazette appendices need reading-order QA — handled by MarkItDown/pdfminer for
  born-digital PDFs, with EasyOCR (`paragraph=True` reading-order grouping) only for scanned/failed files.
