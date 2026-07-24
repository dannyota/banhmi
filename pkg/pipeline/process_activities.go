package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"danny.vn/banhmi/pkg/base/jurisdiction"
	"danny.vn/banhmi/pkg/extract"
	fitzext "danny.vn/banhmi/pkg/extract/fitz"
	"danny.vn/banhmi/pkg/ingest"
	dbbronze "danny.vn/banhmi/pkg/store/bronze"
	dbingest "danny.vn/banhmi/pkg/store/ingest"
	dbsilver "danny.vn/banhmi/pkg/store/silver"
)

const (
	sourceContentRecheckDelay     = 24 * time.Hour
	sourceContentRecheckFileDelay = time.Minute
	sourceContentMaxRechecks      = 5
	congbaoFallbackMinAge         = 14 * 24 * time.Hour
)

// Extract reads a completed document's best official text source from bronze,
// turns it into NFC-normalized text with a deterministic engine, gates the
// result for quality, and writes silver.document + silver.document_text with
// full provenance.
//
// Engine selection:
//   - DOCX: go-fitz (MuPDF); failed conversion or failed quality gate falls
//     through to the next source in the cascade.
//   - HTML: pure-Go HTML-to-text extractor (extract.HTML); same fallthrough.
//   - Legacy DOC: LibreOffice converts to DOCX, then go-fitz extracts. Tried
//     after HTML and before source PDF/OCR.
//   - PDF: go-fitz text extraction checked with the content gate (tunable via
//     config.setting). Gate failure routes to OCR.
//   - No file: document recorded and flagged needs_review.
func (a *Activities) Extract(ctx context.Context, p StageParams) (ExtractResult, error) {
	log := a.log
	now := time.Now().UTC()

	fd, err := a.ledger.GetFetchDocByID(ctx, p.FetchDocID)
	if err != nil {
		return ExtractResult{}, fmt.Errorf("get fetch_doc %d: %w", p.FetchDocID, err)
	}
	sd, err := a.bronze.SourceDocumentByExternalID(ctx, dbbronze.SourceDocumentByExternalIDParams{
		Source: fd.Source, ExternalID: fd.ExternalID,
	})
	if err != nil {
		return ExtractResult{}, fmt.Errorf("source_document %s/%s: %w", fd.Source, fd.ExternalID, err)
	}
	files, err := a.bronze.ListRawFilesByDocument(ctx, sd.ID)
	if err != nil {
		return ExtractResult{}, fmt.Errorf("list raw files %d: %w", sd.ID, err)
	}
	payloads, err := a.bronze.ListRawPayloadsByDocument(ctx, sd.ID)
	if err != nil {
		return ExtractResult{}, fmt.Errorf("list raw payloads %d: %w", sd.ID, err)
	}

	var reviewRes ExtractResult
	var lastErr error
	sawSourceUnavailable := false
	sawBornDigitalReview := false

	// --- main DOCX (born-digital file; preferred) ---
	if docx := pickFile(files, "docx", "main"); docx != nil && docx.StoragePath != nil {
		res, err := a.extractDOCX(ctx, fd.Source, fd.ExternalID, sd, docx, now)
		switch {
		case err != nil:
			lastErr = err
			log.Warn("extract: DOCX failed, trying next source", "doc", fd.ExternalID, "err", err)
		case res.NeedsReview:
			sawSourceUnavailable = sawSourceUnavailable || res.SourceUnavailable
			sawBornDigitalReview = true
			reviewRes = res
			log.Warn("extract: DOCX needs review, trying next source", "doc", fd.ExternalID,
				"confidence", res.Confidence, "source_unavailable", res.SourceUnavailable)
		default:
			return res, nil
		}
	}

	// --- inline HTML body (vbpl transcription; first fallback for file-poor docs) ---
	if p := pickPayload(payloads, "content_html"); p != nil && p.Content != nil && usableHTMLPayload(*p.Content) {
		res, err := a.extractHTML(ctx, fd.Source, fd.ExternalID, sd, *p.Content, now)
		switch {
		case err != nil:
			lastErr = err
			log.Warn("extract: HTML failed, trying next source", "doc", fd.ExternalID, "err", err)
		case res.NeedsReview:
			sawSourceUnavailable = sawSourceUnavailable || res.SourceUnavailable
			sawBornDigitalReview = true
			reviewRes = res
			log.Warn("extract: HTML needs review, trying next source", "doc", fd.ExternalID,
				"confidence", res.Confidence, "source_unavailable", res.SourceUnavailable)
		default:
			return res, nil
		}
	}

	// --- legacy DOC (LibreOffice DOC-to-DOCX, then go-fitz) ---
	if doc := pickFile(files, "doc", "main"); doc != nil && doc.StoragePath != nil {
		res, err := a.extractDOC(ctx, fd.Source, fd.ExternalID, sd, doc, now)
		switch {
		case err != nil:
			lastErr = err
			log.Warn("extract: DOC failed, trying next source", "doc", fd.ExternalID, "err", err)
		case res.NeedsReview:
			sawSourceUnavailable = sawSourceUnavailable || res.SourceUnavailable
			sawBornDigitalReview = true
			reviewRes = res
			log.Warn("extract: DOC needs review, trying next source", "doc", fd.ExternalID,
				"confidence", res.Confidence, "source_unavailable", res.SourceUnavailable)
		default:
			return res, nil
		}
	}

	// --- PDF (main born-digital or scanned original) ---
	// original_scan is official evidence and an OCR fallback, not a replacement
	// for a born-digital DOCX/HTML text row that already exists but needs review.
	if pdf := pickPDFForExtraction(files, sawBornDigitalReview); pdf != nil && pdf.StoragePath != nil {
		res, err := a.extractPDF(ctx, fd.Source, fd.ExternalID, sd, pdf, now)
		if err != nil {
			return res, err
		}
		if res.SourceUnavailable {
			a.scheduleSourceContentRecheck(ctx, fd.ID, "official source placeholder/empty content", now)
			a.discoverCongbaoFallback(ctx, fd, sd, "official source placeholder/empty content", now)
		}
		return res, nil
	}

	if reviewRes.DocumentID != 0 {
		if sawBornDigitalReview {
			log.Warn("extract: skipping original_scan OCR after born-digital text needs review",
				"doc", fd.ExternalID, "confidence", reviewRes.Confidence,
				"source_unavailable", reviewRes.SourceUnavailable)
		}
		if sawSourceUnavailable {
			a.scheduleSourceContentRecheck(ctx, fd.ID, "official source placeholder/empty content", now)
			a.discoverCongbaoFallback(ctx, fd, sd, "official source placeholder/empty content", now)
			reviewRes.SourceUnavailable = true
		}
		return reviewRes, nil
	}
	if lastErr != nil {
		return ExtractResult{}, fmt.Errorf("extract %s: all candidate sources failed: %w", fd.ExternalID, lastErr)
	}

	// No extractable file found.
	docID, derr := a.upsertSilverDocument(ctx, sd, "", now)
	if derr != nil {
		return ExtractResult{}, derr
	}
	log.Warn("extract: no DOCX, DOC, HTML, or PDF found", "doc", fd.ExternalID)
	a.discoverCongbaoFallback(ctx, fd, sd, "no extractable official file", now)
	return ExtractResult{DocumentID: docID, NeedsReview: true}, nil
}

// extractHTML runs the inline-HTML-body path (vbpl's transcribed born-digital body,
// stored as the content_html payload) and writes silver.document_text under the
// transcription_html authority. No file download or OCR is involved.
func (a *Activities) extractHTML(ctx context.Context, source, externalID string, sd dbbronze.BronzeSourceDocument, body string, now time.Time) (ExtractResult, error) {
	text, engine, err := a.htmlToText(ctx, externalID, body)
	if err != nil {
		return ExtractResult{}, err
	}
	confidence, ok, sourceUnavailable := assessConvertedText(text)

	docID, err := a.upsertSilverDocument(ctx, sd, text, now)
	if err != nil {
		return ExtractResult{}, err
	}

	srcHash := sha256Hex(body)
	verbatim := sha256Hex(text)
	if _, err := a.silver.UpsertDocumentText(ctx, dbsilver.UpsertDocumentTextParams{
		DocumentID:        docID,
		Authority:         "transcription_html",
		Source:            source,
		Markdown:          &text,
		SourceFileSha256:  &srcHash,
		VerbatimSha256:    &verbatim,
		IsBinding:         ok,
		ExtractEngine:     strPtr(engine),
		ExtractConfidence: pgtype.Float8{Float64: confidence, Valid: true},
		NeedsReview:       !ok,
		CreatedAt:         now,
	}); err != nil {
		return ExtractResult{}, fmt.Errorf("upsert document_text %s: %w", externalID, err)
	}
	return ExtractResult{
		DocumentID:        docID,
		Engine:            engine,
		Confidence:        confidence,
		NeedsReview:       !ok,
		SourceUnavailable: sourceUnavailable,
	}, nil
}

