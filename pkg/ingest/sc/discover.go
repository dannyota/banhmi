package sc

import (
	"context"
	stdhtml "html"
	"regexp"
	"strings"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

// docAnchorRe matches a document download link: <a href="…download.ashx?id=GUID">Title (pdf)</a>.
// GUIDs are hex-with-dashes, mixed case.
var (
	docAnchorRe = regexp.MustCompile(`(?is)<a[^>]+href="[^"]*download\.ashx\?id=([a-fA-F0-9-]+)"[^>]*>(.*?)</a>`)
	tagRe       = regexp.MustCompile(`(?s)<[^>]+>`)
	spaceRe     = regexp.MustCompile(`\s+`)
	pdfSuffixRe = regexp.MustCompile(`(?i)\s*\((pdf|docx?|xlsx?)\)\s*$`)
)

// Discover crawls the in-scope SC sections and returns each linked document. SC is
// in-scope by construction (only technology/digital sections are crawled), so it is
// triggered with a keyword and the pipeline's keyword-bypass treats every doc as in
// scope. The keyword is provenance only; the section list is fixed.
func (s *Source) Discover(ctx context.Context, _ time.Time, _ string) ([]ingest.DiscoveredDoc, error) {
	seen := map[string]bool{}
	var out []ingest.DiscoveredDoc
	for _, sec := range inScopeSections {
		body, err := s.get(ctx, s.baseURL+sec)
		if err != nil {
			s.log.Warn("sc section fetch failed", "section", sec, "err", err)
			continue
		}
		for _, m := range docAnchorRe.FindAllStringSubmatch(body, -1) {
			guid := strings.ToLower(m[1])
			if guid == "" || seen[guid] {
				continue
			}
			title := cleanTitle(m[2])
			if title == "" {
				continue
			}
			seen[guid] = true
			out = append(out, ingest.DiscoveredDoc{
				SourceID:   SourceID,
				ExternalID: guid,
				Title:      title,
				Abstract:   title,
				DocType:    "Guideline",
				DetailURL:  s.baseURL + sec,
				Files:      []ingest.FileRef{fileFor(s.baseURL, guid, title)},
			})
		}
		if err := sleep(ctx, pacePage); err != nil {
			return out, err
		}
	}
	s.log.Info("sc discover", "docs", len(out), "sections", len(inScopeSections))
	return out, nil
}

func fileFor(baseURL, guid, title string) ingest.FileRef {
	name := title
	if name == "" {
		name = guid
	}
	return ingest.FileRef{
		URL:      downloadURL(baseURL, guid),
		Name:     name + ".pdf",
		Ext:      "pdf",
		Kind:     "main",
		MIMEType: "application/pdf",
	}
}

func cleanTitle(s string) string {
	s = tagRe.ReplaceAllString(s, " ")
	s = stdhtml.UnescapeString(s)
	s = strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
	return strings.TrimSpace(pdfSuffixRe.ReplaceAllString(s, ""))
}
