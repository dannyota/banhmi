package sec

import (
	"context"

	"danny.vn/banhmi/pkg/ingest"
)

// FetchDetail returns the document metadata from discovery. SEC has no separate
// detail page — the NRS search result table is the only metadata surface — so
// this reconstructs the doc from its ExternalID (NRS ID) and the stored
// discovery row. The file cascade is rebuilt from the NRS ID.
func (s *Source) FetchDetail(_ context.Context, ref ingest.DetailRef) (*ingest.DiscoveredDoc, error) {
	nrsID := ref.ExternalID

	return &ingest.DiscoveredDoc{
		SourceID:   SourceID,
		ExternalID: nrsID,
		DocType:    "SEC Notification",
		DetailURL:  ref.DetailURL,
		Files: []ingest.FileRef{
			// Prefer the signed PDF as baseline; if a DOCX was discovered, the
			// pipeline already has it from the discovery row's Files.
			signedPDFRef(nrsID),
		},
	}, nil
}
