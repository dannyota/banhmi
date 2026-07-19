package nbc

import (
	"context"
	"regexp"
	"strings"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

// legislationPages are the NBC portal pages that list regulations.
var legislationPages = []struct {
	Path    string
	DocType string
}{
	// English variants only (verified 2026-07-20): the non-/english/ pages are
	// the Khmer UI and link only *_kh PDFs — crawling them either yields
	// filtered Khmer files or misses the English documents entirely (the IT
	// guidelines and banking codes live under the /english/ paths).
	{"/english/legislation/prakas_new.php", "Prakas"},
	{"/english/legislation/laws_applicable_to_banks_and_financial_institutions.php", "Law"},
	{"/english/publications/guidelines_it_policy.php", "IT Guideline"},
	{"/english/publications/banking_code.php", "Banking Code"},
}

// pdfAnchorRe matches PDF anchor tags on NBC pages and captures:
//
//	[1] the PDF URL (relative or absolute)
//	[2] the inner HTML of the anchor (includes title text + optional date span)
//
// NBC pages use single-quoted hrefs. The regex handles both quote styles.
var pdfAnchorRe = regexp.MustCompile(`(?is)<a\s[^>]*href=["']((?:https?://www\.nbc\.gov\.kh)?/?(?:\.\./)*download_files/[^"']+\.pdf)["'][^>]*>(.*?)</a>`)

// htmlTagRe strips HTML tags from anchor inner text.
var htmlTagRe = regexp.MustCompile(`<[^>]+>`)

// Discover scrapes NBC's legislation pages for PDF links.
// The since and keyword parameters are ignored — the NBC corpus is small
// enough to scrape the full set.
func (s *Source) Discover(ctx context.Context, _ time.Time, _ string) ([]ingest.DiscoveredDoc, error) {
	seen := map[string]bool{}
	var docs []ingest.DiscoveredDoc

	for _, page := range legislationPages {
		pageURL := s.baseURL + page.Path
		body, err := s.get(ctx, pageURL)
		if err != nil {
			s.log.Warn("nbc page fetch failed", "page", page.Path, "err", err)
			continue
		}

		for _, m := range pdfAnchorRe.FindAllStringSubmatch(body, -1) {
			rawPath := m[1]
			anchorHTML := m[2]
			pdfURL := rawPath
			if strings.HasPrefix(pdfURL, "../") {
				pdfURL = "/" + strings.TrimLeft(pdfURL, "./")
			}
			if !strings.HasPrefix(pdfURL, "http") {
				pdfURL = s.baseURL + pdfURL
			}
			// Skip Khmer PDFs (English corpus). Markers seen on the live
			// site: laws_kh/ and itguideline_kh/ dirs, *_kh.pdf and *-KH.pdf
			// suffixes, in mixed case.
			lower := strings.ToLower(pdfURL)
			if strings.Contains(lower, "_kh/") || strings.Contains(lower, "_kh.") || strings.Contains(lower, "-kh.") {
				continue
			}
			if seen[pdfURL] {
				continue
			}
			seen[pdfURL] = true

			slug := pdfSlug(pdfURL)
			title := anchorTitle(anchorHTML)
			if title == "" {
				title = slugToTitle(slug)
			}

			docs = append(docs, ingest.DiscoveredDoc{
				SourceID:   SourceID,
				ExternalID: slug,
				Number:     slug,
				Title:      title,
				Abstract:   title,
				DocType:    ingest.DocType(page.DocType),
				// FetchDetail's contract: DetailURL IS the PDF URL (NBC has no
				// per-document page). The listing page here broke fetch
				// planning — every doc downloaded the listing HTML as its
				// "main PDF".
				DetailURL: pdfURL,
				Files: []ingest.FileRef{{
					URL:      pdfURL,
					Name:     slug + ".pdf",
					Ext:      "pdf",
					Kind:     "main",
					MIMEType: "application/pdf",
				}},
			})
		}
	}

	s.log.Info("nbc discover", "docs", len(docs))
	return docs, nil
}

// pdfSlug extracts a stable slug from a PDF URL path.
func pdfSlug(pdfURL string) string {
	pdfURL = strings.TrimRight(pdfURL, "/")
	if idx := strings.LastIndex(pdfURL, "/"); idx >= 0 {
		pdfURL = pdfURL[idx+1:]
	}
	pdfURL = strings.TrimSuffix(pdfURL, ".pdf")
	pdfURL = strings.TrimSuffix(pdfURL, ".PDF")
	return pdfURL
}

// anchorTitle extracts a clean document title from anchor inner HTML.
// NBC anchors look like: "PRAKAS ON CAPITAL BUFFER, <span ...>January 9, 2026</span>"
// — strip HTML tags and the trailing comma-separated date phrase.
func anchorTitle(html string) string {
	// Strip HTML tags (e.g. <span>, <i>).
	text := htmlTagRe.ReplaceAllString(html, "")
	text = strings.TrimSpace(text)
	// Remove trailing date — usually after last comma: ", January 9, 2026"
	// or ", 01 January 2026". Find the last segment that looks like a date.
	if idx := strings.LastIndex(text, ","); idx > 0 {
		tail := strings.TrimSpace(text[idx+1:])
		// If tail is empty or looks like a date (starts with month or digit),
		// trim everything from the last meaningful comma.
		if tail == "" || isDateish(tail) {
			text = strings.TrimSpace(text[:idx])
			// Handle "Title, Month DD, YYYY" — the month is after another comma.
			if idx2 := strings.LastIndex(text, ","); idx2 > 0 {
				tail2 := strings.TrimSpace(text[idx2+1:])
				if isDateish(tail2) {
					text = strings.TrimSpace(text[:idx2])
				}
			}
		}
	}
	text = strings.TrimRight(text, ", ")
	// If what remains is itself a date fragment (e.g. "January 9"), discard.
	if text == "" || isDateish(text) {
		return ""
	}
	return text
}

// isDateish returns true if s starts with a month name or a digit (date fragment).
func isDateish(s string) bool {
	if s == "" {
		return false
	}
	if s[0] >= '0' && s[0] <= '9' {
		return true
	}
	months := []string{"january", "february", "march", "april", "may", "june",
		"july", "august", "september", "october", "november", "december"}
	low := strings.ToLower(s)
	for _, m := range months {
		if strings.HasPrefix(low, m) {
			return true
		}
	}
	return false
}

// slugToTitle converts a filename slug to a readable title.
func slugToTitle(slug string) string {
	s := strings.ReplaceAll(slug, "-", " ")
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.TrimSpace(s)
	if s == "" {
		return slug
	}
	words := strings.Fields(s)
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}
