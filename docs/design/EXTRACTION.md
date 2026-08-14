# Extraction

Extraction turns official files and source payloads into normalized text with provenance. It does
not decide what the law means. See [SOURCES](SOURCES.md) for source collection and
[SCHEMA](SCHEMA.md) for the stored records.

Two terms matter throughout this document:

- **Binding text** is a source transcription that passed the deterministic quality checks and may
  feed the primary provision tree.
- **OCR text** is a machine transcription of a scan. banhmi stores it as non-binding evidence even
  when it passes the text gate.

## Current pipeline

Extraction and optical character recognition (OCR) are separate stages:

1. `Extract` tries the best available source in this order: main DOCX, usable inline HTML, main
   legacy DOC, then PDF.
2. Each textual result is normalized to Unicode NFC. DOCX, HTML, and legacy DOC results are checked
   for source placeholders or supplement-only content; PDF uses its own extraction gates.
   `Normalize` checks the selected text again.
3. A PDF that cannot be extracted or fails its gates is stored as non-binding, marked
   `needs_review`, and left for `OcrAll`. `Extract` never calls OCR inline.
4. `OcrAll` selects documents with no binding text, OCRs one PDF per document, and writes an
   `ocr_extractive` row with `is_binding=FALSE`.
5. `Normalize` prefers the official VBPL provision tree when one exists. Otherwise it selects usable
   binding text. If a document has no binding text, it may use a usable OCR transcription as an
   explicitly non-binding fallback.

`cmd/pipeline -run-all` runs OCR after extraction and normalizes successful OCR rows again. The
standalone form is:

```bash
go run ./cmd/pipeline -ocr-all
go run ./cmd/pipeline -ocr-all -force -limit 20
```

## Active extractors

| Input | Active path | Recorded engine | Notes |
|---|---|---|---|
| DOCX | `pkg/extract/fitz` (go-fitz/MuPDF) | `mupdf/1` | The pure-Go `extract.DOCX` parser exists, but the pipeline does not call it. |
| Inline HTML | `extract.HTML` (`golang.org/x/net/html`) | `gohtml/1` | Script, style, head, title, and noscript content is omitted. Block boundaries become line breaks. |
| Legacy DOC | LibreOffice headless to DOCX, then go-fitz/MuPDF | `libreoffice+mupdf/1` | Conversion timeout is 120 seconds. |
| PDF | go-fitz/MuPDF, one page at a time | `mupdf/1` | The content and scan-layer gates decide whether the result is binding. |
| Scanned PDF | Vision OCR, local EasyOCR, or Kaggle EasyOCR through `OcrAll` | `vision-ocr/builtin-latest` or EasyOCR engine tag | Always stored as non-binding text. |

go-fitz serializes MuPDF calls because the library's shared native state is not safe for concurrent
access in this integration. MuPDF is an AGPL-licensed dependency. This document does not make a
license-compatibility determination; deployments must meet the applicable license terms.

## PDF gates

A PDF must pass both a file-level check and the common text gate.

### Scan-layer check

`fitz.ScanStats` probes at most 12 pages. When the share of probed pages containing raster images is
at least `extract.pdf.max_scan_image_ratio` (default `0.8`), the extracted text is treated as an
untrusted embedded OCR layer. The file is sent to `OcrAll` even if its text looks fluent.

This check protects languages where an inaccurate embedded OCR layer can pass text-only tests. A
clean OCR result may also show that an apparent parser failure was caused by its input text rather
than the provision parser.

### Text check

`extract.GateConfig.Assess` checks:

- replacement and disallowed control characters;
- minimum letter count;
- excessive whitespace;
- Unicode private-use characters;
- common visible UTF-8 mojibake markers;
- a CP1251 double-encoding signal represented by a run of Cyrillic characters;
- low diacritic density for Vietnam only.

The active operator settings are:

| Setting | Compiled default |
|---|---:|
| `extract.pdf.max_bad_ratio` | `0.01` |
| `extract.pdf.min_diacritic_density` | `0.02` |
| `extract.pdf.min_letters` | `50` |
| `extract.pdf.pass_threshold` | `0.6` |
| `extract.pdf.max_whitespace_ratio` | `0.40` |
| `extract.pdf.max_pua_ratio` | `0.005` |
| `extract.pdf.max_scan_image_ratio` | `0.8` |

