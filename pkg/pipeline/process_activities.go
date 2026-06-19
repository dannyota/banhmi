package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"go.temporal.io/sdk/activity"

	"danny.vn/banhmi/pkg/extract"
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
// turns it into NFC-normalized Markdown with a deterministic engine (DOCX, HTML,
// then PDF/OCR), gates the result for quality, and writes silver.document +
// silver.document_text with full provenance.
//
// Engine selection:
//   - DOCX/HTML: local MarkItDown; failed conversion or failed quality gate
//     falls through to the next source in the cascade.
//   - Legacy DOC: rendered to PDF with LibreOffice in the MarkItDown helper,
//     then converted with MarkItDown. It is tried after HTML and before source
//     PDF/OCR.
//   - PDF: Go-side assessment tries local MarkItDown and checks the result with
//     the content gate (tunable via config.setting). Assessment failure or gate
//     failure routes to local PDF OCR.
//   - No file: document recorded and flagged needs_review.
func (a *Activities) Extract(ctx context.Context, p StageParams) (ExtractResult, error) {
	log := activity.GetLogger(ctx)
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

	// --- legacy DOC (official Word binary rendered to PDF, then MarkItDown) ---
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
	text, engine, err := a.htmlToMarkdown(ctx, externalID, body)
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

// htmlToMarkdown converts an HTML body to NFC-normalized Markdown via the
// local MarkItDown. It returns the text and the engine provenance tag.
func (a *Activities) htmlToMarkdown(ctx context.Context, externalID, body string) (string, string, error) {
	markitdown, err := a.requireMarkItDown()
	if err != nil {
		return "", "", err
	}
	res, err := markitdown.ConvertData(ctx, []byte(ensureHTMLUTF8(body)), ".html")
	if err != nil {
		return "", "", fmt.Errorf("markitdown html %s: %w", externalID, err)
	}
	return extract.Normalize(res.Markdown), "markitdown/1", nil
}

func ensureHTMLUTF8(body string) string {
	lower := strings.ToLower(body)
	if strings.Contains(lower, "charset=") {
		return body
	}
	if i := strings.Index(lower, "<head"); i >= 0 {
		if end := strings.Index(body[i:], ">"); end >= 0 {
			pos := i + end + 1
			return body[:pos] + `<meta charset="utf-8">` + body[pos:]
		}
	}
	return `<!doctype html><html><head><meta charset="utf-8"></head><body>` + body + `</body></html>`
}

// docxToMarkdown converts DOCX bytes to NFC-normalized Markdown via the
// local MarkItDown. It returns the text and the engine provenance tag.
func (a *Activities) docxToMarkdown(ctx context.Context, externalID string, data []byte) (string, string, error) {
	markitdown, err := a.requireMarkItDown()
	if err != nil {
		return "", "", err
	}
	res, err := markitdown.ConvertData(ctx, data, ".docx")
	if err != nil {
		return "", "", fmt.Errorf("markitdown docx %s: %w", externalID, err)
	}
	return extract.Normalize(res.Markdown), "markitdown/1", nil
}

// docToMarkdown converts legacy OLE DOC bytes by rendering the file to PDF with
// LibreOffice in the helper, then converting that PDF to NFC-normalized Markdown
// with MarkItDown.
func (a *Activities) docToMarkdown(ctx context.Context, externalID string, data []byte) (string, string, error) {
	markitdown, err := a.requireMarkItDown()
	if err != nil {
		return "", "", err
	}
	res, err := markitdown.ConvertData(ctx, data, ".doc")
	if err != nil {
		return "", "", fmt.Errorf("markitdown doc %s: %w", externalID, err)
	}
	return extract.Normalize(cleanPDFMarkdownNoise(res.Markdown)), "libreoffice-pdf/1+markitdown/1", nil
}

// extractDOCX runs the DOCX extraction path and writes silver.document_text.
func (a *Activities) extractDOCX(ctx context.Context, source, externalID string, sd dbbronze.BronzeSourceDocument, docx *dbbronze.BronzeRawFile, now time.Time) (ExtractResult, error) {
	log := activity.GetLogger(ctx)

	data, err := os.ReadFile(filepath.Join(a.storageDir, *docx.StoragePath))
	if err != nil {
		return ExtractResult{}, fmt.Errorf("read docx %s: %w", *docx.StoragePath, err)
	}
	text, engine, err := a.docxToMarkdown(ctx, externalID, data)
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
	log := activity.GetLogger(ctx)

	data, err := os.ReadFile(filepath.Join(a.storageDir, *doc.StoragePath))
	if err != nil {
		return ExtractResult{}, fmt.Errorf("read doc %s: %w", *doc.StoragePath, err)
	}
	text, engine, err := a.docToMarkdown(ctx, externalID, data)
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
	log := activity.GetLogger(ctx)

	gate, err := a.loadGate(ctx)
	if err != nil {
		// Config load failing must not block extraction; fall back to defaults.
		log.Warn("extract: failed to load gate config, using defaults", "err", err)
		gate = extract.DefaultGate()
	}

	absPath := filepath.Join(a.storageDir, *pdf.StoragePath)
	assessment := a.assessPDFExtraction(ctx, externalID, absPath, gate)

	if assessment.sourceUnavailable {
		return a.writePDFText(ctx, source, externalID, sd, pdf, assessment.text, 0,
			"gazette_borndigital", assessment.engine, false, true, true, now)
	}

	if assessment.extractable {
		return a.writePDFText(ctx, source, externalID, sd, pdf, assessment.text, assessment.gate.Confidence,
			"gazette_borndigital", assessment.engine, true, false, false, now)
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

func (a *Activities) assessPDFExtraction(ctx context.Context, externalID, absPath string, gate extract.GateConfig) pdfExtractionAssessment {
	text, engine, err := a.pdfToMarkdown(ctx, externalID, absPath)
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

// pdfToMarkdown converts a born-digital PDF into NFC-normalized Markdown/text.
// MarkItDown is the single document-to-Markdown engine for DOCX, HTML, and PDF.
func (a *Activities) pdfToMarkdown(ctx context.Context, externalID, absPath string) (string, string, error) {
	markitdown, err := a.requireMarkItDown()
	if err != nil {
		return "", "markitdown/1", err
	}
	res, err := markitdown.ConvertPath(ctx, absPath)
	if err != nil {
		return "", "markitdown/1", fmt.Errorf("markitdown pdf %s: %w", externalID, err)
	}
	return extract.Normalize(cleanPDFMarkdownNoise(res.Markdown)), "markitdown/1", nil
}

func cleanPDFMarkdownNoise(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.ReplaceAll(text, "\f", "\n")

	lines := strings.Split(text, "\n")
	remove := make([]bool, len(lines))
	header := make([]bool, len(lines))
	for i, line := range lines {
		if isCongbaoPageHeader(line) {
			remove[i] = true
			header[i] = true
		}
	}
	for i, line := range lines {
		if isStandalonePageNumber(line) && hasNearbyCongbaoHeader(lines, header, i) {
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

func hasNearbyCongbaoHeader(lines []string, header []bool, idx int) bool {
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

func (a *Activities) requireMarkItDown() (*extract.MarkItDownClient, error) {
	if a.markitdown == nil {
		return nil, fmt.Errorf("markitdown client not configured")
	}
	return a.markitdown, nil
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
	log := activity.GetLogger(ctx)

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
	return extract.GateFromSettings(m), nil
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
        -- Mirrors Go docKey(): "<TYPE>|<NUMBER>", number alone when the type is
        -- missing, source:external_id when the number is missing.
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
    WHERE fd.state = 'complete'
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

// ListFetchDocIDsNeedingNormalizeAfter resolves docs whose Extract stage has
// created a Silver document but Normalize has not yet written the current
// document-level validity marker.
func (a *Activities) ListFetchDocIDsNeedingNormalizeAfter(ctx context.Context, p ListStageFetchDocIDsAfterParams) ([]int64, error) {
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
    WHERE fd.state = 'complete'
      AND fd.in_scope
      AND fd.id > $1
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
          ))
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
    WHERE fd.state = 'complete'
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
		activity.GetLogger(ctx).Warn("schedule source content recheck failed",
			"fetch_doc", fetchDocID, "err", err)
		return
	}
	activity.GetLogger(ctx).Info("scheduled source content recheck",
		"fetch_doc", fetchDocID, "next_attempt_at", bodyNext)
}

func (a *Activities) discoverCongbaoFallback(
	ctx context.Context,
	fd dbingest.IngestFetchDoc,
	sd dbbronze.BronzeSourceDocument,
	reason string,
	now time.Time,
) {
	log := activity.GetLogger(ctx)
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

// upsertSilverDocument writes the logical document row from the bronze observation.
func (a *Activities) upsertSilverDocument(ctx context.Context, sd dbbronze.BronzeSourceDocument, markdown string, now time.Time) (int64, error) {
	var md *string
	if markdown != "" {
		md = &markdown
	}
	var displayNumber *string
	if sd.DocNumber != nil {
		if n := cleanDocNumber(*sd.DocNumber); n != "" {
			displayNumber = &n
		}
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
		IssuedAt:         sd.IssuedAt,
		IsConsolidated:   sd.IsConsolidated,
		Signer:           a.signerFromDetailMeta(ctx, sd.ID),
		Markdown:         md,
		SourceDocumentID: &sd.ID,
		CreatedAt:        now,
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
// file kinds, in kind priority order. Appendices are intentionally excluded from
// primary text selection; they are evidence/supplemental material, not the main
// legal body.
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
		number = docNumberKey(*sd.DocNumber)
	}
	if number == "" {
		return sd.Source + ":" + sd.ExternalID
	}
	if t := docTypeKey(sd.DocType); t != "" {
		return t + "|" + number
	}
	return number
}

// docNumberKey normalizes a số ký hiệu exactly like number-based doc_ref keys
// so a document's number component and incoming number references align.
// Unicode spaces fold to plain spaces first so separator tightening also
// removes NBSP around "/" and "-".
func docNumberKey(number string) string {
	return canonicalDocRefKey(foldSpaces(number))
}

func docTypeKey(docType *string) string {
	if docType == nil {
		return ""
	}
	return strings.ToUpper(strings.Join(strings.Fields(*docType), " "))
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
