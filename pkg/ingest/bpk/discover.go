package bpk

import (
	"context"
	"fmt"
	stdhtml "html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

// jenisCode maps a BPK jenis query parameter to its short doc-type label. The
// codes are the values for /Search?jenis=N. PBI (78) is excluded — it comes
// from the separate bi source.
var jenisCode = map[int]ingest.DocType{
	8:   "uu",
	10:  "pp",
	80:  "pojk",
	212: "seojk",
}

// jenisOrder is the enumeration order for discovery (deterministic).
var jenisOrder = []int{8, 10, 80, 212}

// jenisGeneral are the general national-law types (UU, PP) that carry
// all-sector legislation. These are the only types searched when a keyword is
// specified — regulator-specific types (POJK/SEOJK) cover the full sector
// already and need no keyword filter.
var jenisGeneral = []int{8, 10}

const (
	maxPages = 600                    // safety cap (PP has ~4,991 docs / 10 = 500 pages)
	pacePage = 400 * time.Millisecond // polite delay between page fetches
)

// Regex patterns for listing page parsing.
var (
	// totalCountRe extracts the total result count from
	// <p class="text-danger">Menemukan N peraturan ...</p>
	totalCountRe = regexp.MustCompile(`(?i)Menemukan\s+([\d.,]+)\s+peraturan`)

	// cardSplitRe splits the listing body at each card boundary.
	cardSplitRe = regexp.MustCompile(`(?s)<div\s+class="card">`)

	// typeNumberRe extracts the type+number line from
	// <div class="col-lg-8 fw-semibold fs-5 text-gray-600">
	//   Peraturan Otoritas Jasa Keuangan Nomor 5 Tahun 2026
	// </div>
	typeNumberRe = regexp.MustCompile(`(?is)<div[^>]*class="[^"]*col-lg-8[^"]*fw-semibold[^"]*"[^>]*>(.*?)</div>`)

	// titleLinkRe extracts the title and detail href from
	// <div class="col-lg-10 fs-2 fw-bold ..."><a href="/Details/350261/slug">Title</a></div>
	titleLinkRe = regexp.MustCompile(`(?is)<div[^>]*class="[^"]*col-lg-10[^"]*fs-2[^"]*fw-bold[^"]*"[^>]*>.*?<a\s+href="(/Details/(\d+)/[^"]*)"[^>]*>\s*(.*?)\s*</a>`)

	// badgeRe extracts subject badges.
	badgeRe = regexp.MustCompile(`(?is)<span[^>]*class="[^"]*badge[^"]*badge-light-primary[^"]*"[^>]*>(.*?)</span>`)

	// downloadRe extracts download links from listing cards.
	// <a ... class="download-file ..." data-id="413974" href="/Download/413974/POJK%205%20Tahun%202026.pdf">
	downloadRe = regexp.MustCompile(`(?is)<a[^>]*class="[^"]*download-file[^"]*"[^>]*data-id="(\d+)"[^>]*href="(/Download/\d+/[^"]+)"[^>]*>`)

	// statusBlockRe finds the inline "Status Peraturan" block in a card.
	statusBlockRe = regexp.MustCompile(`(?is)<div[^>]*>Status Peraturan</div>(.*?)(?:<div[^>]*>Download file:|</div>\s*</div>\s*</div>\s*</div>)`)

	// relationTypeRe extracts the relation type from the colored badge.
	relationTypeRe = regexp.MustCompile(`(?is)<div[^>]*class="[^"]*bg-light-primary[^"]*text-primary[^"]*"[^>]*>\s*(.*?)\s*</div>`)

	// relationTargetRe extracts each target in an inline relation list.
	// <a class="text-danger" href="/Details/128426/slug">Peraturan OJK No. ...</a>
	// <span> tentang Title</span>
	relationTargetRe = regexp.MustCompile(`(?is)<a[^>]*class="[^"]*text-danger[^"]*"[^>]*href="/Details/(\d+)/[^"]*"[^>]*>(.*?)</a>\s*(?:<span>\s*tentang\s*(.*?)\s*</span>)?`)

	// lastPageRe extracts the last page number from the pagination.
	// The "Last" link: <a class="page-link" href="/Search?...&p=51">Last</a>
	lastPageRe = regexp.MustCompile(`(?is)<li[^>]*class="[^"]*page-item[^"]*"[^>]*>\s*<a[^>]*class="[^"]*page-link[^"]*"[^>]*href="[^"]*[?&](?:amp;)?p=(\d+)[^"]*"[^>]*>\s*Last\s*</a>`)

	// numberYearRe extracts "Nomor X Tahun YYYY" from the type+number line.
	numberYearRe = regexp.MustCompile(`(?i)Nomor\s+(\S+)\s+Tahun\s+(\d{4})`)

	// tagRe strips HTML tags.
	tagRe = regexp.MustCompile(`(?s)<[^>]+>`)

	// spaceRe collapses whitespace.
	spaceRe = regexp.MustCompile(`\s+`)
)

// Discover iterates the configured jenis listings (UU, PP, POJK, SEOJK)
// newest-first, emitting a DiscoveredDoc per card.
//
// When keyword is non-empty, only the GENERAL national-law types (UU, PP) are
// searched with that keyword term — regulator-specific types (POJK/SEOJK)
// already cover the full financial sector and never need keyword filtering.
// An empty keyword runs the full sweep across all four jenis.
//
// Incremental crawl: BPK's Search endpoint filters server-side by tahun
// (regulation year, multi-value — verified live 2026-07-04); there is no page
// size override, sitemap, or sort param. Each card's PublishedAt is set to
// Jan 1 of its Tahun year — a year-granularity discovery watermark, not a
// legal date (real dates come from FetchDetail). When since is non-zero,
// Discover crawls only the years from since.Year()-1 through the current year
// (the extra year is margin for docs published around the boundary); the
// first run (zero since) is a full scan. BPK occasionally backfills older
// years — clear the discover cursor to force a full rescan.
func (s *Source) Discover(ctx context.Context, since time.Time, keyword string) ([]ingest.DiscoveredDoc, error) {
	years := yearWindow(since, time.Now().UTC())

	// Keyword searches target only general national-law types (UU, PP);
	// sweep mode (empty keyword) iterates all four jenis.
	order := jenisOrder
	if keyword != "" {
		order = jenisGeneral
	}

	var out []ingest.DiscoveredDoc
	for _, jenis := range order {
		docs, err := s.discoverJenis(ctx, jenis, years, keyword)
		if err != nil {
			s.log.Warn("bpk jenis discover failed", "jenis", jenis, "keyword", keyword, "err", err)
			continue
		}
		out = append(out, docs...)
	}
	s.log.Info("bpk discover", "docs", len(out), "years", years, "keyword", keyword)
	return out, nil
}

// yearWindow returns the tahun filter values for an incremental crawl: the
// years from since.Year()-1 through now.Year(). A zero since means full scan
// (nil — no tahun filter).
func yearWindow(since, now time.Time) []int {
	if since.IsZero() {
		return nil
	}
	first := since.Year() - 1
	if first > now.Year() {
		first = now.Year()
	}
	var years []int
	for y := first; y <= now.Year(); y++ {
		years = append(years, y)
	}
	return years
}

// discoverJenis paginates one jenis listing (optionally keyword-filtered) and
// returns all parsed cards.
func (s *Source) discoverJenis(ctx context.Context, jenis int, years []int, keyword string) ([]ingest.DiscoveredDoc, error) {
	docType := jenisCode[jenis]
	var out []ingest.DiscoveredDoc
	lastPage := 1 // updated from pagination on first page

	for page := 1; page <= lastPage && page <= maxPages; page++ {
		u := listingURL(jenis, page, years, keyword)
		body, err := s.client.Get(ctx, u)
		if err != nil {
			return out, fmt.Errorf("listing jenis=%d page=%d: %w", jenis, page, err)
		}

		if page == 1 {
			if lp := parseLastPage(body); lp > 0 {
				lastPage = lp
			}
		}

		cards := parseListing(body, docType)
		if len(cards) == 0 {
			break
		}
		out = append(out, cards...)

		if err := sleep(ctx, pacePage); err != nil {
			return out, err
		}
	}
	s.log.Info("bpk jenis done", "jenis", jenis, "type", docType, "docs", len(out))
	return out, nil
}

// listingURL builds a search URL for the given jenis, page, optional tahun
// year filter (multi-value, server-side), and optional keyword search term.
func listingURL(jenis, page int, years []int, keyword string) string {
	u := fmt.Sprintf("%s/Search?jenis=%d&p=%d", baseURL, jenis, page)
	for _, y := range years {
		u += fmt.Sprintf("&tahun=%d", y)
	}
	if keyword != "" {
		u += "&keyword=" + url.QueryEscape(keyword)
	}
	return u
}

// parseLastPage extracts the last page number from the pagination "Last" link.
func parseLastPage(body string) int {
	m := lastPageRe.FindStringSubmatch(body)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return n
}

// parseTotalCount extracts the total result count from the listing page.
func parseTotalCount(body string) int {
	m := totalCountRe.FindStringSubmatch(body)
	if m == nil {
		return 0
	}
	s := strings.ReplaceAll(m[1], ".", "")
	s = strings.ReplaceAll(s, ",", "")
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// parseListing splits a listing page into cards and parses each one.
func parseListing(body string, docType ingest.DocType) []ingest.DiscoveredDoc {
	parts := cardSplitRe.Split(body, -1)
	var out []ingest.DiscoveredDoc
	for _, card := range parts {
		d, ok := parseCard(card, docType)
		if ok {
			out = append(out, d)
		}
	}
	return out
}

// parseCard parses a single listing card into a DiscoveredDoc.
func parseCard(card string, docType ingest.DocType) (ingest.DiscoveredDoc, bool) {
	// Extract title link (required — identifies the doc).
	tlm := titleLinkRe.FindStringSubmatch(card)
	if tlm == nil {
		return ingest.DiscoveredDoc{}, false
	}
	detailPath := tlm[1]
	externalID := tlm[2]
	title := cleanText(tlm[3])

	// Extract type+number line.
	var number string
	if tnm := typeNumberRe.FindStringSubmatch(card); tnm != nil {
		number = cleanText(tnm[1])
	}

	// Extract subject badges.
	var subjects []string
	for _, bm := range badgeRe.FindAllStringSubmatch(card, -1) {
		if t := cleanText(bm[1]); t != "" {
			subjects = append(subjects, t)
		}
	}

	// Extract download links.
	var files []ingest.FileRef
	for _, dm := range downloadRe.FindAllStringSubmatch(card, -1) {
		fileID := dm[1]
		href := dm[2]
		name := fileNameFromHref(href)
		files = append(files, ingest.FileRef{
			URL:      baseURL + href,
			Name:     name,
			Ext:      fileExt(name),
			Kind:     "main",
			MIMEType: "application/pdf",
		})
		_ = fileID // available for audit but not needed
	}

	// Extract inline status relations.
	var relations []ingest.Relation
	if sm := statusBlockRe.FindStringSubmatch(card); sm != nil {
		relations = parseInlineRelations(sm[1])
	}

	// PublishedAt carries the card's Tahun year (Jan 1, UTC) — a
	// year-granularity discovery watermark that drives incremental tahun
	// crawls, not a legal date (FetchDetail supplies the real dates).
	var publishedAt time.Time
	if m := numberYearRe.FindStringSubmatch(number); m != nil {
		if y, err := strconv.Atoi(m[2]); err == nil {
			publishedAt = time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC)
		}
	}

	return ingest.DiscoveredDoc{
		SourceID:    SourceID,
		ExternalID:  externalID,
		Number:      parseNumber(number, docType),
		Title:       title,
		Abstract:    strings.Join(subjects, " - "),
		DocType:     docType,
		DetailURL:   baseURL + detailPath,
		Files:       files,
		Relations:   relations,
		PublishedAt: publishedAt,
	}, true
}

// parseInlineRelations parses the "Status Peraturan" block from a listing card.
func parseInlineRelations(block string) []ingest.Relation {
	// The block may contain multiple relation groups (each with a type badge
	// and a list of targets). Split by the type badge.
	typeParts := relationTypeRe.FindAllStringSubmatchIndex(block, -1)
	if len(typeParts) == 0 {
		return nil
	}

	var out []ingest.Relation
	for i, loc := range typeParts {
		relType := cleanText(block[loc[2]:loc[3]])

		// Find the extent of this relation group (until next type badge or end).
		end := len(block)
		if i+1 < len(typeParts) {
			end = typeParts[i+1][0]
		}
		segment := block[loc[1]:end]

		// Extract targets within this segment.
		for _, tm := range relationTargetRe.FindAllStringSubmatch(segment, -1) {
			targetID := tm[1]
			targetNumber := cleanText(tm[2])
			var targetTitle string
			if len(tm) > 3 {
				targetTitle = cleanText(tm[3])
			}
			out = append(out, ingest.Relation{
				Type:         relType,
				TargetID:     targetID,
				TargetNumber: targetNumber,
				TargetTitle:  targetTitle,
				TargetURL:    baseURL + "/Details/" + targetID,
			})
		}
	}
	return out
}

// parseNumber extracts a normalized number from the type+number line.
// Input examples:
//
//	"Peraturan Otoritas Jasa Keuangan Nomor 5 Tahun 2026" → "POJK 5/2026"
//	"Undang-undang (UU) Nomor 27 Tahun 2022" → "UU 27/2022"
//	"Peraturan Pemerintah (PP) Nomor 71 Tahun 2019" → "PP 71/2019"
func parseNumber(raw string, docType ingest.DocType) string {
	if raw == "" {
		return ""
	}
	// Try to extract "Nomor X Tahun YYYY" pattern.
	m := numberYearRe.FindStringSubmatch(raw)
	if m == nil {
		return raw
	}
	prefix := strings.ToUpper(string(docType))
	return prefix + " " + m[1] + "/" + m[2]
}

// fileNameFromHref extracts the file name from a download href, URL-decoding it.
func fileNameFromHref(href string) string {
	// href is like "/Download/413974/POJK%205%20Tahun%202026.pdf"
	parts := strings.Split(href, "/")
	if len(parts) == 0 {
		return ""
	}
	name, err := url.PathUnescape(parts[len(parts)-1])
	if err != nil {
		return parts[len(parts)-1]
	}
	return name
}

// fileExt extracts the lowercase extension from a filename.
func fileExt(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return strings.ToLower(name[i+1:])
	}
	return ""
}

func cleanText(s string) string {
	s = tagRe.ReplaceAllString(s, " ")
	s = stdhtml.UnescapeString(s)
	return strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