// pickPayload returns the first raw_payload of the given kind, or nil.
func pickPayload(ps []dbbronze.BronzeRawPayload, kind string) *dbbronze.BronzeRawPayload {
	for i := range ps {
		if ps[i].Kind == kind {
			return &ps[i]
		}
	}
	return nil
}

// signerFromDetailMeta reads the document's người ký (signer) from the preserved
// VBPL detail metadata (the bronze detail_json raw payload), if present. Returns
// nil when the payload is absent (e.g. non-VBPL sources, or pre-detail_json docs)
// or carries no signer, so it is safe to call for every document.
func (a *Activities) signerFromDetailMeta(ctx context.Context, sourceDocID int64) *string {
	payloads, err := a.bronze.ListRawPayloadsByDocument(ctx, sourceDocID)
	if err != nil {
		return nil
	}
	p := pickPayload(payloads, "detail_json")
	if p == nil || p.Content == nil {
		return nil
	}
	var d struct {
		DocumentIssues []struct {
			PersonName string `json:"personName"`
		} `json:"documentIssues"`
	}
	if err := json.Unmarshal([]byte(*p.Content), &d); err != nil {
		return nil
	}
	for _, di := range d.DocumentIssues {
		if s := strings.TrimSpace(di.PersonName); s != "" {
			return &s
		}
	}
	return nil
}

func assessConvertedText(text string) (confidence float64, ok bool, sourceUnavailable bool) {
	if extract.SourceUnavailable(text) {
		return 0, false, true
	}
	if supplementOnlyText(text) {
		return 0.2, false, false
	}
	confidence, ok = extract.Assess(text)
	return confidence, ok, false
}

func supplementOnlyText(text string) bool {
	meaningful := 0
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "|") {
			continue
		}
		meaningful++
		line = strings.TrimSpace(strings.Trim(line, "*_# "))
		folded := strings.ToLower(line)
		if strings.HasPrefix(folded, "điều ") ||
			strings.HasPrefix(folded, "chương ") ||
			strings.HasPrefix(folded, "thông tư") ||
			strings.HasPrefix(folded, "nghị định") ||
			strings.HasPrefix(folded, "quyết định") {
			return false
		}
		if strings.HasPrefix(folded, "phụ lục") ||
			strings.HasPrefix(folded, "phu luc") ||
			strings.HasPrefix(folded, "mẫu số") ||
			strings.HasPrefix(folded, "mau so") ||
			strings.Contains(folded, "báo cáo tình hình") ||
			strings.Contains(folded, "bao cao tinh hinh") {
			return true
		}
		if meaningful >= 8 {
			return false
		}
	}
	return false
}

// htmlToText converts an inline HTML body to NFC-normalized plain text via the
// pure-Go HTML extractor (extract.HTML). The body is already UTF-8 (stored in
// bronze), so no charset sniffing is needed.
func (a *Activities) htmlToText(_ context.Context, externalID, body string) (string, string, error) {
	text, err := extract.HTML(body)
	if err != nil {
		return "", "", fmt.Errorf("html extract %s: %w", externalID, err)
	}
	return text, "gohtml/1", nil
}

// docxToText converts DOCX bytes to NFC-normalized text via go-fitz (MuPDF).
func (a *Activities) docxToText(_ context.Context, externalID string, data []byte) (string, string, error) {
	text, err := fitzext.ExtractTextFromBytes(data, ".docx")
	if err != nil {
		return "", "", fmt.Errorf("fitz docx %s: %w", externalID, err)
	}
	return extract.Normalize(text), "mupdf/1", nil
}

