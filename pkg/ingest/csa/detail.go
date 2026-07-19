package csa

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"danny.vn/banhmi/pkg/ingest"
)

// titleRe matches <title> or <h1> tags for extracting the page title.
var titleRe = regexp.MustCompile(`(?is)<(?:title|h1)[^>]*>(.*?)</(?:title|h1)>`)

// isomerPDFRe matches Isomer CDN PDF links:
// https://isomer-user-content.by.gov.sg/36/{uuid}/{filename}.pdf
var isomerPDFRe = regexp.MustCompile(`https?://isomer-user-content\.by\.gov\.sg/36/([a-f0-9-]+)/([^"'\s]+\.pdf)`)

// tagStripRe matches HTML tags for stripping from title text.
var tagStripRe = regexp.MustCompile(`<[^>]+>`)

// FetchDetail fetches the CSA page HTML and extracts the title and PDF links
// from the Isomer CDN.
func (s *Source) FetchDetail(ctx context.Context, ref ingest.DetailRef) (*ingest.DiscoveredDoc, error) {
	if ref.DetailURL == "" {
		return nil, fmt.Errorf("csa detail: empty detail url")
	}
	body, err := s.get(ctx, ref.DetailURL)
	if err != nil {
		return nil, fmt.Errorf("csa detail %s: %w", ref.ExternalID, err)
	}
	title := extractTitle(body)
	files := extractIsomerPDFs(body)
	return &ingest.DiscoveredDoc{
		SourceID:   SourceID,
		ExternalID: ref.ExternalID,
		Title:      title,
		DetailURL:  ref.DetailURL,
		Files:      files,
	}, nil
}

// extractTitle pulls the page title from <title> or <h1>, preferring <h1>.
func extractTitle(html string) string {
	matches := titleRe.FindAllStringSubmatch(html, -1)
	if len(matches) == 0 {
		return ""
	}
	// Prefer <h1> if found (usually the second match after <title>).
	best := matches[0][1]
	for _, m := range matches {
		tag := strings.ToLower(m[0][:3])
		if tag == "<h1" {
			best = m[1]
			break
		}
	}
	best = tagStripRe.ReplaceAllString(best, "")
	best = strings.TrimSpace(best)
	// Strip site suffix like " | CSA Singapore" or " - CSA".
	if idx := strings.LastIndex(best, " | "); idx > 0 {
		best = strings.TrimSpace(best[:idx])
	}
	if idx := strings.LastIndex(best, " - "); idx > 0 {
		best = strings.TrimSpace(best[:idx])
	}
	return best
}

// extractIsomerPDFs finds Isomer CDN PDF links in the HTML and returns FileRefs.
func extractIsomerPDFs(html string) []ingest.FileRef {
	seen := map[string]bool{}
	var files []ingest.FileRef
	for _, m := range isomerPDFRe.FindAllStringSubmatch(html, -1) {
		fullURL := m[0]
		filename := m[2]
		if seen[fullURL] {
			continue
		}
		seen[fullURL] = true
		files = append(files, ingest.FileRef{
			URL:      fullURL,
			Name:     filename,
			Ext:      "pdf",
			Kind:     "main",
			MIMEType: "application/pdf",
		})
	}
	return files
}
