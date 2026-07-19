package bi

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

// Card-parsing regular expressions. The listing HTML contains div.col.peraturan-item
// blocks with structured content: status badge, title link with PeraturanID,
// hidden metadata fields (JenisPeraturanID, TaksonomiID, year), date, and summary.
var (
	// cardRe splits the HTML into per-card blocks. Each match is one regulation card.
	cardRe = regexp.MustCompile(`(?s)<div\s+class="col peraturan-item">(.*?)</div>\s*</div>\s*</div>\s*</div>\s*</div>`)

	// badgeRe extracts the status badge text (Berlaku / Tidak Berlaku).
	badgeRe = regexp.MustCompile(`(?s)<span\s+class="badge[^"]*large-badge"[^>]*>(.*?)</span>`)

	// titleLinkRe extracts the PeraturanID and display title from the card-title link.
	// href="/Web/DaftarPeraturan/Detail/{id}"
	titleLinkRe = regexp.MustCompile(`(?s)<h5\s+class="card-title[^"]*">\s*<a\s+href="/Web/DaftarPeraturan/Detail/(\d+)"[^>]*>\s*(.*?)\s*</a>`)

	// jenisRe extracts JenisPeraturanID from the hidden <i class="none"> element.
	jenisRe = regexp.MustCompile(`<i\s+class="none"[^>]*>\s*(\d+)\s*</i>`)

	// dateRe extracts the dd/MM/yyyy date from the card.
	dateRe = regexp.MustCompile(`<span\s+class="h6\s+mb-5">\s*(\d{2}/\d{2}/\d{4})\s*</span>`)

	// summaryRe extracts the summary/ringkasan text.
	summaryRe = regexp.MustCompile(`(?s)<p\s+class="mt-2\s+ringkasan">\s*(.*?)\s*</p>`)

	// downloadIDRe extracts the PeraturanID from the DownloadPdf onclick.
	downloadIDRe = regexp.MustCompile(`DownloadPdf\('(\d+)'`)

	tagRe   = regexp.MustCompile(`<[^>]+>`)
	spaceRe = regexp.MustCompile(`\s+`)
)

// Discover fetches the BI listing for PBI (jenis 1) and PADG (jenis 2) and
// returns all discovered documents. Discovery parses the full HTML listing page
// and filters by the hidden JenisPeraturanID field.
//
// The listing is fetched once as an unfiltered GET (all ~1,500 cards in one HTML
// response), then parsed and filtered client-side by JenisPeraturanID. This is
// more robust than two filtered POSTs because the HTML card structure is
// identical and the hidden <i class="none"> field is a reliable jenis marker;
// doing two POSTs would risk missing cards if the server-side filter has bugs.
//
// Incremental discovery: the card date (TanggalPenetapan displayed as dd/MM/yyyy)
// is used as the watermark. Cards with a date on or before `since` are skipped.
// Caveat: the card date is the penetapan (enactment) date, not an update
// timestamp; if BI retroactively edits a card without changing the date, the
// incremental crawl will miss it. A periodic full crawl (since=zero) catches
// those cases.
func (s *Source) Discover(ctx context.Context, since time.Time, _ string) ([]ingest.DiscoveredDoc, error) {
	body, err := s.fetchListing(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch listing: %w", err)
	}

	cards := cardRe.FindAllStringSubmatch(body, -1)
	if len(cards) == 0 {
		s.log.Warn("bi discover: no cards found in listing HTML")
		return nil, nil
	}

	var out []ingest.DiscoveredDoc
	for _, m := range cards {
		cardHTML := m[1]

		doc, ok := parseCard(cardHTML)
		if !ok {
			continue
		}

		// Filter: only PBI (1) and PADG (2).
		if doc.jenisID != jenisPeraturanPBI && doc.jenisID != jenisPeraturanPADG {
			continue
		}

		// Incremental watermark: skip cards on or before `since`.
		if !since.IsZero() && !doc.date.IsZero() && !doc.date.After(since) {
			continue
		}

		out = append(out, doc.toDiscoveredDoc())
	}

	s.log.Info("bi discover", "total_cards", len(cards), "in_scope", len(out))
	return out, nil
}

