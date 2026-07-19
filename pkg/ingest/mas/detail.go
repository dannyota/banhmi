package mas

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"danny.vn/banhmi/pkg/ingest"
)

// pdfLinkRe matches PDF download links on MAS detail pages.
// Pattern: href="/-/media/mas-media-library/.../.../filename.pdf"
var pdfLinkRe = regexp.MustCompile(`(?i)href="(/-/media/[^"]+\.pdf[^"]*)"`)

// issuedPursuantRe extracts the "Issued pursuant to" text.
var issuedPursuantRe = regexp.MustCompile(`(?is)Issued\s+pursuant\s+to[:\s]*(.*?)(?:</(?:p|div|li)|<br)`)

// appliesToRe extracts the "Applies to" text.
var appliesToRe = regexp.MustCompile(`(?is)Applies\s+to[:\s]*(.*?)(?:</(?:p|div|li)|<br)`)

// tagStripRe strips HTML tags for extracting plain text.
var tagStripRe = regexp.MustCompile(`<[^>]+>`)

// FetchDetail fetches the MAS detail page and extracts PDF download links and
// metadata. The detail page is server-rendered HTML at the document's page URL.
func (s *Source) FetchDetail(ctx context.Context, ref ingest.DetailRef) (*ingest.DiscoveredDoc, error) {
	if ref.DetailURL == "" {
		return nil, fmt.Errorf("mas detail: empty detail url")
	}

	body, err := s.get(ctx, ref.DetailURL)
	if err != nil {
		return nil, fmt.Errorf("mas detail fetch: %w", err)
	}

	html := string(body)

	// Extract PDF links.
	var files []ingest.FileRef
	seen := map[string]bool{}
	for _, m := range pdfLinkRe.FindAllStringSubmatch(html, -1) {
		pdfPath := m[1]
		absURL := strings.TrimRight(s.baseURL, "/") + pdfPath
		if seen[absURL] {
			continue
		}
		seen[absURL] = true

		name := pdfPath
		if idx := strings.LastIndex(pdfPath, "/"); idx >= 0 {
			name = pdfPath[idx+1:]
		}
		files = append(files, ingest.FileRef{
			URL:      absURL,
			Name:     name,
			Ext:      "pdf",
			Kind:     "main",
			MIMEType: "application/pdf",
		})
	}

	// Extract "Issued pursuant to" and "Applies to" for metadata.
	var issuedPursuant, appliesTo string
	if m := issuedPursuantRe.FindStringSubmatch(html); len(m) > 1 {
		issuedPursuant = cleanHTML(m[1])
	}
	if m := appliesToRe.FindStringSubmatch(html); len(m) > 1 {
		appliesTo = cleanHTML(m[1])
	}

	// Build abstract from extracted metadata.
	var parts []string
	if issuedPursuant != "" {
		parts = append(parts, "Issued pursuant to: "+issuedPursuant)
	}
	if appliesTo != "" {
		parts = append(parts, "Applies to: "+appliesTo)
	}

	doc := &ingest.DiscoveredDoc{
		SourceID:   SourceID,
		ExternalID: ref.ExternalID,
		DetailURL:  ref.DetailURL,
		Files:      files,
	}
	if len(parts) > 0 {
		doc.Abstract = strings.Join(parts, "; ")
	}

	return doc, nil
}

// cleanHTML strips HTML tags and normalizes whitespace.
func cleanHTML(s string) string {
	s = tagStripRe.ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(s), " ")
}
