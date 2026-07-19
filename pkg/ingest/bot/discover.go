package bot

import (
	"context"
	"errors"
	"fmt"
	stdhtml "html"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

// docGroups maps the DocGroup POST field value to its label.
// 1 = Financial Institutions (primary), 3 = Payment Systems (primary).
var docGroups = []struct {
	code  string
	label string
}{
	{"1", "Financial Institutions"},
	{"3", "Payment Systems"},
}

const (
	listingPath = "/Thai/PFIPCS_list.aspx"

	// ASP.NET event target for page navigation via the dropdown page selector.
	pageEventTarget = "ctl00$ContentPlaceHolder1$dgDocument$ctl33$ddlPageSelector"

	maxPages = 200 // safety cap; ~1,560 docs / 30 rows = ~52 pages per group
)

// Regex patterns for listing page parsing.
var (
	// rowStartRe locates the start of each document row.
	// The real HTML contains nested <table><tr>…</tr></table> inside the title
	// cell, so a simple <tr>…</tr> match truncates at the inner </tr>. Instead
	// we find each row start and split the body at these positions.
	rowStartRe = regexp.MustCompile(`(?i)<tr\s+class="nonebgnews(?:White|Gray)"[^>]*>`)

	// topCellRe splits a row into its top-level <td> cells. Each outer cell
	// carries a class attribute (namenews, datenews, tx-news) while inner
	// nested cells don't, so we split on `<td class=` boundaries.
	topCellRe = regexp.MustCompile(`(?i)<td\s+class="`)

	// packIdRe extracts the packId from the JavaScript OpenWindow call.
	// OpenWindow('PFIPCS_summary.aspx?packId=25670003','summary')
	packIdRe = regexp.MustCompile(`OpenWindow\s*\(\s*'[^']*packId=(\d+)'`)

	// pdfLinkRe extracts PDF URLs from href attributes.
	pdfLinkRe = regexp.MustCompile(`(?i)href='([^']+\.pdf)'`)

	// statusImgRe extracts the alt text from the status icon image.
	statusImgRe = regexp.MustCompile(`(?i)<img[^>]+alt='([^']+)'`)

	// pageCountRe extracts the total page count from the page selector dropdown.
	// <option value="2">2</option> ... last option is the page count.
	pageOptionRe = regexp.MustCompile(`(?is)<select[^>]*name="[^"]*ddlPageSelector"[^>]*>(.*?)</select>`)
	optionRe     = regexp.MustCompile(`<option[^>]*value="(\d+)"`)

	// tagRe strips HTML tags.
	tagRe = regexp.MustCompile(`(?s)<[^>]+>`)

	// spaceRe collapses whitespace.
	spaceRe = regexp.MustCompile(`\s+`)
)

// thaiMonths maps Thai month abbreviations to time.Month.
var thaiMonths = map[string]time.Month{
	"ม.ค.":  time.January,
	"ก.พ.":  time.February,
	"มี.ค.": time.March,
	"เม.ย.": time.April,
	"พ.ค.":  time.May,
	"มิ.ย.": time.June,
	"ก.ค.":  time.July,
	"ส.ค.":  time.August,
	"ก.ย.":  time.September,
	"ต.ค.":  time.October,
	"พ.ย.":  time.November,
	"ธ.ค.":  time.December,
}

// thaiDateRe matches a Thai date like "16 ก.ค. 2569".
var thaiDateRe = regexp.MustCompile(`(\d{1,2})\s+(\S+\.)\s+(\d{4})`)

// Discover crawls both in-scope DocGroups (Financial Institutions and Payment
// Systems) and returns all documents. keyword is ignored (BOT uses DocGroup
// filtering, not keyword search). The since parameter is not used for
// incremental crawl — BOT has no server-side date filter; the pipeline's
// cursor-based dedup handles incremental updates.
//
// Partial failure contract: if one DocGroup fails, documents from the
// successful group are still returned alongside a non-nil error. The pipeline
// must not advance the cursor on error — upserts are idempotent.
func (s *Source) Discover(ctx context.Context, _ time.Time, _ string) ([]ingest.DiscoveredDoc, error) {
	seen := map[string]bool{}
	var (
		out     []ingest.DiscoveredDoc
		errs    []error
		nFailed int
	)
	for _, dg := range docGroups {
		docs, err := s.discoverGroup(ctx, dg.code, dg.label)
		if err != nil {
			s.log.Warn("bot docgroup discover failed", "group", dg.label, "code", dg.code, "err", err)
			errs = append(errs, fmt.Errorf("docgroup %s (%s): %w", dg.code, dg.label, err))
			nFailed++
		}
		for _, d := range docs {
			if seen[d.ExternalID] {
				continue
			}
			seen[d.ExternalID] = true
			out = append(out, d)
		}
	}
	s.log.Info("bot discover", "docs", len(out), "groups_failed", nFailed)
	if nFailed > 0 {
		return out, fmt.Errorf("bot discover: %d of %d groups failed: %w", nFailed, len(docGroups), errors.Join(errs...))
	}
	return out, nil
}

// discoverGroup crawls one DocGroup listing using ASP.NET WebForms pagination.
func (s *Source) discoverGroup(ctx context.Context, groupCode, groupLabel string) ([]ingest.DiscoveredDoc, error) {
	// Step 1: GET the listing page to establish session and get initial ViewState.
	listURL := s.baseURL + listingPath
	body, err := s.get(ctx, listURL)
	if err != nil {
		return nil, fmt.Errorf("initial GET: %w", err)
	}
	vs := extractViewState(body)

	// Step 2: POST to select the DocGroup filter.
	body, err = s.postForm(ctx, listURL, url.Values{
		"__VIEWSTATE":                         {vs.ViewState},
		"__VIEWSTATEGENERATOR":                {vs.ViewStateGenerator},
		"__EVENTVALIDATION":                   {vs.EventValidation},
		"__VIEWSTATEENCRYPTED":                {vs.ViewStateEncrypted},
		"ctl00$ContentPlaceHolder1$DocGroup":  {groupCode},
		"ctl00$ContentPlaceHolder1$ddlStatus": {"0"}, // all statuses
		"__EVENTTARGET":                       {"ctl00$ContentPlaceHolder1$DocGroup"},
		"__EVENTARGUMENT":                     {""},
	})
	if err != nil {
		return nil, fmt.Errorf("docgroup POST: %w", err)
	}
	vs = extractViewState(body)

	// Parse first page.
	totalPages := parsePageCount(body)
	if totalPages == 0 {
		totalPages = 1
	}
	if totalPages > maxPages {
		totalPages = maxPages
	}

	var out []ingest.DiscoveredDoc
	rows := parseRows(body)
	out = append(out, rows...)
	s.log.Debug("bot discover page", "group", groupLabel, "page", 1, "total_pages", totalPages, "rows", len(rows))

	// Paginate through remaining pages.
	for page := 2; page <= totalPages; page++ {
		if err := sleep(ctx, pacePage); err != nil {
			return out, err
		}

		body, err = s.postForm(ctx, listURL, url.Values{
			"__VIEWSTATE":                         {vs.ViewState},
			"__VIEWSTATEGENERATOR":                {vs.ViewStateGenerator},
			"__EVENTVALIDATION":                   {vs.EventValidation},
			"__VIEWSTATEENCRYPTED":                {vs.ViewStateEncrypted},
			"__EVENTTARGET":                       {pageEventTarget},
			"__EVENTARGUMENT":                     {""},
			"ctl00$ContentPlaceHolder1$DocGroup":  {groupCode},
			"ctl00$ContentPlaceHolder1$ddlStatus": {"0"},
			"ctl00$ContentPlaceHolder1$dgDocument$ctl33$ddlPageSelector": {strconv.Itoa(page)},
		})
		if err != nil {
			return out, fmt.Errorf("page %d POST: %w", page, err)
		}
		vs = extractViewState(body)

		rows = parseRows(body)
		out = append(out, rows...)
		s.log.Debug("bot discover page", "group", groupLabel, "page", page, "rows", len(rows))

		if len(rows) == 0 {
			break
		}
	}

	s.log.Info("bot docgroup done", "group", groupLabel, "code", groupCode, "docs", len(out))
	return out, nil
}

// get fetches a URL with bounded retries on 429/5xx.
func (s *Source) get(ctx context.Context, rawURL string) (string, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			if err := sleep(ctx, time.Duration(float64(baseBackoff)*math.Pow(2, float64(attempt-1)))); err != nil {
				return "", err
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return "", fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
		resp, err := s.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
			drainClose(resp.Body)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			drainClose(resp.Body)
			return "", fmt.Errorf("status %d", resp.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		drainClose(resp.Body)
		if err != nil {
			return "", fmt.Errorf("read body: %w", err)
		}
		return string(body), nil
	}
	if lastErr == nil {
		lastErr = errors.New("exhausted retries")
	}
	return "", lastErr
}

// postForm sends a POST with form-encoded values and returns the response body.
func (s *Source) postForm(ctx context.Context, rawURL string, values url.Values) (string, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			if err := sleep(ctx, time.Duration(float64(baseBackoff)*math.Pow(2, float64(attempt-1)))); err != nil {
				return "", err
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, strings.NewReader(values.Encode()))
		if err != nil {
			return "", fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
		resp, err := s.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
			drainClose(resp.Body)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			drainClose(resp.Body)
			return "", fmt.Errorf("status %d", resp.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		drainClose(resp.Body)
		if err != nil {
			return "", fmt.Errorf("read body: %w", err)
		}
		return string(body), nil
	}
	if lastErr == nil {
		lastErr = errors.New("exhausted retries")
	}
	return "", lastErr
}

// parseRows extracts DiscoveredDocs from all table rows in the HTML body.
// The real BOT HTML nests <table><tr>…</tr></table> inside the title cell,
// so we cannot use a simple <tr>…</tr> match. Instead we split the body at
// each row-start marker and parse the fragment up to the next marker.
func parseRows(body string) []ingest.DiscoveredDoc {
	locs := rowStartRe.FindAllStringIndex(body, -1)
	if len(locs) == 0 {
		return nil
	}
	var out []ingest.DiscoveredDoc
	for i, loc := range locs {
		start := loc[1] // after the opening <tr ...>
		var end int
		if i+1 < len(locs) {
			end = locs[i+1][0]
		} else {
			end = len(body)
		}
		if d, ok := parseRow(body[start:end]); ok {
			out = append(out, d)
		}
	}
	return out
}

// splitTopCells splits a row fragment into its top-level <td> cells.
// Inner nested cells (no class attribute) are included in their parent cell's
// text, not split out. Each outer cell carries class="namenews|datenews|tx-news".
func splitTopCells(rowHTML string) []string {
	locs := topCellRe.FindAllStringIndex(rowHTML, -1)
	if len(locs) == 0 {
		return nil
	}
	cells := make([]string, len(locs))
	for i, loc := range locs {
		start := loc[0]
		var end int
		if i+1 < len(locs) {
			end = locs[i+1][0]
		} else {
			end = len(rowHTML)
		}
		cells[i] = rowHTML[start:end]
	}
	return cells
}

// parseRow parses a single table row fragment into a DiscoveredDoc.
// Columns: 0=DocType, 1=Date, 2=NewIcon, 3=Title+packId, 4=Status, 5=PDF links.
func parseRow(rowHTML string) (ingest.DiscoveredDoc, bool) {
	cells := splitTopCells(rowHTML)
	if len(cells) < 6 {
		return ingest.DiscoveredDoc{}, false
	}

	// Column 0: document type.
	docType := cleanText(cells[0])

	// Column 1: date in Thai format.
	dateText := cleanText(cells[1])
	issuedAt := parseThaiDate(dateText)

	// Column 3: title + packId.
	titleCell := cells[3]
	title := cleanText(titleCell)

	// Extract packId from JavaScript OpenWindow call.
	packID := ""
	if pm := packIdRe.FindStringSubmatch(titleCell); pm != nil {
		packID = pm[1]
	}
	if packID == "" {
		return ingest.DiscoveredDoc{}, false
	}

	// Column 4: status from img alt.
	statusCell := cells[4]
	status := "active"
	if sm := statusImgRe.FindStringSubmatch(statusCell); sm != nil {
		alt := strings.TrimSpace(sm[1])
		if alt == "ยกเลิก" {
			status = "revoked"
		}
	}

	// Column 5: PDF links.
	var files []ingest.FileRef
	for _, pm := range pdfLinkRe.FindAllStringSubmatch(cells[5], -1) {
		pdfURL := pm[1]
		// Resolve relative URLs.
		if !strings.HasPrefix(pdfURL, "http") {
			pdfURL = "https://www.bot.or.th" + pdfURL
		}
		name := packID + ".pdf"
		if parts := strings.Split(pdfURL, "/"); len(parts) > 0 {
			if decoded, err := url.PathUnescape(parts[len(parts)-1]); err == nil && decoded != "" {
				name = decoded
			}
		}
		files = append(files, ingest.FileRef{
			URL:      pdfURL,
			Name:     name,
			Ext:      "pdf",
			Kind:     "main",
			MIMEType: "application/pdf",
		})
	}

	return ingest.DiscoveredDoc{
		SourceID:   SourceID,
		ExternalID: packID,
		Title:      title,
		DocType:    ingest.DocType(docType),
		Status:     status,
		IssuedAt:   issuedAt,
		Files:      files,
	}, true
}

// parseThaiDate parses a Thai-format date like "16 ก.ค. 2569".
// The year is in Buddhist Era (CE = BE - 543).
func parseThaiDate(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	m := thaiDateRe.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}
	}
	day, err := strconv.Atoi(m[1])
	if err != nil || day < 1 || day > 31 {
		return time.Time{}
	}
	month, ok := thaiMonths[m[2]]
	if !ok {
		return time.Time{}
	}
	yearBE, err := strconv.Atoi(m[3])
	if err != nil {
		return time.Time{}
	}
	yearCE := yearBE - 543
	return time.Date(yearCE, month, day, 0, 0, 0, 0, time.UTC)
}

// parsePageCount extracts the total number of pages from the ddlPageSelector dropdown.
func parsePageCount(body string) int {
	selMatch := pageOptionRe.FindStringSubmatch(body)
	if selMatch == nil {
		return 0
	}
	options := optionRe.FindAllStringSubmatch(selMatch[1], -1)
	if len(options) == 0 {
		return 0
	}
	// Last option value is the highest page number.
	last := options[len(options)-1][1]
	n, err := strconv.Atoi(last)
	if err != nil {
		return 0
	}
	return n
}

func cleanText(s string) string {
	s = tagRe.ReplaceAllString(s, " ")
	s = stdhtml.UnescapeString(s)
	return strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
}
