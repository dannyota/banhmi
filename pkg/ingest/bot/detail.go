package bot

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"danny.vn/banhmi/pkg/ingest"
)

const summaryPath = "/Thai/PFIPCS_summary.aspx"

// hasDetailContent checks if the response contains actual detail content
// (not the empty portal shell returned without a valid session).
func hasDetailContent(body string) bool {
	return strings.Contains(body, "LblDocName") || strings.Contains(body, "LblDocTitle")
}

// Detail page field patterns.
var (
	// lblDocNameRe extracts the document number from the detail page.
	// <span id="...LblDocName">หนังสือเวียน ธปท. ฝนส.(03) ว. 3/2569</span>
	lblDocNameRe = regexp.MustCompile(`(?is)<span[^>]+id="[^"]*(?:LblDocName|lblDocID)"[^>]*>(.*?)</span>`)

	// lblTitleRe extracts the full title.
	lblTitleRe = regexp.MustCompile(`(?is)<span[^>]+id="[^"]*LblDocTitle"[^>]*>(.*?)</span>`)

	// lblIssueDateRe extracts the issued date.
	lblIssueDateRe = regexp.MustCompile(`(?is)<span[^>]+id="[^"]*LblLetter[Dd]ate"[^>]*>(.*?)</span>`)

	// lblEffectiveDateRe extracts the effective date.
	lblEffectiveDateRe = regexp.MustCompile(`(?is)<span[^>]+id="[^"]*LblEffective[Dd]ate"[^>]*>(.*?)</span>`)

	// lblExpiryDateRe extracts the expiry date.
	lblExpiryDateRe = regexp.MustCompile(`(?is)<span[^>]+id="[^"]*LblExpiry[Dd]ate"[^>]*>(.*?)</span>`)

	// lblPurposeRe extracts the purpose field.
	lblPurposeRe = regexp.MustCompile(`(?is)<span[^>]+id="[^"]*LblPurpose"[^>]*>(.*?)</span>`)

	// lblSubstanceRe extracts the substance/abstract field.
	lblSubstanceRe = regexp.MustCompile(`(?is)<span[^>]+id="[^"]*LblSubstance"[^>]*>(.*?)</span>`)
)

// FetchDetail fetches the FIPCS summary page for a document and returns an
// enriched DiscoveredDoc with the document number, dates, purpose, and substance.
// The summary page requires the same ASP.NET session, so a fresh session is
// established if needed.
func (s *Source) FetchDetail(ctx context.Context, ref ingest.DetailRef) (*ingest.DiscoveredDoc, error) {
	detailURL := ref.DetailURL
	if detailURL == "" {
		detailURL = s.baseURL + summaryPath + "?packId=" + ref.ExternalID
	}

	// Skip the summary page — metadata already came from Discover. The summary
	// page requires a session (serializes concurrency) and adds ~3s per doc.
	// Just return the PDF FileRef so the pipeline downloads the actual PDF.
	doc := &ingest.DiscoveredDoc{
		SourceID:   SourceID,
		ExternalID: ref.ExternalID,
		DetailURL:  detailURL,
	}

	// Prefer the hrefs Discover scraped from listing column 5 — they are the
	// site's own links, so they cannot drift from its layout. The pipeline
	// replays them here via DetailRef.Files.
	if len(ref.Files) > 0 {
		doc.Files = ref.Files
		return doc, nil
	}

	// Fallback only when discovery captured no link (older ledger rows, or a
	// listing row without a PDF cell): construct the conventional path. This is a
	// guess — the FPG group and the packId's leading B.E. year hold for most
	// documents but not all, which stranded 243 of them before DetailRef carried
	// the real links. A short packId cannot be sliced, so bail instead of panicking.
	if len(ref.ExternalID) < 4 {
		return doc, nil
	}
	pdfURL := fmt.Sprintf("%s/FPG/%s/ThaiPDF/%s.pdf",
		s.pdfBaseURL, ref.ExternalID[:4], ref.ExternalID)
	doc.Files = []ingest.FileRef{{
		URL:      pdfURL,
		Name:     ref.ExternalID + ".pdf",
		Ext:      "pdf",
		Kind:     "main",
		MIMEType: "application/pdf",
	}}
	return doc, nil
}

// parseDetail parses a summary page HTML into a DiscoveredDoc.
func parseDetail(body, externalID, detailURL string) *ingest.DiscoveredDoc {
	d := &ingest.DiscoveredDoc{
		SourceID:   SourceID,
		ExternalID: externalID,
		DetailURL:  detailURL,
	}

	// Document number.
	if m := lblDocNameRe.FindStringSubmatch(body); m != nil {
		d.Number = cleanText(m[1])
	}

	// Title.
	if m := lblTitleRe.FindStringSubmatch(body); m != nil {
		d.Title = cleanText(m[1])
	}

	// Issued date.
	if m := lblIssueDateRe.FindStringSubmatch(body); m != nil {
		d.IssuedAt = parseThaiDate(cleanText(m[1]))
	}

	// Effective date.
	if m := lblEffectiveDateRe.FindStringSubmatch(body); m != nil {
		d.EffectiveAt = parseThaiDate(cleanText(m[1]))
	}

	// Expiry date.
	if m := lblExpiryDateRe.FindStringSubmatch(body); m != nil {
		d.ExpireAt = parseThaiDate(cleanText(m[1]))
	}

	// Purpose → Abstract.
	var purpose, substance string
	if m := lblPurposeRe.FindStringSubmatch(body); m != nil {
		purpose = cleanText(m[1])
	}
	if m := lblSubstanceRe.FindStringSubmatch(body); m != nil {
		substance = cleanText(m[1])
	}
	switch {
	case purpose != "" && substance != "":
		d.Abstract = purpose + " — " + substance
	case purpose != "":
		d.Abstract = purpose
	case substance != "":
		d.Abstract = substance
	}

	return d
}
