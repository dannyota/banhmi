package pdpc

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"danny.vn/banhmi/pkg/ingest"
)

// assetUUIDRe matches /assets/{uuid} links in the detail page HTML.
var assetUUIDRe = regexp.MustCompile(`/assets/([a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12})`)

// knownIconUUIDs are asset UUIDs for site icons/images that appear in the HTML
// but are not document PDFs.
var knownIconUUIDs = map[string]bool{
	"5bfe2a77-00a0-4836-94bb-5cf432c8f92d": true,
	"ee462a25-5953-4484-8cf0-88bde43d21bc": true,
}

// FetchDetail fetches the PDPC detail page and extracts PDF asset links from
// the Optical CDN. The detail page HTML contains /assets/{uuid} references;
// each non-icon UUID maps to a downloadable PDF.
func (s *Source) FetchDetail(ctx context.Context, ref ingest.DetailRef) (*ingest.DiscoveredDoc, error) {
	if ref.DetailURL == "" {
		return nil, fmt.Errorf("pdpc detail: empty detail url")
	}
	body, err := s.get(ctx, ref.DetailURL)
	if err != nil {
		return nil, fmt.Errorf("pdpc detail %s: %w", ref.ExternalID, err)
	}
	files := extractPDFs(body)
	return &ingest.DiscoveredDoc{
		SourceID:   SourceID,
		ExternalID: ref.ExternalID,
		DetailURL:  ref.DetailURL,
		Files:      files,
	}, nil
}

// extractPDFs finds asset UUIDs in the HTML and returns FileRefs pointing at
// the Optical CDN. Known icon UUIDs are filtered out.
func extractPDFs(html string) []ingest.FileRef {
	seen := map[string]bool{}
	var files []ingest.FileRef
	for _, m := range assetUUIDRe.FindAllStringSubmatch(html, -1) {
		uuid := strings.ToLower(m[1])
		if seen[uuid] || knownIconUUIDs[uuid] {
			continue
		}
		seen[uuid] = true
		files = append(files, ingest.FileRef{
			URL:      opticalCDNBase + "/" + uuid + ".pdf",
			Name:     uuid + ".pdf",
			Ext:      "pdf",
			Kind:     "main",
			MIMEType: "application/pdf",
		})
	}
	return files
}
