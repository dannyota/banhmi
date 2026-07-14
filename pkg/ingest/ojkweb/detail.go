package ojkweb

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

// Detail page parse patterns for ojk.go.id.
var (
	// titleRe extracts the page title from <h1 class="title">.
	titleRe = regexp.MustCompile(`(?is)<h1[^>]*class="title"[^>]*>(.*?)</h1>`)

	// nomorRe extracts the regulation number from the display span.
	nomorRe = regexp.MustCompile(`(?is)<span[^>]*class="[^"]*nomor-regulasi-display[^"]*"[^>]*>(.*?)</span>`)

	// effectiveDateRe extracts the effective date (tanggal-2) in M/D/YYYY.
	effectiveDateRe = regexp.MustCompile(`(?is)<span[^>]*class="[^"]*display-date-text\s+tanggal-2[^"]*"[^>]*>(.*?)</span>`)

	// issueDateRe extracts the issue date (tanggal-1) in M/D/YYYY.
	issueDateRe = regexp.MustCompile(`(?is)<span[^>]*class="[^"]*display-date-text\s+tanggal-1[^"]*"[^>]*>(.*?)</span>`)

	// sektorRe extracts the sector labels.
	sektorRe = regexp.MustCompile(`(?is)<span[^>]*class="[^"]*sektor-regulasi-display[^"]*"[^>]*>(.*?)</span>`)

	// subSektorRe extracts the sub-sector labels.
	subSektorRe = regexp.MustCompile(`(?is)<span[^>]*class="[^"]*subsektor-regulasi-display[^"]*"[^>]*>(.*?)</span>`)

	// jenisDisplayRe extracts the regulation type label from the display area.
	jenisDisplayRe = regexp.MustCompile(`(?is)<span[^>]*class="[^"]*list-regulasi-display[^"]*"[^>]*>(.*?)</span>`)

	// pdfLinkRe extracts PDF download links from the detail page. The href
	// is a relative path under /id/regulasi/Documents/Pages/.
	pdfLinkRe = regexp.MustCompile(`(?is)<a[^>]*href="(/id/regulasi/Documents/Pages/[^"]*\.pdf)"[^>]*>`)

	// mdyDateRe parses M/D/YYYY dates (American format used by SharePoint).
	mdyDateRe = regexp.MustCompile(`^(\d{1,2})/(\d{1,2})/(\d{4})$`)
)

// FetchDetail fetches and parses a detail page for a document, returning the
// enriched DiscoveredDoc with metadata and PDF file references.
func (s *Source) FetchDetail(ctx context.Context, ref ingest.DetailRef) (*ingest.DiscoveredDoc, error) {
	u := ref.DetailURL
	if u == "" {
		return nil, fmt.Errorf("fetch detail %s: no detail url", ref.ExternalID)
	}

	body, err := s.client.Get(ctx, u)
	if err != nil {
		return nil, fmt.Errorf("fetch detail %s: %w", ref.ExternalID, err)
	}

	return parseDetail(body, ref.ExternalID, u)
}

// parseDetail parses a detail page HTML into a DiscoveredDoc.
func parseDetail(body, externalID, pageURL string) (*ingest.DiscoveredDoc, error) {
	doc := &ingest.DiscoveredDoc{
		SourceID:   SourceID,
		ExternalID: externalID,
		DetailURL:  pageURL,
	}

	// Title from <h1 class="title">.
	if m := titleRe.FindStringSubmatch(body); m != nil {
		doc.Title = cleanText(m[1])
	}

	// Number from <span class="nomor-regulasi-display">.
	if m := nomorRe.FindStringSubmatch(body); m != nil {
		doc.Number = cleanText(m[1])
	}

	// DocType from the display label.
	if m := jenisDisplayRe.FindStringSubmatch(body); m != nil {
		doc.DocType = ingest.DocType(cleanText(m[1]))
	}

	// Effective date (tanggal-2).
	if m := effectiveDateRe.FindStringSubmatch(body); m != nil {
		doc.EffectiveAt = parseMDYDate(cleanText(m[1]))
	}

	// Issue date (tanggal-1).
	if m := issueDateRe.FindStringSubmatch(body); m != nil {
		doc.IssuedAt = parseMDYDate(cleanText(m[1]))
	}

	// Sector as abstract.
	if m := sektorRe.FindStringSubmatch(body); m != nil {
		doc.Abstract = cleanText(m[1])
	}

	// Sub-sector: append to abstract if present.
	if m := subSektorRe.FindStringSubmatch(body); m != nil {
		sub := cleanText(m[1])
		if sub != "" && doc.Abstract != "" {
			doc.Abstract = doc.Abstract + "; " + sub
		} else if sub != "" {
			doc.Abstract = sub
		}
	}

	// PDF links.
	doc.Files = parsePDFLinks(body)

	return doc, nil
}

// parsePDFLinks extracts all PDF download links from the detail page HTML.
func parsePDFLinks(body string) []ingest.FileRef {
	var files []ingest.FileRef
	seen := map[string]bool{}

	for _, m := range pdfLinkRe.FindAllStringSubmatch(body, -1) {
		href := m[1]
		if seen[href] {
			continue
		}
		seen[href] = true

		fullURL := baseURL + href
		name := href
		if idx := strings.LastIndex(href, "/"); idx >= 0 {
			name = href[idx+1:]
		}

		files = append(files, ingest.FileRef{
			URL:      fullURL,
			Name:     name,
			Ext:      fileExt(name),
			Kind:     "main",
			MIMEType: "application/pdf",
		})
	}
	return files
}

// parseMDYDate parses an M/D/YYYY date string (American format used by
// SharePoint) and returns the UTC time. Returns zero time on parse failure.
func parseMDYDate(s string) time.Time {
	m := mdyDateRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return time.Time{}
	}
	month, _ := strconv.Atoi(m[1])
	day, _ := strconv.Atoi(m[2])
	year, _ := strconv.Atoi(m[3])
	if month < 1 || month > 12 || day < 1 || day > 31 {
		return time.Time{}
	}
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
}

// fileExt returns the lowercase extension without the dot.
func fileExt(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return strings.ToLower(name[i+1:])
	}
	return ""
}