// docToText converts legacy OLE DOC bytes by writing to a temp file, converting
// to DOCX with LibreOffice, then extracting text with go-fitz (MuPDF).
func (a *Activities) docToText(_ context.Context, externalID string, data []byte) (string, string, error) {
	tmpDir, err := os.MkdirTemp("", "banhmi-doc-convert-*")
	if err != nil {
		return "", "", fmt.Errorf("create doc temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	docPath := filepath.Join(tmpDir, "input.doc")
	if err := os.WriteFile(docPath, data, 0o600); err != nil {
		return "", "", fmt.Errorf("write doc temp file %s: %w", externalID, err)
	}

	docxPath, err := fitzext.ConvertDOCToDocx(docPath, tmpDir)
	if err != nil {
		return "", "", fmt.Errorf("doc to docx %s: %w", externalID, err)
	}

	text, err := fitzext.ExtractText(docxPath)
	if err != nil {
		return "", "", fmt.Errorf("fitz docx (from doc) %s: %w", externalID, err)
	}
	return extract.Normalize(cleanPDFMarkdownNoise(text)), "libreoffice+mupdf/1", nil
}

// extractDOCX runs the DOCX extraction path and writes silver.document_text.
func (a *Activities) extractDOCX(ctx context.Context, source, externalID string, sd dbbronze.BronzeSourceDocument, docx *dbbronze.BronzeRawFile, now time.Time) (ExtractResult, error) {
	log := a.log

	if err := a.ensureLocalFile(ctx, *docx.StoragePath); err != nil {
		return ExtractResult{}, fmt.Errorf("ensure docx %s: %w", *docx.StoragePath, err)
	}
	data, err := os.ReadFile(filepath.Join(a.storageDir, *docx.StoragePath))
	if err != nil {
		return ExtractResult{}, fmt.Errorf("read docx %s: %w", *docx.StoragePath, err)
	}
	text, engine, err := a.docxToText(ctx, externalID, data)
	if err != nil {
		return ExtractResult{}, err
	}
	confidence, ok, sourceUnavailable := assessConvertedText(text)

	docID, err := a.upsertSilverDocument(ctx, sd, text, now)
	if err != nil {
		return ExtractResult{}, err
	}

	verbatim := sha256Hex(text)
	if _, err := a.silver.UpsertDocumentText(ctx, dbsilver.UpsertDocumentTextParams{
		DocumentID:        docID,
		Authority:         "gazette_borndigital",
		Source:            source,
		RawFileID:         &docx.ID,
		Markdown:          &text,
		SourceFileSha256:  docx.Sha256,
		VerbatimSha256:    &verbatim,
		IsBinding:         ok,
		ExtractEngine:     strPtr(engine),
		ExtractConfidence: pgtype.Float8{Float64: confidence, Valid: true},
		NeedsReview:       !ok,
		CreatedAt:         now,
	}); err != nil {
		return ExtractResult{}, fmt.Errorf("upsert document_text %d: %w", docID, err)
	}

	log.Info("extracted DOCX", "doc", externalID,
		"chars", len([]rune(text)), "confidence", confidence, "ok", ok)

	return ExtractResult{
		DocumentID:        docID,
		Engine:            engine,
		Confidence:        confidence,
		NeedsReview:       !ok,
		SourceUnavailable: sourceUnavailable,
	}, nil
}

// extractDOC runs the legacy DOC extraction path and writes silver.document_text.
func (a *Activities) extractDOC(ctx context.Context, source, externalID string, sd dbbronze.BronzeSourceDocument, doc *dbbronze.BronzeRawFile, now time.Time) (ExtractResult, error) {
	log := a.log

	if err := a.ensureLocalFile(ctx, *doc.StoragePath); err != nil {
		return ExtractResult{}, fmt.Errorf("ensure doc %s: %w", *doc.StoragePath, err)
	}
	data, err := os.ReadFile(filepath.Join(a.storageDir, *doc.StoragePath))
	if err != nil {
		return ExtractResult{}, fmt.Errorf("read doc %s: %w", *doc.StoragePath, err)
	}
	text, engine, err := a.docToText(ctx, externalID, data)
	if err != nil {
		return ExtractResult{}, err
	}
	confidence, ok, sourceUnavailable := assessConvertedText(text)

	docID, err := a.upsertSilverDocument(ctx, sd, text, now)
	if err != nil {
		return ExtractResult{}, err
	}

	verbatim := sha256Hex(text)
	if _, err := a.silver.UpsertDocumentText(ctx, dbsilver.UpsertDocumentTextParams{
		DocumentID:        docID,
		Authority:         "gazette_borndigital",
		Source:            source,
		RawFileID:         &doc.ID,
		Markdown:          &text,
		SourceFileSha256:  doc.Sha256,
		VerbatimSha256:    &verbatim,
		IsBinding:         ok,
		ExtractEngine:     strPtr(engine),
		ExtractConfidence: pgtype.Float8{Float64: confidence, Valid: true},
		NeedsReview:       !ok,
		CreatedAt:         now,
	}); err != nil {
		return ExtractResult{}, fmt.Errorf("upsert document_text %d: %w", docID, err)
	}

	log.Info("extracted DOC", "doc", externalID,
		"chars", len([]rune(text)), "confidence", confidence, "ok", ok)

	return ExtractResult{
		DocumentID:        docID,
		Engine:            engine,
		Confidence:        confidence,
		NeedsReview:       !ok,
		SourceUnavailable: sourceUnavailable,
	}, nil
}

type pdfExtractionAssessment struct {
	text              string
	engine            string
	gate              extract.AssessResult
	extractable       bool
	sourceUnavailable bool
	reason            string
}

// extractPDF runs the PDF extraction path with a Go-side assessment and content
// gate, then routes failed cases to local OCR.
func (a *Activities) extractPDF(ctx context.Context, source, externalID string, sd dbbronze.BronzeSourceDocument, pdf *dbbronze.BronzeRawFile, now time.Time) (ExtractResult, error) {
	log := a.log

	gate, err := a.loadGate(ctx)
	if err != nil {
		// Config load failing must not block extraction; fall back to defaults.
		log.Warn("extract: failed to load gate config, using defaults", "err", err)
		gate = extract.DefaultGate()
	}

	if err := a.ensureLocalFile(ctx, *pdf.StoragePath); err != nil {
		return ExtractResult{}, fmt.Errorf("ensure pdf %s: %w", *pdf.StoragePath, err)
	}
	absPath := filepath.Join(a.storageDir, *pdf.StoragePath)
	assessment := a.assessPDFExtraction(ctx, externalID, absPath, gate)

	if assessment.sourceUnavailable {
		return a.writePDFText(ctx, source, externalID, sd, pdf, assessment.text, 0,
			"gazette_borndigital", assessment.engine, false, true, true, now)
	}

	if assessment.extractable {
		// ojkweb FAQ pages describe a regulation; their PDFs are never the
		// regulation's binding text. Marking them binding blocks the OCR
		// selector (which keys on "no binding text") from repairing the real
		// gazette scan (UU 4/2023 was stuck this way).
		binding := !isOjkwebFAQ(source, externalID)
		return a.writePDFText(ctx, source, externalID, sd, pdf, assessment.text, assessment.gate.Confidence,
			"gazette_borndigital", assessment.engine, binding, false, false, now)
	}

	// OCR is deferred to the OcrAll batch: we track every scan that needs OCR and
	// do them all in one job (local CPU or Kaggle GPU). Record the failed
	// born-digital text as non-binding/needs_review so the doc is tracked; OcrAll
	// fills the ocr_extractive text later, then Normalize/Index continue.
	log.Info("extract: PDF assess/gate failed, deferring to OCR batch",
		"doc", externalID, "engine", assessment.engine, "reason", assessment.reason,
		"confidence", assessment.gate.Confidence)
	return a.writePDFText(ctx, source, externalID, sd, pdf, assessment.text, assessment.gate.Confidence,
		"gazette_borndigital", assessment.engine, false, true, false, now)
}

// isOjkwebFAQ reports an ojkweb FAQ page observation. FAQ pages (and their
// attached PDFs) explain a regulation but are not its text.
func isOjkwebFAQ(source, externalID string) bool {
	return source == "ojkweb" && strings.Contains(strings.ToLower(externalID), "faq")
}

func (a *Activities) assessPDFExtraction(ctx context.Context, externalID, absPath string, gate extract.GateConfig) pdfExtractionAssessment {
	text, engine, err := a.pdfToText(ctx, externalID, absPath)
	if err != nil {
		return pdfExtractionAssessment{engine: engine, reason: err.Error()}
	}
	if extract.OfficialPlaceholder(text) {
		return pdfExtractionAssessment{
			text:              text,
			engine:            engine,
			sourceUnavailable: true,
			reason:            "official source placeholder",
		}
	}
	// A predominantly image-paged PDF is a scan; any text it yielded is an
	// embedded OCR layer. Those layers pass the text-level checks in
	// diacritic-poor languages while carrying heavy misrecognitions, so defer
	// to the OCR batch (Vision) instead of trusting them.
	if probed, imagePages, err := fitzext.ScanStats(absPath, 12); err == nil && gate.ScanLayerSuspect(probed, imagePages) {
		return pdfExtractionAssessment{
			text:   text,
			engine: engine,
			reason: fmt.Sprintf("scan with embedded OCR text layer (%d/%d image pages)", imagePages, probed),
		}
	}
	gateResult := gate.Assess(text)
	if !gateResult.OK {
		return pdfExtractionAssessment{
			text:   text,
			engine: engine,
			gate:   gateResult,
			reason: gateResult.Reason,
		}
	}
	return pdfExtractionAssessment{
		text:        text,
		engine:      engine,
		gate:        gateResult,
		extractable: true,
	}
}

// pdfToText converts a born-digital PDF into NFC-normalized text via go-fitz
// (MuPDF). The congbao page-header noise cleaner still runs on the result.
func (a *Activities) pdfToText(_ context.Context, externalID, absPath string) (string, string, error) {
	text, err := fitzext.ExtractText(absPath)
	if err != nil {
		return "", "mupdf/1", fmt.Errorf("fitz pdf %s: %w", externalID, err)
	}
	return extract.Normalize(cleanPDFMarkdownNoise(text)), "mupdf/1", nil
}

func cleanPDFMarkdownNoise(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.ReplaceAll(text, "\f", "\n")

	lines := strings.Split(text, "\n")
	remove := make([]bool, len(lines))
	header := make([]bool, len(lines))
	for i, line := range lines {
		if isCongbaoPageHeader(line) || isSetnegPageArtifact(line) {
			remove[i] = true
			header[i] = true
		}
	}
	for i, line := range lines {
		if isStandalonePageNumber(line) && hasNearbyGazetteHeader(lines, header, i) {
			remove[i] = true
		}
	}

	var b strings.Builder
	blankRun := 0
	for i, line := range lines {
		if remove[i] {
			continue
		}
		line = strings.TrimRight(line, " \t")
		if strings.TrimSpace(line) == "" {
			blankRun++
			if blankRun > 2 {
				continue
			}
		} else {
			blankRun = 0
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
	}
	return strings.TrimSpace(b.String())
}

func isCongbaoPageHeader(line string) bool {
	line = strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(line))), " ")
	return strings.Contains(line, "công báo") &&
		strings.Contains(line, "số") &&
		strings.Contains(line, "ngày")
}

// setnegSKStampRe matches the Setneg print stamp ("SK No 164024 A") the state
// gazette carries on every page, tolerating OCR digit noise (commas, spaces).
var setnegSKStampRe = regexp.MustCompile(`^SK\s*No[\s.,0-9]{4,12}A?$`)

// isSetnegPageArtifact reports an Indonesian state-gazette page artifact: the
// per-page "PRESIDEN REPUBLIK INDONESIA" letterhead, the "SK No …" print stamp,
// or the "- 52 -" page number. These recur on every gazette page and pollute
// chunks with non-legal noise. Standalone lines only — quoted prose never
// matches (letterhead lines are short; the stamp shape is unmistakable).
func isSetnegPageArtifact(line string) bool {
	line = strings.Join(strings.Fields(strings.TrimSpace(line)), " ")
	if line == "" {
		return false
	}
	if setnegSKStampRe.MatchString(line) {
		return true
	}
	// The page letterhead is bare; the law's own preamble/signature lines are
	// "PRESIDEN REPUBLIK INDONESIA," WITH a trailing comma — never remove those.
	upper := strings.ToUpper(line)
	if len(line) <= 40 && !strings.HasSuffix(line, ",") &&
		strings.Contains(upper, "PRESIDEN") && strings.Contains(upper, "INDONESIA") {
		return true
	}
	// "- 52 -" / "-52-" page numbers (bare digits are handled by
	// isStandalonePageNumber + header proximity).
	if len(line) <= 10 && strings.HasPrefix(line, "-") && strings.HasSuffix(line, "-") {
		inner := strings.TrimSpace(strings.Trim(line, "- "))
		if inner != "" && strings.IndexFunc(inner, func(r rune) bool { return r < '0' || r > '9' }) < 0 {
			return true
		}
	}
	return false
}

