package sso

import (
	"context"
	"fmt"
	stdhtml "html"
	"regexp"
	"strings"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

// actAnchorRe matches ALL links whose href starts with /Act/{CODE} (no query
// string). Multiple links share the same href per Act (title, "Add to My
// Collections", PDF, RSS, SL). The first match per code with a non-boilerplate
// title wins — see isBoilerplate.
var (
	actAnchorRe = regexp.MustCompile(`(?is)<a[^>]+href="/Act/([A-Za-z0-9]+)"[^>]*>(.*?)</a>`)
	tagRe       = regexp.MustCompile(`(?s)<[^>]+>`)
	spaceRe     = regexp.MustCompile(`\s+`)
)

var boilerplateTitles = map[string]bool{
	"add to my collections":  true,
	"download pdf":           true,
	"amendments rss feed":    true,
	"subsidiary legislation": true,
}

// letters is the A-Z iteration for discovery.
const letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"

// Discover browses SSO's A-Z Act index and returns each Act with its PDF file.
// The since watermark and keyword are unused (SSO has no incremental feed).
// Scope filtering is handled by the pipeline, not here.
func (s *Source) Discover(ctx context.Context, _ time.Time, _ string) ([]ingest.DiscoveredDoc, error) {
	seen := map[string]bool{}
	var out []ingest.DiscoveredDoc

	for _, letter := range letters {
		pageURL := fmt.Sprintf("%s/Browse/Act/Current/%c?PageSize=100", s.baseURL, letter)
		body, err := s.get(ctx, pageURL)
		if err != nil {
			s.log.Warn("sso browse page failed", "letter", string(letter), "err", err)
			// Continue to next letter on failure (partial results).
			continue
		}

		for _, m := range actAnchorRe.FindAllStringSubmatch(body, -1) {
			code := m[1]
			if code == "" || seen[code] {
				continue
			}
			title := cleanTitle(m[2])
			if title == "" || boilerplateTitles[strings.ToLower(title)] || strings.HasPrefix(strings.ToLower(title), "download pdf") {
				continue
			}
			seen[code] = true

			detailURL := s.baseURL + "/Act/" + code
			pdfURL := detailURL + "?ViewType=Pdf"

			out = append(out, ingest.DiscoveredDoc{
				SourceID:   SourceID,
				ExternalID: code,
				Number:     code,
				Title:      title,
				Abstract:   title,
				DocType:    "Act",
				DetailURL:  detailURL,
				Files: []ingest.FileRef{{
					URL:      pdfURL,
					Name:     title + ".pdf",
					Ext:      "pdf",
					Kind:     "main",
					MIMEType: "application/pdf",
				}},
			})
		}

		if err := sleep(ctx, pacePage); err != nil {
			return out, err
		}
	}

	s.log.Info("sso discover", "acts", len(out))
	return out, nil
}

// cleanTitle strips HTML tags, unescapes entities, and normalizes whitespace.
func cleanTitle(s string) string {
	s = tagRe.ReplaceAllString(s, " ")
	s = stdhtml.UnescapeString(s)
	s = strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
	return s
}
