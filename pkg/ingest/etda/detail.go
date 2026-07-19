package etda

import (
	"context"

	"danny.vn/banhmi/pkg/ingest"
)

// FetchDetail returns the document as discovered. ETDA has no separate detail
// page per document — the PDF is the content, fully identified at discovery.
func (s *Source) FetchDetail(_ context.Context, ref ingest.DetailRef) (*ingest.DiscoveredDoc, error) {
	return &ingest.DiscoveredDoc{
		SourceID:   SourceID,
		ExternalID: ref.ExternalID,
		DocType:    "Regulation",
		DetailURL:  ref.DetailURL,
		Files: []ingest.FileRef{{
			URL:      s.baseURL + "/getattachment/" + ref.ExternalID + "/file.pdf.aspx",
			Name:     ref.ExternalID + ".pdf",
			Ext:      "pdf",
			Kind:     "main",
			MIMEType: "application/pdf",
		}},
	}, nil
}
