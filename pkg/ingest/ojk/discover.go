package ojk

import (
	"context"
	"encoding/json"
	"fmt"
	stdhtml "html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

const (
	// pageSize is the requested records per listing POST. Live-verified
	// 2026-07-12: the server currently ignores start/length and returns every
	// row for the jenis in one response; the pagination loop below still
	// terminates correctly either way (offset catches up with the total).
	pageSize = 200

	// pacePage is the polite delay between listing requests.
	pacePage = 1 * time.Second
)

// listResponse is the DataTables-style JSON envelope from ListDataPeraturan.
// iTotalRecords is a one-element array ([560]) on the live API; parseTotal
// also accepts a bare number defensively.
type listResponse struct {
	ITotalRecords        json.RawMessage `json:"iTotalRecords"`
	ITotalDisplayRecords json.RawMessage `json:"iTotalDisplayRecords"`
	AaData               [][]string      `json:"aaData"`
}

// Listing parse patterns.
var (
	// rowLinkRe extracts UUID, sektor (may be empty — live rows use
	// /Detail/{uuid}//{jenis}), jenis, and title text from the first cell's
	// anchor. The href is absolute (http://jdih.ojk.go.id/...).
	rowLinkRe = regexp.MustCompile(`(?is)href='[^']*?/Web/ViewPeraturan/Detail/([^'/]+)/([^'/]*)/([^'/]+)'[^>]*>\s*(.*?)\s*</a>`)

	// numberTitleRe splits an official title "Peraturan Otoritas Jasa Keuangan
	// [Republik Indonesia] Nomor 9/POJK.04/2015 tentang Pedoman ..." into the
	// number ("9/POJK.04/2015" or "47 Tahun 2024") and the subject title.
	// The \s* around the number group handles no-space cases like
	// "Nomor34/POJK.05/2015tentang ..." (live edge case).
	numberTitleRe = regexp.MustCompile(`(?i)\bNomor\s*(.+?)\s*tentang\s+(.+)$`)

	// listDateRe matches the optional dd-mm-yyyy date in row cell 6.
	listDateRe = regexp.MustCompile(`^(\d{2})-(\d{2})-(\d{4})$`)

	tagRe   = regexp.MustCompile(`<[^>]+>`)
	spaceRe = regexp.MustCompile(`\s+`)
)

// Discover enumerates the jenisPeraturan listings (POJK, SEOJK, UU) via the
// ListDataPeraturan POST endpoint and returns all discovered documents.
// Discovery is sweep-all — no keyword filtering (the keyword parameter is
// ignored); OJK is a single-regulator repository entirely in scope.
//
// Incremental crawl: row cell 6 carries a dd-mm-yyyy date (pengundangan) for
// newer rows only (~20% live), used as PublishedAt. Rows with a date on or
// before `since` are skipped; undated rows are always re-emitted — the
// downstream upsert is idempotent, so this only costs cheap re-observations.
func (s *Source) Discover(ctx context.Context, since time.Time, _ string) ([]ingest.DiscoveredDoc, error) {
	var out []ingest.DiscoveredDoc
	for i, jenis := range jenisOrder {
		if i > 0 {
			if err := sleep(ctx, pacePage); err != nil {
				return out, err
			}
		}
		docs, err := s.discoverJenis(ctx, jenis, since)
		if err != nil {
			s.log.Warn("ojk jenis discover failed", "jenis", jenis, "err", err)
			continue
		}
		out = append(out, docs...)
	}
	s.log.Info("ojk discover", "docs", len(out))
	return out, nil
}

// discoverJenis pages one jenisPeraturan listing and returns its parsed rows.
func (s *Source) discoverJenis(ctx context.Context, jenis string, since time.Time) ([]ingest.DiscoveredDoc, error) {
	docType := jenisPeraturan[jenis]
	var out []ingest.DiscoveredDoc
	offset := 0

	for {
		resp, err := s.fetchListing(ctx, jenis, offset)
		if err != nil {
			return out, fmt.Errorf("listing jenis=%s offset=%d: %w", jenis, offset, err)
		}
		if len(resp.AaData) == 0 {
			break
		}

		for _, row := range resp.AaData {
			doc, ok := parseListRow(row, jenis, docType)
			if !ok {
				continue
			}
			if !since.IsZero() && !doc.PublishedAt.IsZero() && !doc.PublishedAt.After(since) {
				continue
			}
			out = append(out, doc)
		}

		offset += len(resp.AaData)
		if total := parseTotal(resp.ITotalRecords); offset >= total {
			break
		}
		if err := sleep(ctx, pacePage); err != nil {
			return out, err
		}
	}

	s.log.Info("ojk jenis done", "jenis", jenis, "type", docType, "docs", len(out))
	return out, nil
}

// fetchListing POSTs one page request to ListDataPeraturan and decodes the
// JSON envelope. The endpoint needs form encoding plus the XMLHttpRequest
// header; no WAF challenge (F5 BIG-IP cookies are set automatically).
func (s *Source) fetchListing(ctx context.Context, jenis string, offset int) (*listResponse, error) {
	form := url.Values{
		"draw":           {"1"},
		"start":          {strconv.Itoa(offset)},
		"length":         {strconv.Itoa(pageSize)},
		"jenisPeraturan": {jenis},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+listingPath, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("User-Agent", userAgent)

	resp, err := s.client.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("post listing: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("listing status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("read listing body: %w", err)
	}

	var lr listResponse
	if err := json.Unmarshal(body, &lr); err != nil {
		return nil, fmt.Errorf("decode listing json: %w", err)
	}
	return &lr, nil
}

// parseTotal extracts the record count from iTotalRecords, which the live API
// returns as a one-element array ([560]); a bare number or numeric string is
// accepted defensively.
func parseTotal(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return n
	}
	var arr []json.Number
	if err := json.Unmarshal(raw, &arr); err == nil && len(arr) > 0 {
		if i, err := strconv.Atoi(arr[0].String()); err == nil {
			return i
		}
	}
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		if i, err := strconv.Atoi(str); err == nil {
			return i
		}
	}
	return 0
}