// fetchListing retrieves the full listing HTML via GET.
func (s *Source) fetchListing(ctx context.Context) (string, error) {
	return s.client.Get(ctx, baseURL+listingPath)
}

// parsedCard holds the extracted fields from one regulation card in the listing.
type parsedCard struct {
	id      string // PeraturanID (from title link or DownloadPdf)
	title   string // display title from the card heading
	summary string // ringkasan text
	status  string // "Berlaku" or "Tidak Berlaku" from the badge
	jenisID int    // JenisPeraturanID from hidden <i class="none">
	date    time.Time
}

// parseCard extracts structured fields from a single card's inner HTML.
func parseCard(html string) (parsedCard, bool) {
	var c parsedCard

	// Extract PeraturanID and title from the title link.
	if m := titleLinkRe.FindStringSubmatch(html); m != nil {
		c.id = m[1]
		c.title = cleanText(m[2])
	}

	// Fallback: extract ID from DownloadPdf onclick.
	if c.id == "" {
		if m := downloadIDRe.FindStringSubmatch(html); m != nil {
			c.id = m[1]
		}
	}
	if c.id == "" {
		return c, false
	}

	// Status badge.
	if m := badgeRe.FindStringSubmatch(html); m != nil {
		c.status = cleanText(m[1])
	}

	// JenisPeraturanID from hidden field.
	if m := jenisRe.FindStringSubmatch(html); m != nil {
		c.jenisID, _ = strconv.Atoi(m[1])
	}

	// Date (dd/MM/yyyy).
	if m := dateRe.FindStringSubmatch(html); m != nil {
		c.date, _ = time.Parse("02/01/2006", m[1])
	}

	// Summary/ringkasan.
	if m := summaryRe.FindStringSubmatch(html); m != nil {
		c.summary = cleanText(m[1])
	}

	return c, true
}

// toDiscoveredDoc converts a parsed card to a DiscoveredDoc. The discovery phase
// populates only what is cheaply available from the listing; FetchDetail enriches
// the rest.
func (c *parsedCard) toDiscoveredDoc() ingest.DiscoveredDoc {
	docType := docTypeFromJenis(c.jenisID)
	return ingest.DiscoveredDoc{
		SourceID:   SourceID,
		ExternalID: c.id,
		Number:     c.title, // card title IS the display number (e.g. "PBI Nomor 5 Tahun 2026")
		Title:      c.summary,
		DocType:    docType,
		Status:     c.status,
		IssuedAt:   c.date,
		DetailURL:  detailURL(c.id),
		// PublishedAt uses the card date as the discovery watermark.
		PublishedAt: c.date,
	}
}

// docTypeFromJenis returns the DocType for a JenisPeraturanID. The verbose
// form matches BPK's Bentuk metadata field so doc_keys converge when both
// sources carry the same regulation (e.g. BPK jenis 78 carries PBI too).
func docTypeFromJenis(jenis int) ingest.DocType {
	switch jenis {
	case jenisPeraturanPBI:
		return pbiDocType
	case jenisPeraturanPADG:
		return padgDocType
	default:
		return ingest.DocType(fmt.Sprintf("BI_%d", jenis))
	}
}

// Short DocType codes — must match BPK's jenisCode (discover.go) for doc_key
// convergence. BPK uses lowercase short codes ("pbi", "padg"); BI must too.
const (
	pbiDocType  ingest.DocType = "pbi"
	padgDocType ingest.DocType = "padg"
)

// cleanText strips HTML tags and collapses whitespace.
func cleanText(s string) string {
	s = tagRe.ReplaceAllString(s, " ")
	s = strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
	return s
}
