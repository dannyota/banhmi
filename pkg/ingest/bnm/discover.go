package bnm

import (
	"context"
	stdhtml "html"
	"path"
	"regexp"
	"strings"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

// inScopeSectors are the BNM listing pages banhmi crawls for banking digital/tech
// regulation. The pages mix tech and non-tech policy documents; the pipeline's
// scope.Match keeps the tech subset (the BNM signal is injected into Number, so the
// MY weak tech terms — technology/cloud/outsourcing/electronic… — fire here).
var inScopeSectors = []string{
	"/banking-islamic-banking",
	"/payment-systems",
}

var (
	rowSplitRe = regexp.MustCompile(`(?i)<tr[\s>]`)
	pdfLinkRe  = regexp.MustCompile(`(?is)<a[^>]+href="([^"]*/documents/[^"]+\.pdf)"[^>]*>(.*?)</a>`)
	dateRe     = regexp.MustCompile(`(\d{1,2}\s+[A-Za-z]{3}\s+\d{4})`)
	badgeRe    = regexp.MustCompile(`(?is)<div[^>]*class="[^"]*badge[^"]*"[^>]*>(.*?)</div>`)
	tagRe      = regexp.MustCompile(`(?s)<[^>]+>`)
	spaceRe    = regexp.MustCompile(`\s+`)
)

// Discover crawls the in-scope BNM sector listings and returns each policy document
// (the row's direct PDF link). Triggered with an empty keyword so the pipeline's
// scope.Match filters to the tech subset; each doc's Number carries the "BNM" signal
// so the MY weak tech terms count.
func (s *Source) Discover(ctx context.Context, _ time.Time, _ string) ([]ingest.DiscoveredDoc, error) {
	seen := map[string]bool{}
	var out []ingest.DiscoveredDoc
	for _, sec := range inScopeSectors {
		body, err := s.get(ctx, s.baseURL+sec)
		if err != nil {
			s.log.Warn("bnm sector fetch failed", "sector", sec, "err", err)
			continue
		}
		out = append(out, parseSector(body, s.baseURL, sec, seen)...)
		if err := sleep(ctx, pacePage); err != nil {
			return out, err
		}
	}
	s.log.Info("bnm discover", "docs", len(out), "sectors", len(inScopeSectors))
	return out, nil
}

// parseSector parses one listing page's rows into discovered documents.
func parseSector(body, baseURL, sector string, seen map[string]bool) []ingest.DiscoveredDoc {
	var out []ingest.DiscoveredDoc
	for _, row := range rowSplitRe.Split(body, -1) {
		m := pdfLinkRe.FindStringSubmatch(row)
		if m == nil {
			continue
		}
		pdfURL := absURL(baseURL, m[1])
		title := cleanText(m[2])
		if title == "" {
			continue
		}
		ext := strings.TrimSuffix(path.Base(pdfURL), ".pdf")
		if pdfURL == "" || seen[pdfURL] {
			continue
		}
		seen[pdfURL] = true

		var issued time.Time
		if dm := dateRe.FindStringSubmatch(stripHidden(row)); dm != nil {
			issued = parseBNMDate(dm[1])
		}
		docType := ingest.DocType("Policy Document")
		if bm := badgeRe.FindStringSubmatch(row); bm != nil {
			if t := cleanText(bm[1]); t != "" {
				docType = ingest.DocType(t)
			}
		}
		out = append(out, ingest.DiscoveredDoc{
			SourceID:    SourceID,
			ExternalID:  pdfPath(pdfURL),
			Number:      "BNM/" + ext, // carries the "bnm" scope signal
			Title:       title,
			Abstract:    title,
			DocType:     docType,
			IssuedAt:    issued,
			PublishedAt: issued,
			DetailURL:   baseURL + sector,
			Files: []ingest.FileRef{{
				URL: pdfURL, Name: path.Base(pdfURL), Ext: "pdf", Kind: "main", MIMEType: "application/pdf",
			}},
		})
	}
	return out
}

// stripHidden removes the sortable <span class="hidden">YYYY/MM/DD</span> so the
// human "27 Mar 2026" date is what dateRe matches.
func stripHidden(row string) string {
	return regexp.MustCompile(`(?is)<span class="hidden">.*?</span>`).ReplaceAllString(row, " ")
}

func absURL(baseURL, href string) string {
	if strings.HasPrefix(href, "http") {
		return href
	}
	return strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(href, "/")
}

// pdfPath returns the path portion of a PDF URL — a stable external id independent
// of the host scheme.
func pdfPath(u string) string {
	if i := strings.Index(u, "/documents/"); i >= 0 {
		return u[i:]
	}
	return u
}

func cleanText(s string) string {
	s = tagRe.ReplaceAllString(s, " ")
	s = stdhtml.UnescapeString(s)
	return strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
}

func parseBNMDate(s string) time.Time {
	if t, err := time.Parse("2 Jan 2006", strings.TrimSpace(s)); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse("02 Jan 2006", strings.TrimSpace(s)); err == nil {
		return t.UTC()
	}
	return time.Time{}
}
