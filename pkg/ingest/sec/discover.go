package sec

import (
	"context"
	"errors"
	"fmt"
	stdhtml "html"
	"regexp"
	"strconv"
	"strings"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

// inScopeCategories are the NRS categories banhmi crawls — digital-asset and
// IT-system regulations relevant to banking/financial technology regulation.
var inScopeCategories = []struct {
	code string
	name string
}{
	{"1299", "สินทรัพย์ดิจิทัล"},                 // Digital Assets
	{"1346", "การจัดให้มีระบบเทคโนโลยีสารสนเทศ"}, // IT Systems
}

// Parse patterns for the NRS search result HTML table.
var (
	// rowRe matches one <tr> row in the results table. The SEC NRS returns all
	// results inline in one HTML page (no pagination).
	rowRe = regexp.MustCompile(`(?is)<tr[^>]*>(.*?)</tr>`)

	// cellRe extracts <td> cell contents.
	cellRe = regexp.MustCompile(`(?is)<td[^>]*>(.*?)</td>`)

	// nrsIDRe extracts the NRS ID from an OpenWindow JS call or from the title
	// parenthetical. Examples:
	//   OpenWindow('11113', ...)
	//   (11113)
	openWindowRe = regexp.MustCompile(`OpenWindow\s*\(\s*'(\d+)'`)
	titleIDRe    = regexp.MustCompile(`\((\d{3,6})\)\s*$`)

	// fileLinkRe matches download links to publish.sec.or.th.
	fileLinkRe = regexp.MustCompile(`(?i)href\s*=\s*['"]?(https?://publish\.sec\.or\.th/nrs/(\d+)([^'">\s]*))['">\s]`)

	// statusActiveImg matches the "currently in force" icon.
	statusActiveRe = regexp.MustCompile(`(?i)ready_flag\.png`)

	// statusExpiredRe matches the expired text marker.
	statusExpiredRe = regexp.MustCompile(`(?i)สิ้นผลใช้บังคับ`)

	// dateRe matches DD/MM/YYYY in Buddhist Era calendar.
	dateRe = regexp.MustCompile(`(\d{1,2})/(\d{1,2})/(\d{4})`)

	tagRe   = regexp.MustCompile(`<[^>]+>`)
	spaceRe = regexp.MustCompile(`\s+`)
)

// Discover enumerates the in-scope NRS categories and returns all discovered
// documents. SEC is sweep-all — the keyword parameter is ignored. The since
// watermark is applied against the signed date (วันที่ลงนาม) when available.
func (s *Source) Discover(ctx context.Context, since time.Time, _ string) ([]ingest.DiscoveredDoc, error) {
	seen := map[string]bool{}
	var (
		out     []ingest.DiscoveredDoc
		errs    []error
		nFailed int
	)

	for i, cat := range inScopeCategories {
		if i > 0 {
			if err := sleep(ctx, paceCat); err != nil {
				return out, err
			}
		}
		docs, err := s.discoverCategory(ctx, cat.code, since)
		if err != nil {
			s.log.Warn("sec category discover failed", "cat", cat.code, "name", cat.name, "err", err)
			errs = append(errs, fmt.Errorf("category %s (%s): %w", cat.code, cat.name, err))
			nFailed++
			continue
		}
		for _, d := range docs {
			if seen[d.ExternalID] {
				continue
			}
			seen[d.ExternalID] = true
			out = append(out, d)
		}
	}

	s.log.Info("sec discover", "docs", len(out), "categories", len(inScopeCategories))
	if nFailed > 0 {
		return out, fmt.Errorf("sec discover: %d of %d categories failed: %w",
			nFailed, len(inScopeCategories), errors.Join(errs...))
	}
	return out, nil
}

// discoverCategory POSTs one NRS search form and parses the result table.
func (s *Source) discoverCategory(ctx context.Context, catCode string, since time.Time) ([]ingest.DiscoveredDoc, error) {
	form := "chkType=1&SearchType=&chkCat=1&SearchCat=" + catCode + "&chkPost=1"

	rawBody, err := s.post(ctx, discoveryBaseURL+nrsSearchPath, strings.NewReader(form))
	if err != nil {
		return nil, fmt.Errorf("post NRS search cat=%s: %w", catCode, err)
	}

	body, err := decodeCP874(rawBody)
	if err != nil {
		return nil, fmt.Errorf("decode CP874 cat=%s: %w", catCode, err)
	}

	return parseNRSTable(body, since), nil
}

// parseNRSTable extracts documents from the NRS search result HTML.
func parseNRSTable(body string, since time.Time) []ingest.DiscoveredDoc {
	rows := rowRe.FindAllStringSubmatch(body, -1)
	var out []ingest.DiscoveredDoc

	for _, row := range rows {
		cells := cellRe.FindAllStringSubmatch(row[1], -1)
		if len(cells) < 3 {
			continue
		}

		doc, ok := parseNRSRow(cells)
		if !ok {
			continue
		}

		// Watermark: skip documents signed on or before `since`.
		if !since.IsZero() && !doc.IssuedAt.IsZero() && !doc.IssuedAt.After(since) {
			continue
		}

		out = append(out, doc)
	}

	return out
}

// parseNRSRow maps one table row (as extracted cells) to a DiscoveredDoc.
// NRS table columns (observed):
//
//	[0] document type + number (e.g. "ประกาศคณะกรรมการ ก.ล.ต. ที่ กม. 3/2568")
//	[1] title (Thai text, NRS ID in parentheses at end)
//	[2] section reference (มาตรา)
//	[3] file download links
//	[4] status icon/text
//	[5] date signed (วันที่ลงนาม) DD/MM/YYYY Buddhist Era
//	[6] effective date
//
// Column indices may shift; extraction is best-effort from whatever cells exist.
func parseNRSRow(cells [][]string) (ingest.DiscoveredDoc, bool) {
	// Collect all cell texts for scanning.
	var cellTexts []string
	var cellHTMLs []string
	for _, c := range cells {
		cellHTMLs = append(cellHTMLs, c[1])
		cellTexts = append(cellTexts, cleanText(c[1]))
	}

	// Extract NRS ID: try OpenWindow links first, then title parenthetical.
	nrsID := extractNRSID(strings.Join(cellHTMLs, " "))
	if nrsID == "" {
		return ingest.DiscoveredDoc{}, false
	}

	// Real NRS layout (validated on live rows 2026-07-26): cell 0 is the row
	// ordinal ("70."), cell 1 the notification designation ("ประกาศคณะกรรมการ
	// ก.ล.ต. ที่ กธ. 35/2563"), cell 2 the subject line (ending "(NRSID)").
	// The original mapping (0=number, 1=title) fed the scope gate designations
	// instead of subjects, so every SEC document was vocabulary-rejected.
	docTypeNumber := ""
	if len(cellTexts) > 1 {
		docTypeNumber = cellTexts[1]
	}

	title := ""
	if len(cellTexts) > 2 {
		title = titleIDRe.ReplaceAllString(cellTexts[2], "")
		title = strings.TrimSpace(title)
	}
	if title == "" {
		// Form/attachment rows carry no subject cell — keep the designation so
		// the document is at least identifiable.
		title = docTypeNumber
	}

	// Status: scan all cells for the active/expired markers.
	status := "active"
	allHTML := strings.Join(cellHTMLs, " ")
	if statusExpiredRe.MatchString(allHTML) {
		status = "expired"
	} else if !statusActiveRe.MatchString(allHTML) {
		// No explicit marker found — default to active.
		status = "active"
	}

	// Date signed: find the first DD/MM/YYYY (Buddhist Era) in the row.
	issuedAt := parseBEDate(allHTML)

	// Dates: cell 6 = signed, cell 7 = effective (both Buddhist Era).
	var effectiveAt time.Time
	if len(cellHTMLs) > 7 {
		effectiveAt = parseBEDate(cellHTMLs[7])
	}

	// File references from publish.sec.or.th links.
	files := extractFileRefs(allHTML, nrsID)

	return ingest.DiscoveredDoc{
		SourceID:    SourceID,
		ExternalID:  nrsID,
		Number:      docTypeNumber,
		Title:       title,
		DocType:     "SEC Notification",
		Status:      status,
		IssuedAt:    issuedAt,
		EffectiveAt: effectiveAt,
		DetailURL:   discoveryBaseURL + nrsSearchPath,
		Files:       files,
	}, true
}

// extractNRSID finds the NRS ID from OpenWindow calls or title parenthetical.
func extractNRSID(html string) string {
	if m := openWindowRe.FindStringSubmatch(html); m != nil {
		return m[1]
	}
	if m := titleIDRe.FindStringSubmatch(cleanText(html)); m != nil {
		return m[1]
	}
	return ""
}

// extractFileRefs finds publish.sec.or.th download links and builds the file
// cascade: prefer DOCX → readable PDF → signed PDF.
func extractFileRefs(html, nrsID string) []ingest.FileRef {
	matches := fileLinkRe.FindAllStringSubmatch(html, -1)
	if len(matches) == 0 {
		// Fall back to constructing the signed PDF URL from NRS ID.
		return []ingest.FileRef{signedPDFRef(nrsID)}
	}

	var (
		docxRef *ingest.FileRef
		pdfRRef *ingest.FileRef
		pdfSRef *ingest.FileRef
		others  []ingest.FileRef
	)

	seen := map[string]bool{}
	for _, m := range matches {
		fileURL := m[1]
		suffix := strings.ToLower(m[3])
		if seen[fileURL] {
			continue
		}
		seen[fileURL] = true

		ref := ingest.FileRef{
			URL:  fileURL,
			Kind: "main",
		}

		switch {
		case strings.HasSuffix(suffix, ".docx"):
			ref.Name = nrsID + "p.docx"
			ref.Ext = "docx"
			ref.MIMEType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
			docxRef = &ref
		case strings.HasSuffix(suffix, ".doc"):
			ref.Name = nrsID + "p.doc"
			ref.Ext = "doc"
			ref.MIMEType = "application/msword"
			others = append(others, ref)
		case strings.Contains(suffix, "p_r.pdf"):
			ref.Name = nrsID + "p_r.pdf"
			ref.Ext = "pdf"
			ref.MIMEType = "application/pdf"
			pdfRRef = &ref
		case strings.HasSuffix(suffix, "s.pdf"):
			ref.Name = nrsID + "s.pdf"
			ref.Ext = "pdf"
			ref.MIMEType = "application/pdf"
			ref.Kind = "original_scan"
			pdfSRef = &ref
		default:
			ref.Name = nrsID + suffix
			ref.Ext = fileExt(suffix)
			ref.MIMEType = "application/pdf"
			others = append(others, ref)
		}
	}

	// Build cascade: DOCX first (best), then readable PDF, then signed PDF.
	var out []ingest.FileRef
	if docxRef != nil {
		out = append(out, *docxRef)
	}
	if pdfRRef != nil {
		if docxRef != nil {
			pdfRRef.Kind = "attachment"
		}
		out = append(out, *pdfRRef)
	}
	if pdfSRef != nil {
		out = append(out, *pdfSRef)
	}
	out = append(out, others...)

	if len(out) == 0 {
		return []ingest.FileRef{signedPDFRef(nrsID)}
	}
	return out
}

// signedPDFRef returns a FileRef for the standard signed PDF URL.
func signedPDFRef(nrsID string) ingest.FileRef {
	return ingest.FileRef{
		URL:      publishBaseURL + "/nrs/" + nrsID + "s.pdf",
		Name:     nrsID + "s.pdf",
		Ext:      "pdf",
		Kind:     "main",
		MIMEType: "application/pdf",
	}
}

// parseBEDate extracts the first DD/MM/YYYY date in Buddhist Era from text
// and converts to CE (year - 543).
func parseBEDate(s string) time.Time {
	m := dateRe.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}
	}
	day, _ := strconv.Atoi(m[1])
	month, _ := strconv.Atoi(m[2])
	beYear, _ := strconv.Atoi(m[3])
	if month < 1 || month > 12 || day < 1 || day > 31 {
		return time.Time{}
	}
	// Buddhist Era to Common Era: subtract 543.
	ceYear := beYear - 543
	if ceYear < 1900 || ceYear > 2100 {
		return time.Time{}
	}
	return time.Date(ceYear, time.Month(month), day, 0, 0, 0, 0, time.UTC)
}

// cleanText strips HTML tags, unescapes entities, and collapses whitespace.
func cleanText(s string) string {
	s = tagRe.ReplaceAllString(s, " ")
	s = stdhtml.UnescapeString(s)
	s = strings.ReplaceAll(s, " ", " ")
	return strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
}

// fileExt returns the lowercase extension from a suffix string, without the dot.
func fileExt(suffix string) string {
	if i := strings.LastIndex(suffix, "."); i >= 0 {
		return strings.ToLower(suffix[i+1:])
	}
	return ""
}
