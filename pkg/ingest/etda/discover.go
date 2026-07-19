package etda

import (
	"context"
	"errors"
	"fmt"
	stdhtml "html"
	"regexp"
	"strings"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

// inScopePages are the ETDA listing pages banhmi crawls — digital platform,
// digital ID, and recommendations/standards. Server-rendered ASP.NET; all
// documents render on one page per section (no pagination).
var inScopePages = []string{
	"/th/regulator/Digitalplatform/law.aspx",
	"/th/regulator/DigitalID/law.aspx",
	"/th/Our-Service/Recommendation.aspx",
}

// attachRe matches ETDA's getattachment PDF links:
//
//	href="/getattachment/{GUID}/{filename}.pdf.aspx"
//	href="https://www.etda.or.th/getattachment/{GUID}/{filename}.aspx"
//
// Captures: (1) GUID, (2) filename (may include .pdf before .aspx).
var (
	attachRe = regexp.MustCompile(`(?is)<a[^>]+href="[^"]*getattachment/([a-fA-F0-9-]+)/([^"]+?\.(?:pdf\.aspx|aspx)(?:\?[^"]*)?)"[^>]*>(.*?)</a>`)
	tagRe    = regexp.MustCompile(`(?s)<[^>]+>`)
	spaceRe  = regexp.MustCompile(`\s+`)
)

// Discover crawls the in-scope ETDA listing pages and returns each linked PDF
// document. Deduplicates by GUID (the same PDF may appear on multiple pages).
//
// Partial failure contract: if any page fetch fails, Discover still collects
// docs from successful pages and returns them alongside a non-nil error.
func (s *Source) Discover(ctx context.Context, _ time.Time, _ string) ([]ingest.DiscoveredDoc, error) {
	seenGUID := map[string]bool{}
	var (
		out     []ingest.DiscoveredDoc
		errs    []error
		nFailed int
	)
	for _, page := range inScopePages {
		body, err := s.get(ctx, s.baseURL+page)
		if err != nil {
			s.log.Warn("etda page fetch failed", "page", page, "err", err)
			errs = append(errs, fmt.Errorf("page %s: %w", page, err))
			nFailed++
			continue
		}
		for _, m := range attachRe.FindAllStringSubmatch(body, -1) {
			guid := strings.ToLower(m[1])
			if guid == "" || seenGUID[guid] {
				continue
			}
			seenGUID[guid] = true

			filename := m[2]
			title := cleanTitle(m[3])
			if title == "" {
				title = filename
			}

			fullURL := s.baseURL + "/getattachment/" + m[1] + "/" + filename

			out = append(out, ingest.DiscoveredDoc{
				SourceID:   SourceID,
				ExternalID: guid,
				Number:     "",
				Title:      title,
				Abstract:   title,
				DocType:    "Regulation",
				DetailURL:  s.baseURL + page,
				Files: []ingest.FileRef{{
					URL:      fullURL,
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
	s.log.Info("etda discover", "docs", len(out), "pages", len(inScopePages))
	if nFailed > 0 {
		return out, fmt.Errorf("etda discover: %d of %d pages failed: %w", nFailed, len(inScopePages), errors.Join(errs...))
	}
	return out, nil
}

func cleanTitle(s string) string {
	s = tagRe.ReplaceAllString(s, " ")
	s = stdhtml.UnescapeString(s)
	return strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
}
