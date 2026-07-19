package sc

import (
	"context"

	"danny.vn/banhmi/pkg/ingest"
)

// FetchDetail returns the document's downloadable file. SC documents have no
// separate detail page — the section listing is the only metadata surface — so the
// file is reconstructed from the GUID (ExternalID). Title and section come from the
// stored discovery row; this enrichment supplies the file the pipeline downloads.
func (s *Source) FetchDetail(ctx context.Context, ref ingest.DetailRef) (*ingest.DiscoveredDoc, error) {
	return &ingest.DiscoveredDoc{
		SourceID:   SourceID,
		ExternalID: ref.ExternalID,
		DocType:    "Guideline",
		DetailURL:  ref.DetailURL,
		Files:      []ingest.FileRef{fileFor(s.baseURL, ref.ExternalID, "")},
	}, nil
}
