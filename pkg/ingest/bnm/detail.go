package bnm

import (
	"context"
	"path"
	"strings"

	"danny.vn/banhmi/pkg/ingest"
)

// FetchDetail returns the document's downloadable PDF, reconstructed from the
// stored path (ExternalID). The listing row already carried the title, date, and
// type; BNM has no per-document detail API, and its /-/ landing pages add only
// finer issuance/effective dates (a later refinement). This enrichment supplies
// the file the pipeline downloads.
func (s *Source) FetchDetail(ctx context.Context, ref ingest.DetailRef) (*ingest.DiscoveredDoc, error) {
	url := ref.ExternalID
	if strings.HasPrefix(url, "/") {
		url = strings.TrimRight(s.baseURL, "/") + ref.ExternalID
	}
	return &ingest.DiscoveredDoc{
		SourceID:   SourceID,
		ExternalID: ref.ExternalID,
		DocType:    "Policy Document",
		DetailURL:  ref.DetailURL,
		Files: []ingest.FileRef{{
			URL: url, Name: path.Base(url), Ext: "pdf", Kind: "main", MIMEType: "application/pdf",
		}},
	}, nil
}