func isStandalonePageNumber(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" || len(line) > 4 {
		return false
	}
	for _, r := range line {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func hasNearbyGazetteHeader(lines []string, header []bool, idx int) bool {
	const distance = 3
	for i := idx - 1; i >= 0 && idx-i <= distance; i-- {
		if header[i] {
			return true
		}
		if strings.TrimSpace(lines[i]) != "" {
			break
		}
	}
	for i := idx + 1; i < len(lines) && i-idx <= distance; i++ {
		if header[i] {
			return true
		}
		if strings.TrimSpace(lines[i]) != "" {
			break
		}
	}
	return false
}

// writePDFText upserts silver.document and silver.document_text for a PDF
// extraction result.
func (a *Activities) writePDFText(
	ctx context.Context,
	source, externalID string,
	sd dbbronze.BronzeSourceDocument,
	pdf *dbbronze.BronzeRawFile,
	text string,
	confidence float64,
	authority, engine string,
	isBinding bool,
	needsReview bool,
	sourceUnavailable bool,
	now time.Time,
) (ExtractResult, error) {
	log := a.log

	docID, err := a.upsertSilverDocument(ctx, sd, text, now)
	if err != nil {
		return ExtractResult{}, err
	}

	var md *string
	if text != "" {
		md = &text
	}
	verbatim := sha256Hex(text)
	confPG := pgtype.Float8{Float64: confidence, Valid: confidence > 0}
	if _, err := a.silver.UpsertDocumentText(ctx, dbsilver.UpsertDocumentTextParams{
		DocumentID:        docID,
		Authority:         authority,
		Source:            source,
		RawFileID:         &pdf.ID,
		Markdown:          md,
		SourceFileSha256:  pdf.Sha256,
		VerbatimSha256:    &verbatim,
		IsBinding:         isBinding,
		ExtractEngine:     strPtr(engine),
		ExtractConfidence: confPG,
		NeedsReview:       needsReview,
		CreatedAt:         now,
	}); err != nil {
		return ExtractResult{}, fmt.Errorf("upsert document_text %s: %w", externalID, err)
	}

	log.Info("extracted PDF", "doc", externalID,
		"engine", engine, "authority", authority,
		"chars", len([]rune(text)), "confidence", confidence,
		"is_binding", isBinding, "needs_review", needsReview,
		"source_unavailable", sourceUnavailable)
	return ExtractResult{
		DocumentID:        docID,
		Engine:            engine,
		Confidence:        confidence,
		NeedsReview:       needsReview,
		SourceUnavailable: sourceUnavailable,
	}, nil
}

// loadGate reads gate thresholds from config.setting and returns a GateConfig.
func (a *Activities) loadGate(ctx context.Context) (extract.GateConfig, error) {
	rows, err := a.configQ.ListSettings(ctx)
	if err != nil {
		return extract.DefaultGate(), fmt.Errorf("list settings: %w", err)
	}
	m := make(map[string]string, len(rows))
	for _, r := range rows {
		m[r.Key] = r.Value
	}
	g := extract.GateFromSettings(m)
	// The diacritic-density check is a Vietnamese-specific signal (Vietnamese text
	// is dense with non-ASCII letters). Other jurisdictions are extracted in their
	// own main language (e.g. Malaysia = English), which has ~zero diacritics, so
	// the descriptor enables it only where it applies — the language-neutral
	// checks (replacement chars, PUA/mojibake, length) still gate quality.
	if !a.jur.DiacriticDensityGate {
		g.MinDiacriticDensity = 0
	}
	return g, nil
}

// ListFetchDocIDsNeedingExtractAfter resolves completed fetch docs that still
// need Extract. A document with no extractable source is selected only until
// Extract records the Silver document row; manual per-doc Extract remains the
// force refresh path.
func (a *Activities) ListFetchDocIDsNeedingExtractAfter(ctx context.Context, p ListStageFetchDocIDsAfterParams) ([]int64, error) {
	if a.dbpool == nil {
		return nil, fmt.Errorf("db pool is required")
	}
	const q = `
WITH candidates AS (
    SELECT
        fd.id,
        fd.source,
        fd.external_id,
        -- Approximates Go docKey(): "<TYPE>|<NUMBER>", number alone when the
        -- type is missing, source:external_id when the number is missing. It
        -- does NOT apply idDocTypeShortCodes / canonicalIDDocNumber, so
        -- Indonesian verbose observations key differently here than in silver
        -- and are re-selected each run (harmless: Extract/Normalize are
        -- idempotent; the Go key decides the silver row).
        COALESCE(
            CASE
                WHEN keys.num IS NULL THEN NULL
                WHEN keys.typ IS NULL THEN keys.num
                ELSE keys.typ || '|' || keys.num
            END,
            sd.source || ':' || sd.external_id
        ) AS doc_key,
        COALESCE(bool_or(
            (rp.kind = 'content_html' AND rp.content IS NOT NULL AND length(btrim(rp.content)) > 0)
            OR (rf.file_format IN ('docx', 'doc', 'pdf')
                AND rf.storage_path IS NOT NULL
                AND length(btrim(rf.storage_path)) > 0)
        ), false) AS has_extractable_source
    FROM ingest.fetch_doc fd
    JOIN bronze.source_document sd
      ON sd.source = fd.source
     AND sd.external_id = fd.external_id
    CROSS JOIN LATERAL (
        SELECT
            NULLIF(upper(regexp_replace(btrim(regexp_replace(btrim(translate(sd.doc_number, E'\u00A0', ' ')), '[[:space:]]*([/-])[[:space:]]*', '\1', 'g'), E' \t\r\n,.;:()[]{}'), '[[:space:]]+', ' ', 'g')), '') AS num,
            NULLIF(upper(regexp_replace(btrim(translate(sd.doc_type, E'\u00A0', ' ')), '[[:space:]]+', ' ', 'g')), '') AS typ
    ) AS keys
    LEFT JOIN bronze.raw_payload rp ON rp.source_document_id = sd.id
    LEFT JOIN bronze.raw_file rf ON rf.source_document_id = sd.id
    WHERE fd.state IN ('complete', 'partial')
      AND fd.in_scope
      AND fd.id > $1
    GROUP BY fd.id, sd.source, sd.external_id, keys.num, keys.typ
),
needed AS (
    SELECT DISTINCT ON (c.doc_key)
        c.id,
        c.doc_key
    FROM candidates c
    LEFT JOIN silver.document d ON d.doc_key = c.doc_key
    LEFT JOIN silver.document_text dt
      ON dt.document_id = d.id
     AND dt.markdown IS NOT NULL
     AND length(btrim(dt.markdown)) > 0
    LEFT JOIN silver.document_alias da
      ON da.source = c.source
     AND da.external_id = c.external_id
    WHERE d.id IS NULL
       OR da.document_id IS NULL
       OR (c.has_extractable_source AND dt.id IS NULL)
    ORDER BY c.doc_key, c.id
)
SELECT id
FROM needed
ORDER BY id
LIMIT $2`
	rows, err := a.dbpool.Query(ctx, q, p.AfterID, p.Limit)
	if err != nil {
		return nil, fmt.Errorf("list fetch docs needing extract after %d: %w", p.AfterID, err)
	}
	return scanInt64Rows(rows)
}

// listNormalizeCandidatesQuery is built once at init: sourcePriorityCaseSQL
// fills the fetch-source (%[1]s) and validity-row-source (%[2]s) priority
// expressions so the SQL ranks sources exactly like metadataPriority.
var listNormalizeCandidatesQuery = fmt.Sprintf(`
WITH candidates AS (
    SELECT
        fd.id,
        d.id AS document_id,
        %[1]s AS source_priority
    FROM ingest.fetch_doc fd
    JOIN silver.document_alias da
      ON da.source = fd.source
     AND da.external_id = fd.external_id
    JOIN silver.document d
      ON d.id = da.document_id
    WHERE fd.state IN ('complete', 'partial')
      AND fd.in_scope
      AND fd.id > $1
      -- Never a candidate while a strictly-higher-priority complete/partial
      -- fetch_doc exists for the same document — in EVERY mode, including Force.
      -- Without this, a forced drain pages past the priority pick and
      -- lower-priority siblings run LAST, replacing the authoritative source's
      -- provision-tree sections with a markdown parse (observed clobbering 86 VN
      -- docs).
      AND NOT EXISTS (
          SELECT 1
          FROM ingest.fetch_doc fd2
          JOIN silver.document_alias da2
            ON da2.source = fd2.source
           AND da2.external_id = fd2.external_id
          WHERE da2.document_id = d.id
            AND fd2.state IN ('complete', 'partial')
            AND fd2.in_scope
            AND %[3]s > %[1]s
      )
      AND ($3::boolean
          OR NOT EXISTS (
              SELECT 1
              FROM silver.validity_period vp
              WHERE vp.document_id = d.id
                AND vp.section_id IS NULL
                AND vp.superseded_at IS NULL
          )
          -- A scan normalized as textless during the pre-OCR drain still gets a
          -- document-level validity_period (status unknown), so the check above
          -- treats it as done. When OcrAll later writes ocr_extractive text, the
          -- doc has usable text but no sections — re-normalize so that text becomes
          -- citable sections. Self-clears once sections exist (no re-select loop).
          OR (
              EXISTS (
                  SELECT 1 FROM silver.document_text dt
                  WHERE dt.document_id = d.id
                    AND COALESCE(dt.markdown, '') <> ''
              )
              AND NOT EXISTS (
                  SELECT 1 FROM silver.document_section ds
                  WHERE ds.document_id = d.id
              )
          )
          -- Reopen on a better source: eligible again when this fetch_doc's
          -- source outranks (strictly) the source that wrote the current open
          -- doc-level validity row (NULL source ranks 0). Heals documents sealed
          -- by a low-authority source before the authoritative fetch completed —
          -- including cross-page shadowing under the paginated driver. Seals
          -- itself: once the authoritative source has normalized, no source
          -- strictly outranks its own validity row.
          OR %[1]s > COALESCE((
              SELECT MAX(%[2]s)
              FROM silver.validity_period vp
              WHERE vp.document_id = d.id
                AND vp.section_id IS NULL
                AND vp.superseded_at IS NULL
          ), 0))
),
needed AS (
    SELECT DISTINCT ON (document_id) id, document_id
    FROM candidates
    ORDER BY document_id, source_priority DESC, id
)
SELECT id
FROM needed
ORDER BY id
LIMIT $2`, sourcePriorityCaseSQL("fd.source"), sourcePriorityCaseSQL("vp.source"), sourcePriorityCaseSQL("fd2.source"))

// ListFetchDocIDsNeedingNormalizeAfter resolves docs whose Extract stage has
// created a Silver document but Normalize has not yet written the current
// document-level validity marker.
//
// A document can be discovered by several sources (VN: sbv_hanoi, vanban,
// vbpl), each with its own fetch_doc. Two rules decide which fetch_doc
// normalizes it:
//
//  1. Priority-ordered pick: among a document's eligible fetch_docs, the
//     highest metadataPriority source wins (id breaks ties). Previously the
//     lowest fetch_doc id won, so a low-authority source (sbv_hanoi) could
//     permanently shadow vbpl — the source carrying real validity status and
//     the provision tree.
//  2. Reopen on a better source: a fetch_doc is also eligible when its source
//     priority is strictly greater than the priority of the source that wrote
//     the document's current open doc-level validity row (NULL source ranks 0).
//     This heals documents already sealed by a low-authority source — the
//     page-shadowing failure where the paginated driver (fd.id > cursor,
//     LIMIT) normalized a low-priority fetch_doc and the resulting validity
//     row made the document look done forever — and later-arriving
//     authoritative fetches. The gate seals itself: after the authoritative
//     source normalizes, its priority is not strictly greater than its own.
//
// Eligibility otherwise unchanged: no open doc-level validity row; or text
// exists but sections do not (OCR re-trigger); Force bypasses the gate.
func (a *Activities) ListFetchDocIDsNeedingNormalizeAfter(ctx context.Context, p ListStageFetchDocIDsAfterParams) ([]int64, error) {
	if a.dbpool == nil {
		return nil, fmt.Errorf("db pool is required")
	}
	rows, err := a.dbpool.Query(ctx, listNormalizeCandidatesQuery, p.AfterID, p.Limit, p.Force)
	if err != nil {
		return nil, fmt.Errorf("list fetch docs needing normalize after %d: %w", p.AfterID, err)
	}
	return scanInt64Rows(rows)
}

// ListFetchDocIDsNeedingIndexAfter resolves normalized docs with current
// sections but no Gold chunks tied to those section rows.
func (a *Activities) ListFetchDocIDsNeedingIndexAfter(ctx context.Context, p ListStageFetchDocIDsAfterParams) ([]int64, error) {
	if a.dbpool == nil {
		return nil, fmt.Errorf("db pool is required")
	}
	const q = `
WITH candidates AS (
    SELECT
        fd.id,
        d.id AS document_id
    FROM ingest.fetch_doc fd
    JOIN silver.document_alias da
      ON da.source = fd.source
     AND da.external_id = fd.external_id
    JOIN silver.document d
      ON d.id = da.document_id
    WHERE fd.state IN ('complete', 'partial')
      AND fd.in_scope
      AND fd.id > $1
      AND EXISTS (
          SELECT 1
          FROM silver.validity_period vp
          WHERE vp.document_id = d.id
            AND vp.section_id IS NULL
            AND vp.superseded_at IS NULL
      )
      AND EXISTS (
          SELECT 1
          FROM silver.document_section s
          WHERE s.document_id = d.id
      )
      AND (
          $3::boolean
          OR (
              d.index_class = 'primary'
              AND NOT EXISTS (
                  SELECT 1
                  FROM gold.chunk c
                  JOIN silver.document_section s
                    ON s.id = c.section_id
                   AND s.document_id = d.id
                  WHERE c.document_id = d.id
              )
          )
      )
),
needed AS (
    SELECT DISTINCT ON (document_id) id, document_id
    FROM candidates
    ORDER BY document_id, id
)
SELECT id
FROM needed
ORDER BY id
LIMIT $2`
	rows, err := a.dbpool.Query(ctx, q, p.AfterID, p.Limit, p.Force)
	if err != nil {
		return nil, fmt.Errorf("list fetch docs needing index after %d: %w", p.AfterID, err)
	}
	return scanInt64Rows(rows)
}

func scanInt64Rows(rows pgx.Rows) ([]int64, error) {
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

func (a *Activities) scheduleSourceContentRecheck(ctx context.Context, fetchDocID int64, reason string, now time.Time) {
	if a.ledger == nil {
		return
	}
	bodyNext := now.Add(sourceContentRecheckDelay)
	fileNext := bodyNext.Add(sourceContentRecheckFileDelay)
	if err := a.ledger.ScheduleSourceContentRecheck(ctx, dbingest.ScheduleSourceContentRecheckParams{
		FetchDocID:        fetchDocID,
		BodyNextAttemptAt: &bodyNext,
		FileNextAttemptAt: &fileNext,
		Reason:            strPtr(reason),
		UpdatedAt:         now,
		MaxRechecks:       sourceContentMaxRechecks,
	}); err != nil {
		a.log.Warn("schedule source content recheck failed",
			"fetch_doc", fetchDocID, "err", err)
		return
	}
	a.log.Info("scheduled source content recheck",
		"fetch_doc", fetchDocID, "next_attempt_at", bodyNext)
}

func (a *Activities) discoverCongbaoFallback(
	ctx context.Context,
	fd dbingest.IngestFetchDoc,
	sd dbbronze.BronzeSourceDocument,
	reason string,
	now time.Time,
) {
	log := a.log
	if fd.Source != "vbpl" || a.ledger == nil || a.bronze == nil {
		return
	}
	if sd.DocNumber == nil || strings.TrimSpace(*sd.DocNumber) == "" {
		return
	}
	if sd.IssuedAt == nil || now.Sub(sd.IssuedAt.UTC()) < congbaoFallbackMinAge {
		return
	}

	src, ok := a.sources["congbao"]
	if !ok {
		return
	}
	searcher, ok := src.(ingest.NumberSearcher)
	if !ok {
		return
	}

	number := strings.TrimSpace(*sd.DocNumber)
	var titleHint string
	if sd.Title != nil {
		titleHint = strings.TrimSpace(*sd.Title)
	}
	doc, found, err := searcher.SearchByNumber(ctx, number, titleHint)
	if err != nil {
		log.Warn("congbao fallback search failed",
			"source_doc", fd.ExternalID, "number", number, "err", err)
		return
	}
	if !found {
		log.Info("congbao fallback not found", "source_doc", fd.ExternalID, "number", number)
		return
	}
	if ok, rejectReason := validCongbaoFallbackCandidate(sd, *doc); !ok {
		log.Info("congbao fallback rejected",
			"source_doc", fd.ExternalID, "congbao_doc", doc.ExternalID, "number", number, "reason", rejectReason)
		return
	}

	if err := a.recordDiscoveredDoc(ctx,
		"congbao",
		"source_fallback",
		"source_fallback",
		*doc,
		[]string{reason},
		fd.ID,
		"official_file",
		now,
	); err != nil {
		log.Warn("record congbao fallback failed",
			"source_doc", fd.ExternalID, "congbao_doc", doc.ExternalID, "number", number, "err", err)
		return
	}
	log.Info("discovered congbao fallback",
		"source_doc", fd.ExternalID,
		"congbao_doc", doc.ExternalID,
		"number", number,
		"files", len(doc.Files))
}

func validCongbaoFallbackCandidate(sd dbbronze.BronzeSourceDocument, doc ingest.DiscoveredDoc) (bool, string) {
	sourceNumber := sourceDocNumberNorm(sd)
	if sourceNumber == "" {
		return false, "missing_source_number"
	}
	if got := normalizeDocNumberForStorage(doc.Number); got != sourceNumber {
		return false, "number_mismatch"
	}
	if !compatibleIssueDate(sd.IssuedAt, doc.IssuedAt) {
		return false, "issued_date_mismatch"
	}
	if !compatibleDocType(sd.DocType, string(doc.DocType)) {
		return false, "doc_type_mismatch"
	}
	if !hasExtractableFileRefs(doc.Files) {
		return false, "no_extractable_files"
	}
	return true, ""
}

func compatibleIssueDate(source *time.Time, fallback time.Time) bool {
	if source == nil || source.IsZero() || fallback.IsZero() {
		return true
	}
	s := source.UTC()
	f := fallback.UTC()
	return s.Year() == f.Year() && s.Month() == f.Month() && s.Day() == f.Day()
}

func compatibleDocType(source *string, fallback string) bool {
	if source == nil || strings.TrimSpace(*source) == "" || strings.TrimSpace(fallback) == "" {
		return true
	}
	return strings.EqualFold(strings.Join(strings.Fields(*source), " "), strings.Join(strings.Fields(fallback), " "))
}

func hasExtractableFileRefs(files []ingest.FileRef) bool {
	for _, f := range files {
		switch strings.ToLower(strings.TrimSpace(f.Ext)) {
		case "docx", "doc", "pdf":
			if strings.TrimSpace(f.URL) != "" {
				return true
			}
		}
	}
	return false
}

// vnDocNumberYearRe captures the 4-digit year embedded in a Vietnamese số ký
// hiệu (e.g. "04/2025/TT-NHNN" → 2025). The year is always slash-delimited —
// requiring the slashes keeps year-looking ORDINALS (e.g. a decision numbered
// "2028/QĐ-NHNN") from being mistaken for a promulgation year.
var vnDocNumberYearRe = regexp.MustCompile(`/((?:19|20)\d{2})/`)

// correctIssuedAtYear guards against off-by-one issued_at years in source feeds
// (vbpl served 04/2025/TT-NHNN with issueDate 2024-05-15). When the doc_number
// embeds a year and issued_at's year differs from it beyond a December/January
// boundary tolerance, the year is corrected to the number's year (month/day
// preserved) and a WARN is logged. A document numbered for a year but signed in
// the adjacent December (numberYear-1) or January (numberYear+1) is legitimate
// and left untouched. Gated to VN by the caller: only VN numbers embed a year.
func correctIssuedAtYear(log *slog.Logger, docNumber *string, issuedAt *time.Time, externalID string) *time.Time {
	if issuedAt == nil || issuedAt.IsZero() || docNumber == nil {
		return issuedAt
	}
	m := vnDocNumberYearRe.FindStringSubmatch(*docNumber)
	if m == nil {
		return issuedAt
	}
	numberYear, err := strconv.Atoi(m[1])
	if err != nil {
		return issuedAt
	}
	issuedYear := issuedAt.Year()
	if issuedYear == numberYear {
		return issuedAt
	}
	// Dec/Jan boundary tolerance: a doc numbered for numberYear but signed in the
	// adjacent December of the prior year, or January of the following year, is a
	// legitimate straddle — not an error.
	if issuedAt.Month() == time.December && issuedYear == numberYear-1 {
		return issuedAt
	}
	if issuedAt.Month() == time.January && issuedYear == numberYear+1 {
		return issuedAt
	}
	corrected := time.Date(numberYear, issuedAt.Month(), issuedAt.Day(),
		issuedAt.Hour(), issuedAt.Minute(), issuedAt.Second(), issuedAt.Nanosecond(), issuedAt.Location())
	if log != nil {
		log.Warn("issued_at year corrected from doc_number",
			"doc", externalID, "doc_number", *docNumber,
			"from", issuedAt.Format("2006-01-02"), "to", corrected.Format("2006-01-02"))
	}
	return &corrected
}

// upsertSilverDocument writes the logical document row from the bronze observation.
func (a *Activities) upsertSilverDocument(ctx context.Context, sd dbbronze.BronzeSourceDocument, markdown string, now time.Time) (int64, error) {
	var md *string
	if markdown != "" {
		md = &markdown
	}
	var displayNumber *string
	if sd.DocNumber != nil {
		if n := cleanDocNumber(*sd.DocNumber); n != "" {
			n = canonicalIDDocNumber(n)
			displayNumber = &n
		}
	}
	// Cross-check the source issued_at year against the year embedded in the
	// Vietnamese số ký hiệu (VN-only: only VN numbers carry the promulgation
	// year). Other jurisdictions keep the source value verbatim.
	issuedAt := sd.IssuedAt
	if a.jur.StructureParser == jurisdiction.ParserVNMarkdown {
		issuedAt = correctIssuedAtYear(a.log, sd.DocNumber, issuedAt, sd.ExternalID)
	}
	id, err := a.silver.UpsertDocument(ctx, dbsilver.UpsertDocumentParams{
		DocKey:           docKey(sd),
		DocNumber:        displayNumber,
		DocNumberNorm:    sd.DocNumberNorm,
		Title:            sd.Title,
		DocType:          sd.DocType,
		DocTypeCode:      sd.DocTypeCode,
		Issuer:           sd.Issuer,
		IssuerCode:       sd.IssuerCode,
		IssuedAt:         issuedAt,
		IsConsolidated:   sd.IsConsolidated,
		Signer:           a.signerFromDetailMeta(ctx, sd.ID),
		Markdown:         md,
		SourceDocumentID: &sd.ID,
		CreatedAt:        now,
		MetadataPriority: metadataPriority(sd.Source),
	})
	if err != nil {
		return 0, fmt.Errorf("upsert document %s: %w", sd.ExternalID, err)
	}
	matchMethod, confidence := documentAliasMatch(sd)
	if _, err := a.silver.UpsertDocumentAlias(ctx, dbsilver.UpsertDocumentAliasParams{
		Source:      sd.Source,
		ExternalID:  sd.ExternalID,
		DocumentID:  id,
		MatchMethod: matchMethod,
		Confidence:  confidence,
	}); err != nil {
		return 0, fmt.Errorf("upsert document alias %s/%s: %w", sd.Source, sd.ExternalID, err)
	}
	if err := a.resolveSilverDocRef(ctx, docKey(sd), id, now); err != nil {
		return 0, fmt.Errorf("resolve document refs %s/%s: %w", sd.Source, sd.ExternalID, err)
	}
	sourceKey := sourceDocRefKey(sd.Source, sd.ExternalID)
	if sourceKey != "" && sourceKey != docKey(sd) {
		if err := a.resolveSilverDocRef(ctx, sourceKey, id, now); err != nil {
			return 0, fmt.Errorf("resolve source document refs %s/%s: %w", sd.Source, sd.ExternalID, err)
		}
	}
	// References that carry only a số ký hiệu resolve here too, but only while
	// this document is the sole holder of that number — shared numbers stay
	// stubs rather than guessing between distinct documents.
	if numberKey := docNumberKey(nullableString(sd.DocNumber)); numberKey != "" && numberKey != docKey(sd) {
		if err := a.silver.ResolveDocRefForUniqueNumber(ctx, dbsilver.ResolveDocRefForUniqueNumberParams{
			RefKey:        numberKey,
			DocumentID:    &id,
			UpdatedAt:     now,
			DocNumberNorm: sourceDocNumberNorm(sd),
		}); err != nil {
			return 0, fmt.Errorf("resolve number document refs %s/%s: %w", sd.Source, sd.ExternalID, err)
		}
	}
	return id, nil
}

// sourceMetadataPriority is the single source→priority table for cross-source
// authority. Authoritative metadata sources (vbpl for VN, ojk for ID) rank 10;
// secondary official sources (congbao gazette, bi central bank for ID) rank 7;
// every unlisted source gets defaultSourceMetadataPriority. It backs both
// metadataPriority (Go-side metadata dedup) and sourcePriorityCaseSQL (the
// normalize selector's SQL gate) — change priorities here, never in SQL.
var sourceMetadataPriority = map[string]int16{
	"vbpl":    10,
	"ojk":     10,
	"congbao": 7,
	"bi":      7,
	// vbhn is not a crawler source: it is the derived-validity pass for VBHN
	// consolidations (vbhn_validity.go), which stamps validity rows with
	// source='vbhn'. It must rank with vbpl — otherwise the normalize selector's
	// reopen-on-better-source clause sees vbpl(10) > vbhn(default 5) and reopens
	// every consolidation each drain round, so the convergence loop never
	// converges (observed 2026-07-24).
	"vbhn": 10,
}

// defaultSourceMetadataPriority ranks sources absent from sourceMetadataPriority.
const defaultSourceMetadataPriority int16 = 5

// metadataPriority returns the metadata-dedup priority for a source. When two
// sources discover the same document, the higher-priority source keeps its
// metadata (title, issuer, dates, source_document_id for relations). Text
// quality (born-digital vs OCR) is handled separately by document_text
// authority ordering, not by this priority.
func metadataPriority(source string) int16 {
	if p, ok := sourceMetadataPriority[source]; ok {
		return p
	}
	return defaultSourceMetadataPriority
}

// sourcePriorityCaseSQL renders sourceMetadataPriority as a SQL CASE expression
// over col, with WHEN branches in deterministic (sorted-key) order. A NULL col
// ranks 0 — below every real source — so an open validity row without a
// recorded source never blocks a re-normalize; unlisted sources get the same
// default as metadataPriority.
func sourcePriorityCaseSQL(col string) string {
	keys := make([]string, 0, len(sourceMetadataPriority))
	for k := range sourceMetadataPriority {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, "CASE WHEN %s IS NULL THEN 0", col)
	for _, k := range keys {
		fmt.Fprintf(&b, " WHEN %s = '%s' THEN %d", col, k, sourceMetadataPriority[k])
	}
	fmt.Fprintf(&b, " ELSE %d END", defaultSourceMetadataPriority)
	return b.String()
}

func (a *Activities) resolveSilverDocRef(ctx context.Context, refKey string, documentID int64, now time.Time) error {
	if strings.TrimSpace(refKey) == "" {
		return nil
	}
	return a.silver.ResolveDocRef(ctx, dbsilver.ResolveDocRefParams{
		RefKey:     refKey,
		DocumentID: &documentID,
		UpdatedAt:  now,
	})
}

// pickFile returns the first raw file matching format and one of the allowed
// file kinds, in kind priority order. When multiple files share the same kind
// (e.g. three "main" PDFs on an OJK detail page), the lowest-ordinal file wins
// — sources put the regulation body first in DOM order, and duplicate UUIDs are
// already deduped at discover time. Appendices are intentionally excluded from
// primary text selection; they are supplemental material, not the binding body.
func pickFile(files []dbbronze.BronzeRawFile, format string, kinds ...string) *dbbronze.BronzeRawFile {
	for _, kind := range kinds {
		for i := range files {
			if strings.EqualFold(files[i].FileFormat, format) && strings.EqualFold(files[i].FileKind, kind) {
				return &files[i]
			}
		}
	}
	return nil
}

func pickPDFForExtraction(files []dbbronze.BronzeRawFile, sawBornDigitalReview bool) *dbbronze.BronzeRawFile {
	if pdf := pickFile(files, "pdf", "main"); pdf != nil {
		return pdf
	}
	if sawBornDigitalReview {
		return nil
	}
	return pickFile(files, "pdf", "original_scan")
}

// docKey is the canonical silver key for a source observation:
// "<TYPE>|<NUMBER>" — normalized loại văn bản plus normalized số ký hiệu.
// The type discriminates because distinct documents can share a số ký hiệu
// (Luật and Nghị quyết 51/2005/QH11 are different documents). Observations
// without a type key on the number alone; without a number they fall back to
// source:external_id. Must stay in lockstep with the SQL doc_key expression in
// ListFetchDocIDsNeedingExtractAfter.
func docKey(sd dbbronze.BronzeSourceDocument) string {
	number := ""
	if sd.DocNumber != nil {
		number = docNumberKey(canonicalIDDocNumber(*sd.DocNumber))
	}
	if number == "" {
		return sd.Source + ":" + sd.ExternalID
	}
	t := docTypeKey(sd.DocType)
	// OJK families embed their code in the number itself ("43/PADK.03/2025").
	// That infix is authoritative: sources occasionally mislabel the type (a
	// PADK listed as SEOJK), which forks the doc_key into a duplicate document.
	// Non-ID numbers never contain these infixes — untouched.
	if infix := idTypeFromNumberInfix(number); infix != "" {
		t = infix
		if i := strings.IndexByte(number, ' '); i > 0 && idShortCodes[number[:i]] && number[:i] != infix {
			number = number[i+1:]
		}
	}
	// Vietnamese numbers encode the type in the suffix (03/2026/TT-NHNN → Thông
	// tư); that suffix is authoritative and overrides a mislabeled source type so
	// a secondary source cannot fork a duplicate identity. QH numbers stay
	// ambiguous (Luật vs Nghị quyết) and are never overridden.
	if suffix := vnTypeFromNumberSuffix(number); suffix != "" {
		t = suffix
	}
	if t != "" {
		// Indonesian sources disagree on embedding the type code in the number
		// ("UU 4/2023" vs bare "4/2023" vs JDIH "11/POJK.03/2022"). For known ID
		// codes, prefix the code so all shapes key identically. Other
		// jurisdictions' type keys are never in idShortCodes — untouched.
		if idShortCodes[t] && !strings.HasPrefix(number, t+" ") {
			number = t + " " + number
		}
		return t + "|" + number
	}
	return number
}

// idNumberInfixRe matches the OJK regulation-family code embedded in an
// Indonesian number ("11/POJK.03/2022", "43/PADK.03/2025").
var idNumberInfixRe = regexp.MustCompile(`/(POJK|SEOJK|PADK)\.`)

// idTypeFromNumberInfix returns the OJK family code embedded in a regulation
// number, or "" when the number carries none.
func idTypeFromNumberInfix(number string) string {
	if m := idNumberInfixRe.FindStringSubmatch(strings.ToUpper(number)); m != nil {
		return m[1]
	}
	return ""
}

// vnNumberTypeSuffixes maps the type-bearing suffix on a Vietnamese số ký hiệu
// (the token after the last "/") to its canonical loại văn bản key. VN numbers
// encode the document type in this suffix by convention (03/2026/TT-NHNN is a
// Thông tư), so it is authoritative: when a secondary source mislabels the type
// (vanban tagging a TT- number as "Luật"), keying off the source type forks a
// duplicate silver.document. Ordered longest-first so TTLT- wins over TT-. The
// values match docTypeKey output (upper case, single-spaced). These tokens are
// Vietnamese doc-number conventions; other jurisdictions' numbers never carry
// them — untouched.
var vnNumberTypeSuffixes = []struct{ prefix, typeKey string }{
	{"TTLT-", "THÔNG TƯ LIÊN TỊCH"},
	{"VBHN-", "VBHN"},
	{"NĐ-CP", "NGHỊ ĐỊNH"},
	{"NQ-CP", "NGHỊ QUYẾT"},
	{"TT-", "THÔNG TƯ"},
	{"QĐ-", "QUYẾT ĐỊNH"},
	{"CT-", "CHỈ THỊ"},
}

// vnQHNumberRe matches a Quốc hội số ký hiệu suffix (e.g. "51/2005/QH11"). Laws
// (Luật) and Resolutions (Nghị quyết) legitimately share these numbers, so the
// suffix does NOT disambiguate the type — never override it.
var vnQHNumberRe = regexp.MustCompile(`(?i)/QH\d+$`)

// vnTypeFromNumberSuffix derives the canonical loại văn bản key from the
// type-bearing suffix of a Vietnamese số ký hiệu, or "" when the number carries
// no unambiguous suffix. QH-numbered documents are ambiguous (Luật vs Nghị
// quyết) and are never overridden. The input is the already-normalized,
// upper-cased number component built in docKey.
func vnTypeFromNumberSuffix(number string) string {
	u := strings.ToUpper(strings.TrimSpace(number))
	if u == "" || vnQHNumberRe.MatchString(u) {
		return ""
	}
	tail := u
	if i := strings.LastIndexByte(u, '/'); i >= 0 {
		tail = u[i+1:]
	}
	for _, s := range vnNumberTypeSuffixes {
		if strings.HasPrefix(tail, s.prefix) {
			return s.typeKey
		}
	}
	return ""
}

// docNumberKey normalizes a số ký hiệu exactly like number-based doc_ref keys
// so a document's number component and incoming number references align.
// Unicode spaces fold to plain spaces first so separator tightening also
// removes NBSP around "/" and "-".
func docNumberKey(number string) string {
	return canonicalDocRefKey(foldSpaces(number))
}

// idDocTypeShortCodes maps verbose Indonesian regulation type names to BPK
// short codes. This ensures doc_key convergence across sources (ojkweb uses
// verbose names, BPK/ojk use short codes) so the same regulation merges into
// one silver.document instead of creating duplicates.
var idDocTypeShortCodes = map[string]string{
	"PERATURAN OTORITAS JASA KEUANGAN":                                 "POJK",
	"SURAT EDARAN OTORITAS JASA KEUANGAN":                              "SEOJK",
	"PERATURAN BANK INDONESIA":                                         "PBI",
	"PERATURAN ANGGOTA DEWAN GUBERNUR":                                 "PADG",
	"PERATURAN BADAN SIBER DAN SANDI NEGARA":                           "BSSN",
	"PERATURAN MENTERI KEUANGAN":                                       "PMK",
	"PERATURAN MENTERI KOMUNIKASI DAN INFORMATIKA":                     "KOMINFO",
	"PERATURAN MENTERI KOMUNIKASI DAN DIGITAL":                         "KOMDIGI",
	"PERATURAN LEMBAGA PENJAMIN SIMPANAN":                              "LPS",
	"PERATURAN PRESIDEN":                                               "PERPRES",
	"PERATURAN PEMERINTAH":                                             "PP",
	"UNDANG-UNDANG":                                                    "UU",
	"UNDANG-UNDANG (UU)":                                               "UU",
	"PERATURAN PEMERINTAH (PP)":                                        "PP",
	"PERATURAN PRESIDEN (PERPRES)":                                     "PERPRES",
	"PERATURAN PUSAT PELAPORAN DAN ANALISIS TRANSAKSI KEUANGAN":        "PPATK",
	"PERATURAN KEPALA PUSAT PELAPORAN DAN ANALISIS TRANSAKSI KEUANGAN": "PPATK",
	"PERKA PPATK":                                                      "PPATK",
	"SURAT EDARAN BANK INDONESIA":                                      "SEBI",
}

func docTypeKey(docType *string) string {
	if docType == nil {
		return ""
	}
	upper := strings.ToUpper(strings.Join(strings.Fields(*docType), " "))
	if short, ok := idDocTypeShortCodes[upper]; ok {
		return short
	}
	return upper
}

// idVerboseNumberForms rewrites a verbose Indonesian regulation-type phrase at
// the head of a doc_number to its BPK short code, so stale ojk/ojkweb bronze
// rows ("Peraturan Otoritas Jasa Keuangan Nomor 21 Tahun 2023") key and display
// identically to BPK short forms ("POJK 21/2023"). Ordered longest-first so
// PERPPU never half-matches as PP + UU. Paired with idDocTypeShortCodes above
// and mirrored (for goldens) in pkg/eval.
var idVerboseNumberForms = []struct{ verbose, short string }{
	{"PERATURAN PEMERINTAH PENGGANTI UNDANG-UNDANG", "PERPPU"},
	{"PERATURAN KEPALA PUSAT PELAPORAN DAN ANALISIS TRANSAKSI KEUANGAN", "PPATK"},
	{"PERATURAN PUSAT PELAPORAN DAN ANALISIS TRANSAKSI KEUANGAN", "PPATK"},
	{"PERATURAN MENTERI KOMUNIKASI DAN INFORMATIKA", "KOMINFO"},
	{"PERATURAN MENTERI KOMUNIKASI DAN DIGITAL", "KOMDIGI"},
	{"PERATURAN BADAN SIBER DAN SANDI NEGARA", "BSSN"},
	{"SURAT EDARAN OTORITAS JASA KEUANGAN", "SEOJK"},
	{"PERATURAN OTORITAS JASA KEUANGAN", "POJK"},
	{"PERATURAN ANGGOTA DEWAN GUBERNUR", "PADG"},
	{"PERATURAN ANGGOTA DEWAN KOMISIONER", "PADK"},
	{"PERATURAN LEMBAGA PENJAMIN SIMPANAN", "LPS"},
	{"SURAT EDARAN BANK INDONESIA", "SEBI"},
	{"PERATURAN BANK INDONESIA", "PBI"},
	{"PERATURAN MENTERI KEUANGAN", "PMK"},
	{"PERATURAN PEMERINTAH (PP)", "PP"},
	{"PERATURAN PRESIDEN (PERPRES)", "PERPRES"},
	{"PERATURAN PEMERINTAH", "PP"},
	{"PERATURAN PRESIDEN", "PERPRES"},
	{"UNDANG-UNDANG (UU)", "UU"},
	{"UNDANG-UNDANG", "UU"},
	{"PERKA PPATK", "PPATK"},
}

// idShortCodes gates canonicalIDDocNumber: the filler/tahun rewrites apply only
// when the number starts with a known Indonesian short code, so numbers from
// every other jurisdiction pass through untouched.
var idShortCodes = map[string]bool{
	"POJK": true, "SEOJK": true, "PBI": true, "PADG": true, "PADK": true,
	"SEBI": true, "BSSN": true, "PPATK": true, "LPS": true, "PMK": true,
	"KOMINFO": true, "KOMDIGI": true, "PP": true, "PERPRES": true,
	"PERPPU": true, "UU": true,
}

var (
	// idNumberFillerRe strips a leading "NOMOR"/"NO." filler after the code,
	// optionally preceded by a parenthetical repeat of the code ("PMK (PMK)
	// NOMOR 68/...").
	idNumberFillerRe = regexp.MustCompile(`^(?:\([A-Z]+\)\s*)?(?:NOMOR|NO\.?)\s*`)
	// idTahunSuffixRe captures a trailing "TAHUN <year>" clause.
	idTahunSuffixRe = regexp.MustCompile(`\s+TAHUN\s+(\d{4})$`)
	// idBareTahunRe matches a code-less "N TAHUN YYYY" number (optionally with
	// a NOMOR/NO. filler) — an Indonesian-only shape.
	idBareTahunRe = regexp.MustCompile(`^(?:NOMOR\s+|NO\.?\s*)?\d\S*(?:\s+\S+)*\s+TAHUN\s+\d{4}$`)
	// idYearRe reports whether a number already embeds a 4-digit year.
	idYearRe = regexp.MustCompile(`\d{4}`)
)

// canonicalIDDocNumber folds an Indonesian verbose doc_number to the BPK short
// form: type phrase → short code, "NOMOR"/"NO." dropped, a trailing
// "TAHUN <year>" folded into "<number>/<year>" (dropped when the number already
// embeds a year). Non-Indonesian numbers return unchanged (case preserved).
func canonicalIDDocNumber(number string) string {
	u := strings.ToUpper(strings.Join(strings.Fields(foldSpaces(number)), " "))
	for _, f := range idVerboseNumberForms {
		if strings.HasPrefix(u, f.verbose+" ") {
			u = f.short + " " + strings.TrimSpace(u[len(f.verbose):])
			break
		}
	}
	code, rest, ok := strings.Cut(u, " ")
	switch {
	case ok && idShortCodes[code]:
		// "<CODE> ..." — fold fillers/year below.
	case idBareTahunRe.MatchString(u):
		// Bare "N TAHUN YYYY" (no code — TAHUN is an Indonesian-only marker):
		// fold to "N/YYYY"; docKey prefixes the type code separately.
		code, rest = "", u
	default:
		return number
	}
	rest = idNumberFillerRe.ReplaceAllString(rest, "")
	if m := idTahunSuffixRe.FindStringSubmatch(rest); m != nil {
		base := strings.TrimSpace(strings.TrimSuffix(rest, m[0]))
		if idYearRe.MatchString(base) {
			rest = base
		} else {
			rest = base + "/" + m[1]
		}
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return number
	}
	if code == "" {
		return rest
	}
	return code + " " + rest
}

func foldSpaces(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		return r
	}, s)
}