// parseListRow maps one aaData row to a DiscoveredDoc. Live row layout
// (verified 2026-07-12): [0] title anchor HTML, [1] bare number digits,
// [2] sector, [3] sub-classification (nullable), [4] classification
// (nullable), [5] type label, [6] dd-mm-yyyy date or "", [7] status.
//
// Files are NOT emitted here: the DownloadDokumen endpoint takes per-file
// UUIDs that only the detail page exposes (they differ from the document
// UUID), so FetchDetail supplies the file references.
func parseListRow(row []string, jenis string, docType ingest.DocType) (ingest.DiscoveredDoc, bool) {
	if len(row) < 8 {
		return ingest.DiscoveredDoc{}, false
	}

	m := rowLinkRe.FindStringSubmatch(row[0])
	if m == nil {
		return ingest.DiscoveredDoc{}, false
	}
	uuid := m[1]
	sektor := m[2]
	jenisPath := m[3]
	fullTitle := cleanText(m[4])

	shortNumber, title := splitNumberTitle(fullTitle)
	if shortNumber == "" {
		// Fall back to the bare number cell (e.g. "47") — still identifying
		// alongside DocType, though the full form comes from the title.
		shortNumber = strings.TrimSpace(row[1])
	}

	pubAt := parseListDate(row[6])
	// Cap future dates: the JDIH listing occasionally carries promulgation
	// dates set ahead of today (advance publication). A future PublishedAt
	// would push the discover-cursor watermark past the present, causing all
	// subsequent incremental discoveries to skip everything.
	if !pubAt.IsZero() && pubAt.After(time.Now().UTC()) {
		pubAt = time.Time{} // drop; treated as undated
	}

	return ingest.DiscoveredDoc{
		SourceID:    SourceID,
		ExternalID:  uuid,
		Number:      bpkFormatNumber(shortNumber, docType),
		Title:       title,
		Abstract:    strings.TrimSpace(row[2]), // sector, e.g. "Perbankan"
		DocType:     docType,
		DocTypeCode: jenis,
		Status:      strings.TrimSpace(row[7]),
		DetailURL:   detailURL(uuid, sektor, jenisPath),
		PublishedAt: pubAt,
	}, true
}

// splitNumberTitle splits an official long title into (number, subject title).
// "Peraturan Otoritas Jasa Keuangan Nomor 8/POJK.05/2018 tentang Pendanaan
// Dana Pensiun" → ("8/POJK.05/2018", "Pendanaan Dana Pensiun"). When the
// pattern is absent nothing is lost: ("", full text).
func splitNumberTitle(full string) (number, title string) {
	if m := numberTitleRe.FindStringSubmatch(full); m != nil {
		return strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
	}
	return "", full
}

// slashYearRe extracts the trailing year from an old-format slash number like
// "9/POJK.04/2015" → "2015".
var slashYearRe = regexp.MustCompile(`/(\d{4})$`)

// bpkFormatNumber constructs a doc_number in BPK's canonical format so the
// pipeline's doc_key dedup merges OJK and BPK observations of the same
// regulation into one silver.document.
//
// Examples:
//
//	("40 Tahun 2024", "Peraturan Otoritas Jasa Keuangan")
//	  → "Peraturan Otoritas Jasa Keuangan Nomor 40 Tahun 2024"
//
//	("9/POJK.04/2015", "Peraturan Otoritas Jasa Keuangan")
//	  → "Peraturan Otoritas Jasa Keuangan Nomor 9/POJK.04/2015 Tahun 2015"
//
//	("29/SEOJK.03/2022", "Surat Edaran Otoritas Jasa Keuangan")
//	  → "Surat Edaran Otoritas Jasa Keuangan Nomor 29/SEOJK.03/2022"
func bpkFormatNumber(shortNumber string, docType ingest.DocType) string {
	if shortNumber == "" {
		return ""
	}
	base := string(docType) + " Nomor " + shortNumber
	// Old-format POJKs with a slash number (e.g. "9/POJK.04/2015") append
	// "Tahun YYYY" matching the trailing year. SEOJK slash numbers don't
	// carry a "Tahun" suffix in BPK.
	if strings.Contains(shortNumber, "/") && strings.Contains(strings.ToUpper(shortNumber), "POJK") {
		if m := slashYearRe.FindStringSubmatch(shortNumber); m != nil {
			base += " Tahun " + m[1]
		}
	}
	return base
}

// parseListDate parses the optional dd-mm-yyyy date from row cell 6.
func parseListDate(s string) time.Time {
	m := listDateRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return time.Time{}
	}
	day, _ := strconv.Atoi(m[1])
	month, _ := strconv.Atoi(m[2])
	year, _ := strconv.Atoi(m[3])
	if month < 1 || month > 12 || day < 1 || day > 31 {
		return time.Time{}
	}
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
}

// cleanText strips HTML tags, unescapes entities (incl. &nbsp; → NBSP → space),
// and collapses all whitespace.
func cleanText(s string) string {
	s = tagRe.ReplaceAllString(s, " ")
	s = stdhtml.UnescapeString(s)
	s = strings.ReplaceAll(s, "\u00a0", " ")
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
