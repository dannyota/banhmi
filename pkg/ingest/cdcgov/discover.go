package cdcgov

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

// pdfLinkRe matches PDF download links on the CDC laws page.
// URLs are like: https://cdc.gov.kh/wp-content/uploads/2020/07/Law-on-Banking.pdf
var pdfLinkRe = regexp.MustCompile(`href=["'](https?://cdc\.gov\.kh/wp-content/uploads/[^"']+\.pdf)["']`)

// Discover scrapes the CDC laws-and-regulations page for PDF links.
// The since and keyword parameters are ignored — the CDC corpus is small
// enough to fetch the full page each time.
func (s *Source) Discover(ctx context.Context, _ time.Time, _ string) ([]ingest.DiscoveredDoc, error) {
	pageURL := s.baseURL + "/laws-and-regulations/"
	body, err := s.get(ctx, pageURL)
	if err != nil {
		return nil, fmt.Errorf("cdc discover: fetch page: %w", err)
	}

	seen := map[string]bool{}
	var docs []ingest.DiscoveredDoc

	for _, m := range pdfLinkRe.FindAllStringSubmatch(body, -1) {
		pdfURL := m[1]
		if seen[pdfURL] {
			continue
		}
		seen[pdfURL] = true

		slug := pdfSlug(pdfURL)
		title := slugToTitle(slug)

		docs = append(docs, ingest.DiscoveredDoc{
			SourceID:   SourceID,
			ExternalID: slug,
			Number:     slug,
			Title:      title,
			Abstract:   title,
			DocType:    "Legislation",
			DetailURL:  pageURL,
			Files: []ingest.FileRef{{
				URL:      pdfURL,
				Name:     slug + ".pdf",
				Ext:      "pdf",
				Kind:     "main",
				MIMEType: "application/pdf",
			}},
		})
	}

	s.log.Info("cdc discover", "docs", len(docs))
	return docs, nil
}

// pdfSlug extracts a stable slug from a PDF URL path.
// e.g. "https://cdc.gov.kh/wp-content/uploads/2020/07/Law-on-Banking.pdf" -> "Law-on-Banking"
func pdfSlug(pdfURL string) string {
	pdfURL = strings.TrimRight(pdfURL, "/")
	if idx := strings.LastIndex(pdfURL, "/"); idx >= 0 {
		pdfURL = pdfURL[idx+1:]
	}
	pdfURL = strings.TrimSuffix(pdfURL, ".pdf")
	pdfURL = strings.TrimSuffix(pdfURL, ".PDF")
	return pdfURL
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
