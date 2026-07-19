package sc

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

// docAnchorRe matches a document download link: <a href="…download.ashx?id=GUID">Title (pdf)</a>.
// GUIDs are hex-with-dashes, mixed case.
var (
	docAnchorRe   = regexp.MustCompile(`(?is)<a[^>]+href="[^"]*download\.ashx\?id=([a-fA-F0-9-]+)"[^>]*>(.*?)</a>`)
	tagRe         = regexp.MustCompile(`(?s)<[^>]+>`)
	spaceRe       = regexp.MustCompile(`\s+`)
	pdfSuffixRe   = regexp.MustCompile(`(?i)\s*\((pdf|docx?|xlsx?)\)\s*$`)
	nonAlphanumRe = regexp.MustCompile(`[^A-Z0-9]+`)
)

// Discover crawls the in-scope SC sections and returns each linked document.
// Only technology/digital sections are crawled (structural pre-filter); the
// pipeline's scope.Match then applies the MY vocabulary as a second filter.
//
// Partial failure contract: if any section fetch fails, Discover still
// collects docs from successful sections and returns them alongside a non-nil
// error. The pipeline must treat a non-nil error as "this slice is incomplete"
// and NOT advance the discover cursor — upserts are idempotent, so the retry
// is cheap.
func (s *Source) Discover(ctx context.Context, _ time.Time, _ string) ([]ingest.DiscoveredDoc, error) {
	// Deduplicate by title slug, not by GUID. SC section pages list the same
	// PDF under many download GUIDs (one per part/chapter) that all resolve to
	// the identical file. The GUID is unstable across crawls and produces
	// duplicate bronze rows that flow to duplicate silver documents. The title
	// slug is the stable identity; the first GUID seen for a title wins as the
	// canonical ExternalID.
	seenGUID := map[string]bool{}
	seenSlug := map[string]bool{}
	var (
		out     []ingest.DiscoveredDoc
		errs    []error
		nFailed int
	)
	for _, sec := range inScopeSections {
		body, err := s.get(ctx, s.baseURL+sec)
		if err != nil {
			s.log.Warn("sc section fetch failed", "section", sec, "err", err)
			errs = append(errs, fmt.Errorf("section %s: %w", sec, err))
			nFailed++
			continue
		}
		for _, m := range docAnchorRe.FindAllStringSubmatch(body, -1) {
			guid := strings.ToLower(m[1])
			if guid == "" || seenGUID[guid] {
				continue
			}
			title := cleanTitle(m[2])
			if title == "" {
				continue
			}
			seenGUID[guid] = true
			slug := titleSlug(title)
			if seenSlug[slug] {
				s.log.Debug("sc discover: skipping duplicate title slug",
					"title", title, "slug", slug, "guid", guid)
				continue
			}
			seenSlug[slug] = true
			number := scDocNumber(slug)
			out = append(out, ingest.DiscoveredDoc{
				SourceID:   SourceID,
				ExternalID: guid,
				Number:     number,
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
	if nFailed > 0 {
		return out, fmt.Errorf("sc discover: %d of %d sections failed: %w", nFailed, len(inScopeSections), errors.Join(errs...))
	}
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

// scDocNumber returns the synthetic document number for an SC document.
// SC documents have no official registration/reference number — the source site
// identifies them only by title and download GUID, where GUIDs are per-link (the
// same PDF can appear under many GUIDs on one section page). The Number must be
// stable across crawls and identical for true duplicates so the pipeline's
// doc_key dedup ("GUIDELINE|SC-GL/RECOGNIZED-MARKETS") collapses them into one
// silver document. Format: "SC-GL/<TITLE-SLUG>" — the SC-GL prefix mirrors
// BNM/'s prefix convention for the same jurisdiction.
func scDocNumber(slug string) string {
	return "SC-GL/" + slug
}

// titleSlug normalizes a title into an uppercase hyphen-separated slug for use
// as a stable identifier component. It strips all non-alphanumeric characters,
// collapses runs into a single hyphen, and trims leading/trailing hyphens.
// Deterministic and locale-insensitive (SC titles are English).
func titleSlug(title string) string {
	s := strings.ToUpper(title)
	s = nonAlphanumRe.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}
