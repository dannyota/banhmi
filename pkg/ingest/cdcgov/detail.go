package cdcgov

import (
	"context"

	"danny.vn/banhmi/pkg/ingest"
)

// FetchDetail returns a doc with the PDF file reference reconstructed from the
// DetailURL (which IS the PDF URL for CDC). The pipeline planner creates file
// artifacts from Files, so FetchDetail must return them.
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
