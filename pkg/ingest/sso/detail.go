package sso

import (
	"context"

	"danny.vn/banhmi/pkg/ingest"
)

// FetchDetail returns the document's metadata and PDF file reference. SSO Act
// detail pages lazy-load content (only Part 1 inline), so the primary content
// source is the PDF. Discovery already provides the PDF FileRef; this enrichment
// reconstructs it from the ExternalID (Act code).
func (s *Source) FetchDetail(ctx context.Context, ref ingest.DetailRef) (*ingest.DiscoveredDoc, error) {
	detailURL := ref.DetailURL
	if detailURL == "" {
		detailURL = s.baseURL + "/Act/" + ref.ExternalID
	}
	pdfURL := s.baseURL + "/Act/" + ref.ExternalID + "?ViewType=Pdf"

	return &ingest.DiscoveredDoc{
		SourceID:   SourceID,
		ExternalID: ref.ExternalID,
		Number:     ref.ExternalID,
		DocType:    "Act",
		DetailURL:  detailURL,
		Files: []ingest.FileRef{{
			URL:      pdfURL,
			Name:     ref.ExternalID + ".pdf",
			Ext:      "pdf",
			Kind:     "main",
			MIMEType: "application/pdf",
		}},
	}, nil
}