var (
	// soPrefixRe strips a stray leading "số:" / "Số." marker some sources keep
	// in front of the số ký hiệu ("số: 34/2024/QH15"). The colon/period is
	// required so a number can never lose a legitimate leading token.
	soPrefixRe = regexp.MustCompile(`^(?i)số\s*[:.]\s*`)
	// docNumberSepRe tightens spaces around the số ký hiệu separators
	// ("18 /2018" → "18/2018").
	docNumberSepRe = regexp.MustCompile(`\s*([/-])\s*`)
)

// cleanDocNumber tidies a source số ký hiệu for display in silver: Unicode
// spaces fold, a stray leading "số:" marker drops, separators tighten, and
// whitespace collapses. Case is preserved — bronze keeps the verbatim value.
func cleanDocNumber(number string) string {
	s := strings.TrimSpace(foldSpaces(number))
	s = soPrefixRe.ReplaceAllString(s, "")
	s = docNumberSepRe.ReplaceAllString(s, "$1")
	return strings.Join(strings.Fields(s), " ")
}

func documentAliasMatch(sd dbbronze.BronzeSourceDocument) (string, pgtype.Float8) {
	if sd.DocNumber != nil && strings.TrimSpace(*sd.DocNumber) != "" {
		return "so_hieu_van_ban", pgtype.Float8{Float64: 1, Valid: true}
	}
	return "source_external_id", pgtype.Float8{Float64: 1, Valid: true}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
