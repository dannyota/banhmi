package bpk

import (
	"context"
	"errors"
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
	11:  "perpres",
	42:  "pmk",
	54:  "bssn",
	80:  "pojk",
	81:  "ppatk",
	83:  "lps",
	106: "kominfo",
	212: "seojk",
	221: "ppatk",
	278: "komdigi",
}

// jenisSweep is the full enumeration for discovery (deterministic order).
// Discovery is sweep-only: every jenis listing is walked and the config scope
// vocabulary is the single authority on what is kept.
//
// Why no keyword discovery: BPK's Search endpoint silently IGNORES an
// unrecognized filter param and returns the whole unfiltered listing (verified
// live 2026-07-13 — the field is `keywords`/`tentang`, never `keyword`). Because
// the pipeline skips scope.Match on keyword slices (it trusts the server to have
// filtered), a keyword slice enqueued every document as in-scope. Even with the
// right param name, BPK OR-matches multi-word terms ("bank indonesia" returns any
// title with "indonesia"), so its filter can never be trusted as a scope decision.
// One sweep is also far cheaper: ~1.4k listing pages, versus ~9k across keyword
// slices whose result sets overlap heavily.
//
// Scope splits by issuer mandate, and the vocabulary encodes it:
//   - Regulator types (POJK, SEOJK, BSSN, LPS, PPATK) come from bodies whose whole
//     mandate is our domain, so they are in scope by issuer — scope_term_id.csv
//     carries their codes as strong terms, matching on the document number alone.
//   - Broad-mandate types (UU, PP, Perpres, PMK, Kominfo, Komdigi) span every
//     sector (agriculture PP, customs PMK, broadcast Kominfo), so they are admitted
//     only when the vocabulary matches their number, title, or subject.
var jenisSweep = []int{
	80, 212, // POJK, SEOJK — OJK (financial services)
	54,      // BSSN — cybersecurity
	83,      // LPS — deposit insurance
	81, 221, // PPATK — AML/CFT (old + new format)
	8, 10, // UU, PP — national law
	11,  // Perpres — presidential regulations (OJK/BI mandates)
	42,  // PMK — Ministry of Finance (fintech tax, e-money)
	106, // Kominfo — PSE, ITE, data-protection implementing rules
	278, // Komdigi — technology/digital (Kominfo's successor)
}

const (
	maxPages            = 600                    // safety cap (PP has ~4,991 docs / 10 = 500 pages)
	pacePage            = 400 * time.Millisecond // polite delay between page fetches
	discoverConcurrency = 4                      // concurrent jenis types per Discover call

	// Deep pagination trips a Cloudflare throttle that outlasts fetch.Client's
	// own transient retry (3 attempts inside ~3s), surfacing as "connection reset
	// by peer" a few hundred pages in. Back off far longer before abandoning the
	// jenis — losing one page otherwise costs every page after it.
	pageAttempts = 4
)

// pageRetryBackoff is multiplied by the attempt number (10s, 20s, 30s). It is a
// var, not a const, so tests can shrink it — the retry path is exercised there.
var pageRetryBackoff = 10 * time.Second

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

// Discover sweeps every jenis listing in jenisSweep newest-first, emitting a
// DiscoveredDoc per card. Scope is decided downstream by the config vocabulary,
// never by BPK's own search filter (see jenisSweep).
//
// Discovery is sweep-only: a non-empty keyword is rejected. BPK ignores an
// unrecognized filter param and answers with the FULL listing, and the pipeline
// skips scope.Match for keyword slices — so a keyword slice would silently
// enqueue every document in the listing as in-scope.
//
// Partial failure contract: if any jenis listing fails, Discover still
// collects docs from the successful listings and returns them alongside a
// non-nil error. The pipeline must treat a non-nil error as "this slice is
// incomplete" and NOT advance the discover cursor — upserts are idempotent,
// so the retry is cheap.
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
	if keyword != "" {
		return nil, fmt.Errorf("bpk: keyword discovery is unsupported (keyword=%q); bpk discovers by jenis sweep", keyword)
	}
	years := yearWindow(since, time.Now().UTC())
	order := jenisSweep

	// Warm the Cloudflare session before fanning out — one mint serves all
	// jenis combos (verified: cf_clearance works cross-path, and BPK handles
	// 5+ concurrent requests with the same cookie without throttling).
	if _, err := s.client.Get(ctx, challengeURL); err != nil {
		s.log.Warn("bpk: pre-warm mint failed; jenis calls will mint individually", "err", err)
	}

	// Fan out jenis types concurrently, capped at discoverConcurrency. The
	// shared fetch.Client serializes cookie access and re-mints on challenge,
	// so concurrent calls are safe.
	type result struct {
		jenis int
		docs  []ingest.DiscoveredDoc
		err   error
	}
	ch := make(chan result, len(order))
	sem := make(chan struct{}, discoverConcurrency)
	for _, j := range order {
		sem <- struct{}{}
		go func(jenis int) {
			defer func() { <-sem }()
			docs, err := s.discoverJenis(ctx, jenis, years)
			ch <- result{jenis, docs, err}
		}(j)
	}
	var (
		out     []ingest.DiscoveredDoc
		errs    []error
		nFailed int
	)
	for range order {
		r := <-ch
		// Keep the pages this jenis DID walk before it failed — the caller records
		// them and declines to advance the cursor, so nothing is lost or hidden.
		out = append(out, r.docs...)
		if r.err != nil {
			s.log.Warn("bpk jenis discover failed", "jenis", r.jenis, "partial_docs", len(r.docs), "err", r.err)
			errs = append(errs, fmt.Errorf("jenis %d: %w", r.jenis, r.err))
			nFailed++
		}
	}
	s.log.Info("bpk discover", "docs", len(out), "years", years, "jenis_failed", nFailed)
	if nFailed > 0 {
		return out, fmt.Errorf("bpk discover: %d of %d jenis failed: %w", nFailed, len(order), errors.Join(errs...))
	}
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

// discoverJenis paginates one jenis listing and returns all parsed cards.
func (s *Source) discoverJenis(ctx context.Context, jenis int, years []int) ([]ingest.DiscoveredDoc, error) {
	docType := jenisCode[jenis]
	var out []ingest.DiscoveredDoc
	lastPage := 1 // updated from pagination on first page

	for page := 1; page <= lastPage && page <= maxPages; page++ {
		u := listingURL(jenis, page, years)
		body, err := s.listingPage(ctx, u)
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

// listingPage fetches one listing page, retrying past the throttle Cloudflare
// applies to deep pagination (see pageRetryBackoff). fetch.Client's own retry
// covers brief transport blips; this one covers the longer throttle window.
func (s *Source) listingPage(ctx context.Context, u string) (string, error) {
	var lastErr error
	for attempt := 1; attempt <= pageAttempts; attempt++ {
		body, err := s.client.Get(ctx, u)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if attempt == pageAttempts {
			break
		}
		s.log.Warn("bpk listing page failed; backing off", "url", u, "attempt", attempt, "err", err)
		if err := sleep(ctx, time.Duration(attempt)*pageRetryBackoff); err != nil {
			return "", err
		}
	}
	return "", lastErr
}

// listingURL builds a search URL for the given jenis, page, and optional tahun
// year filter (multi-value, server-side). No keyword param: BPK ignores an
// unrecognized filter and returns the full listing (see jenisSweep).
func listingURL(jenis, page int, years []int) string {
	u := fmt.Sprintf("%s/Search?jenis=%d&p=%d", baseURL, jenis, page)
	for _, y := range years {
		u += fmt.Sprintf("&tahun=%d", y)
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