These values come from `config.setting`; an absent or invalid value falls back to the compiled
default. Other older `extract.pdf.*` rows remain in the seed file but are not read by
`GateFromSettings`.

The PDF and OCR paths both remove known gazette page furniture, including Vietnamese Công Báo
headers and Indonesian Setneg stamps. Official placeholder text is not treated as an OCR failure; it
schedules a bounded source recheck instead.

## OCR engines

`extract.ocr.engine` accepts these values:

| Value | Behavior |
|---|---|
| `documentai` | Google Cloud Vision `images:annotate` with `DOCUMENT_TEXT_DETECTION` and model `builtin/latest`. The name is retained for configuration compatibility; this path no longer calls Document AI. |
| `local` | Local CPU EasyOCR through `tools/easyocr_ocr.py`. |
| `kaggle` | One Kaggle batch using the embedded EasyOCR kernel in `pkg/rag/ocr/kagglebatch`. |
| `auto` or empty | Kaggle when `KAGGLE_API_TOKEN` is set; otherwise local EasyOCR. |

The compiled default is `auto`. The reference production pipeline script selects `documentai`, so
its effective engine is Vision OCR. A local deployment follows its own configuration or the compiled
fallback.

`OcrAll` does not consult `extract.ocr.kaggle.min_batch`; `auto` selects Kaggle solely from the token.
Legacy `extract.ocr.documentai.processor` and `bucket` entries do not map to the current config type
and have no effect. Vision cache storage uses `storage.s3_data_bucket`.

### Vision OCR

Vision renders each PDF page to JPEG at the configured DPI and sends one request per page. Defaults
inside the Vision client are 200 DPI, eight concurrent requests, and 600 requests per minute. The
general OCR config defaults to 300 DPI; when passed by `cmd/pipeline`, that value overrides the
Vision default.

Authentication uses Google Application Default Credentials. Off Google Cloud, the deployment passes
`GOOGLE_APPLICATION_CREDENTIALS` for a service-account key. Final text is cached under
`data/<jurisdiction>/ocr/<sha256>.txt` and may be mirrored to
`ocr/<sha256>.txt` in `BANHMI_S3_DATA_BUCKET`.

### EasyOCR

The local helper and Kaggle kernel use the jurisdiction registry's language (`vi`, `en`, `id`, `th`,
or the configured fallback), render with PyMuPDF, recognize boxes with EasyOCR, and reconstruct
lines by box position. Defaults are 300 DPI and recognition batch size 32. OCR output can omit or
misrecognize text; confidence and `needs_review` are therefore preserved as evidence metadata.

The Kaggle path uploads PDFs named by SHA-256, writes plain `ocr.jsonl`, and streams results back by
hash. It deduplicates identical files before OCR and fans one result out to every document that uses
that hash. Unless `KeepArtifacts` is set by a caller, the input dataset and kernel are deleted on
both success and failure.

## Selection and provenance

`OcrAll` searches all source observations linked through `silver.document_alias`, not only the
document's primary source observation. It prefers `original_scan` over `main` when choosing the PDF.
Without `-force`, a document that already has an `ocr_extractive` row is skipped.

Each `silver.document_text` row retains the source, source-file hash, normalized-text hash,
authority, extraction engine, confidence when available, binding flag, and review flag. OCR never
changes an earlier source row or becomes binding automatically.

Source-specific safeguards include:

- an `original_scan` does not replace a born-digital DOCX or HTML result that merely needs review;
- OJK FAQ pages are not marked as binding regulation text;
- supplement-only forms remain non-binding;
- normalization rechecks candidate text before it creates sections.

## Deferred or retired designs

| Status | Design |
|---|---|
| Deferred | Gemma 4 E4B targeted OCR correction, figure OCR, and image-table reconstruction. Any future generative correction must retain page or crop provenance and must not silently replace legal body text. |
| Retired | MarkItDown as the main document extractor. |
| Retired | OCRmyPDF/Tesseract as the active OCR path. `OCRClient` and legacy config fields remain in the tree but are not wired into `OcrAll`. |
| Rejected for the current PDF path | `pdfcpu` as a text extractor; `unipdf` because it requires a commercial license; and parsers that did not preserve the tested corpus text. |

Figure extraction is outside the current scope. Add it only for a demonstrated corpus need and with
an approved provenance design.
