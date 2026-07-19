package csa

import (
	"context"
	"encoding/xml"
	"fmt"
	"strings"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

// inScopePathPrefixes are the CSA URL path prefixes banhmi crawls. Only pages
// under these paths are returned from the sitemap.
var inScopePathPrefixes = []string{
	"/legislation/codes-of-practice",
	"/legislation/notices",
	"/legislation/supplementary-references",
	"/resources/publications/",
}

// sitemapURLSet is the top-level sitemap XML structure.
type sitemapURLSet struct {
	XMLName xml.Name     `xml:"urlset"`
	URLs    []sitemapLoc `xml:"url"`
}

// sitemapLoc is one <url> entry in the sitemap.
type sitemapLoc struct {
	Loc string `xml:"loc"`
}

// Discover fetches the CSA sitemap and returns pages under the in-scope paths.
// The `since` and `keyword` parameters are ignored — the CSA corpus is small
// enough to fetch the full sitemap each time.
func (s *Source) Discover(ctx context.Context, _ time.Time, _ string) ([]ingest.DiscoveredDoc, error) {
	smURL := s.baseURL + "/sitemap.xml"
	body, err := s.get(ctx, smURL)
	if err != nil {
		return nil, fmt.Errorf("csa discover: fetch sitemap: %w", err)
	}
	urls, err := parseSitemapURLs(body)
	if err != nil {
		return nil, fmt.Errorf("csa discover: parse sitemap: %w", err)
	}
	var docs []ingest.DiscoveredDoc
	for _, u := range urls {
		if !isInScope(u) {
			continue
		}
		slug := urlSlug(u)
		title := slugToTitle(slug)
		docs = append(docs, ingest.DiscoveredDoc{
			SourceID:   SourceID,
			ExternalID: slug,
			Title:      title,
			Abstract:   title,
			DocType:    "Legislation",
			DetailURL:  u,
		})
	}
	s.log.Info("csa discover", "docs", len(docs), "sitemap_urls", len(urls))
	return docs, nil
}

// parseSitemapURLs extracts all <loc> entries from a sitemap XML document.
func parseSitemapURLs(xmlBody string) ([]string, error) {
	var urlset sitemapURLSet
	if err := xml.Unmarshal([]byte(xmlBody), &urlset); err != nil {
		return nil, fmt.Errorf("unmarshal sitemap: %w", err)
	}
	out := make([]string, 0, len(urlset.URLs))
	for _, u := range urlset.URLs {
		loc := strings.TrimSpace(u.Loc)
		if loc != "" {
			out = append(out, loc)
		}
	}
	return out, nil
}

// isInScope returns true if the URL path starts with any in-scope prefix.
func isInScope(rawURL string) bool {
	// Extract path portion — URLs from the sitemap include the scheme+host.
	path := rawURL
	if idx := strings.Index(rawURL, "://"); idx >= 0 {
		rest := rawURL[idx+3:]
		if si := strings.Index(rest, "/"); si >= 0 {
			path = rest[si:]
		} else {
			return false
		}
	}
	for _, prefix := range inScopePathPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// urlSlug extracts the last path segment from a URL as a stable external ID.
func urlSlug(rawURL string) string {
	rawURL = strings.TrimRight(rawURL, "/")
	if idx := strings.LastIndex(rawURL, "/"); idx >= 0 {
		return rawURL[idx+1:]
	}
	return rawURL
}

// slugToTitle converts a URL slug to a readable title by replacing hyphens
// with spaces and title-casing.
func slugToTitle(slug string) string {
	s := strings.ReplaceAll(slug, "-", " ")
	s = strings.TrimSpace(s)
	if s == "" {
		return slug
	}
	// Title-case: capitalize first letter of each word.
	words := strings.Fields(s)
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}
