package nbc

import (
	"context"

	"danny.vn/banhmi/pkg/ingest"
)

// FetchDetail returns a minimal doc. NBC's listing pages already contain
// the PDF links; no separate detail page enrichment is needed.
func (s *Source) FetchDetail(ctx context.Context, ref ingest.DetailRef) (*ingest.DiscoveredDoc, error) {
	doc := &ingest.DiscoveredDoc{
		SourceID:   SourceID,
		ExternalID: ref.ExternalID,
		DetailURL:  ref.DetailURL,
	}
	if ref.DetailURL != "" {
		doc.Files = []ingest.FileRef{{
			URL:      ref.DetailURL,
			Name:     ref.ExternalID + ".pdf",
			Ext:      "pdf",
			Kind:     "main",
			MIMEType: "application/pdf",
		}}
	}
	return doc, nil
}
