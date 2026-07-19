package ojkweb

import (
	"context"
	"errors"
	"fmt"
	stdhtml "html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

// webJenis lists all regulation types on ojk.go.id. POJK, SEOJK, and UU
// overlap with jdih.ojk.go.id but ojkweb carries PDFs that jdih often
// WAF-blocks; the pipeline deduplicates by doc_key at normalize time.
//
// POJK and SEOJK carry the full Indonesian type name — the same doc_type the
// bpk and ojk sources store — so observations of the same regulation converge
// on one silver doc_key instead of forking per source (see canonicalNumber).
var webJenis = []struct {
	Value   string
	DocType ingest.DocType
}{
	{"Peraturan OJK", pojkFullName},
	{"Surat Edaran OJK", seojkFullName},
	{"Undang-Undang", "UU"},
	{"Peraturan ADK", "PADK"},
	{"Peraturan Pemerintah", "PP"},
	{"Peraturan/Keputusan Mentri", "PMK"},
	{"PPBI", "PPBI"},
	{"SEBI", "SEBI"},
	{"Klasifikasi Bapepam", "Bapepam"},
	{"Peraturan Bapepam", "Bapepam"},
}

// ASP.NET form field names (SharePoint postback).
const (
	fieldJenis       = "ctl00$PlaceHolderMain$ctl01$DropDownListJenisRegulasi"
	fieldSektor      = "ctl00$PlaceHolderMain$ctl01$DropDownListSektor"
	fieldSubSektor   = "ctl00$PlaceHolderMain$ctl01$DropDownListSubSektor"
	fieldTahun       = "ctl00$PlaceHolderMain$ctl01$DropDownListTahun"
	fieldSearch      = "ctl00$PlaceHolderMain$ctl01$ButtonSearch"
	fieldViewState   = "__VIEWSTATE"
	fieldValidation  = "__EVENTVALIDATION"
	fieldGenerator   = "__VIEWSTATEGENERATOR"
	fieldEventTarget = "__EVENTTARGET"
	fieldEventArg    = "__EVENTARGUMENT"
	fieldDigest      = "__REQUESTDIGEST"
	fieldDisplayMode = "MSOSPWebPartManager_DisplayModeName"
)

// Listing parse patterns.
var (
	viewStateRe  = regexp.MustCompile(`id="__VIEWSTATE"\s+value="([^"]*)"`)
	validationRe = regexp.MustCompile(`id="__EVENTVALIDATION"\s+value="([^"]*)"`)
	generatorRe  = regexp.MustCompile(`id="__VIEWSTATEGENERATOR"\s+value="([^"]*)"`)
	digestRe     = regexp.MustCompile(`id="__REQUESTDIGEST"\s+value="([^"]*)"`)

	listRowRe  = regexp.MustCompile(`(?is)<tr>\s*<td[^>]*>(.*?)</td>\s*<td[^>]*>(.*?)</td>\s*</tr>`)
	listLinkRe = regexp.MustCompile(`(?is)<a[^>]*id="[^"]*HyperlinkTitle"[^>]*href="([^"]*)"[^>]*>(.*?)</a>`)
	captionRe  = regexp.MustCompile(`(?is)<div\s+class="caption"[^>]*>(.*?)</div>`)

	// pagerLinkRe matches a pager link's control ID and visible label (page
	// number, "...", or empty for the arrow). The HTML encodes quotes as &#39;.
	pagerLinkRe = regexp.MustCompile(`__doPostBack\((?:'|&#39;)(ctl00\$PlaceHolderMain\$ctl01\$DataPagerArticles[^'&]+)(?:'|&#39;),(?:'|&#39;)(?:'|&#39;)\)">([^<]*)</a>`)

	tagRe   = regexp.MustCompile(`<[^>]+>`)
	spaceRe = regexp.MustCompile(`\s+`)
)

// Full Indonesian type names as stored by the bpk and ojk sources — the
// doc_key convergence targets (verified against the live corpus 2026-07-16).
const (
	pojkFullName  = "Peraturan Otoritas Jasa Keuangan"
	seojkFullName = "Surat Edaran Otoritas Jasa Keuangan"
)

// Nomor shape patterns for canonicalNumber.
var (
	// slashTightRe tightens spaces around "/" ("40 /POJK.05/2020" → "40/POJK.05/2020").
	slashTightRe = regexp.MustCompile(`\s*/\s*`)
	// pojkSectorNumRe matches the sub-sector-coded POJK shape "40/POJK.03/2019".
	pojkSectorNumRe = regexp.MustCompile(`^(\d+)/POJK\.\d+/(\d{4})$`)
	// seojkSectorNumRe matches the sub-sector-coded SEOJK shape "29/SEOJK.03/2022".
	seojkSectorNumRe = regexp.MustCompile(`^(\d+)/SEOJK\.\d+/(\d{4})$`)
	// plainNumRe matches the post-2022 plain shape "40 Tahun 2024".
	plainNumRe = regexp.MustCompile(`^(?i)\d+\s+Tahun\s+\d{4}$`)
)

// canonicalNumber rewrites the short nomor shown on ojk.go.id into the full
// citation form the bpk and ojk sources store, so all three sources' doc_keys
// converge on one silver document (shapes verified on the live corpus):
//
//	"40 Tahun 2024" (POJK)  → "Peraturan Otoritas Jasa Keuangan Nomor 40 Tahun 2024"
//	"40/POJK.03/2019"       → "Peraturan Otoritas Jasa Keuangan Nomor 40/POJK.03/2019 Tahun 2019"
//	"29/SEOJK.03/2022"      → "Surat Edaran Otoritas Jasa Keuangan Nomor 29/SEOJK.03/2022"
//
// BPK and JDIH append a redundant "Tahun YYYY" to sub-sector POJK numbers but
// not to SEOJK ones; the rules mirror that observed convention. Numbers of
// other types (PADK, PPBI, SEBI, UU, PP, PMK, Bapepam) return unchanged —
// PPBI/SEBI converge with the bi source's bare form ("7/38/PBI/2005"), and the
// rest have no cross-source counterpart convention yet.
func canonicalNumber(number string, docType ingest.DocType) string {
	n := strings.TrimSpace(slashTightRe.ReplaceAllString(number, "/"))
	if n == "" {
		return number
	}
	// Already canonical — idempotent.
	if strings.HasPrefix(n, pojkFullName) || strings.HasPrefix(n, seojkFullName) {
		return n
	}
	if m := pojkSectorNumRe.FindStringSubmatch(n); m != nil {
		return pojkFullName + " Nomor " + n + " Tahun " + m[2]
	}
	if seojkSectorNumRe.MatchString(n) {
		return seojkFullName + " Nomor " + n
	}
	if plainNumRe.MatchString(n) {
		switch string(docType) {
		case pojkFullName:
			return pojkFullName + " Nomor " + n
		case seojkFullName:
			return seojkFullName + " Nomor " + n
		}
	}
	return number
}

// canonicalDetailNumber canonicalizes the nomor from a detail page, where the
// regulation type is not reliably displayed. Sub-sector-coded shapes identify
// their own type and canonicalize; the plain "N Tahun YYYY" shape is
// type-ambiguous at detail time, so it returns "" — the bronze upsert
// COALESCEs the NULL away and keeps the canonical number written at discovery
// time. Other shapes pass through verbatim.
func canonicalDetailNumber(number string) string {
	if n := canonicalNumber(number, ""); n != number {
		return n
	}
	if plainNumRe.MatchString(strings.TrimSpace(number)) {
		return ""
	}
	return number
}

// aspState holds the ASP.NET hidden fields needed for postbacks.
type aspState struct {
	viewState  string
	validation string
	generator  string
	digest     string
}

// Discover enumerates the ojk.go.id listing page for each jenis type in
// webJenis and returns all discovered documents. Jenis types are discovered
// in parallel (each gets its own ASP.NET ViewState chain via a fresh filter
// POST); pages within a jenis are sequential. Discovery is sweep-all — no
// keyword filtering; the keyword parameter is ignored. The since watermark is
// unused (the listing page does not expose reliable dates for filtering).
func (s *Source) Discover(ctx context.Context, since time.Time, _ string) ([]ingest.DiscoveredDoc, error) {
	body, err := s.client.Get(ctx, challengeURL)
	if err != nil {
		return nil, fmt.Errorf("initial listing page: %w", err)
	}

	state := extractASPState(body)
	if state.viewState == "" {
		return nil, fmt.Errorf("no __VIEWSTATE found in listing page")
	}

	type result struct {
		docs []ingest.DiscoveredDoc
		err  error
	}

	results := make([]result, len(webJenis))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 3)
	for i, jenis := range webJenis {
		wg.Add(1)
		go func(idx int, j struct {
			Value   string
			DocType ingest.DocType
		}) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			s.log.Info("ojkweb: discovering jenis", "value", j.Value, "doctype", j.DocType)
			docs, _, err := s.discoverJenis(ctx, j.Value, j.DocType, state)
			if err != nil {
				s.log.Warn("ojkweb jenis discover failed", "value", j.Value, "err", err)
			}
			results[idx] = result{docs: docs, err: err}
		}(i, jenis)
	}
	wg.Wait()

	var out []ingest.DiscoveredDoc
	var errs []error
	for _, r := range results {
		if r.err != nil {
			errs = append(errs, r.err)
		}
		out = append(out, r.docs...)
	}

	// All jenis failed — return combined error so the caller sees total failure.
	if len(errs) == len(webJenis) {
		return nil, fmt.Errorf("all %d jenis types failed: %w", len(errs), errors.Join(errs...))
	}

	s.log.Info("ojkweb discover", "docs", len(out), "jenis_errors", len(errs))
	return out, nil
}

// pagerInfo holds the parsed pager links from one page response.
type pagerInfo struct {
	// numbered maps visible page numbers to their postback control IDs.
	numbered map[int]string
	// ellipsisCtl is the "..." control ID, empty if absent.
	ellipsisCtl string
}

// parsePager extracts page-number links and the "..." link from the pager.
func parsePager(body string) pagerInfo {
	info := pagerInfo{numbered: make(map[int]string)}
	for _, m := range pagerLinkRe.FindAllStringSubmatch(body, -1) {
		ctl, label := m[1], strings.TrimSpace(m[2])
		if label == "..." {
			info.ellipsisCtl = ctl
		} else if n := parseInt(label); n > 0 {
			info.numbered[n] = ctl
		}
	}
	return info
}

func parseInt(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	if s == "" {
		return 0
	}
	return n
}

// discoverJenis POSTs a filter for one jenis type, then fans out pages in
// parallel per pager window. Each window shows ~10 page links; numbered pages
// are fetched concurrently, then "..." advances to the next window.
func (s *Source) discoverJenis(ctx context.Context, jenisValue string, docType ingest.DocType, state aspState) ([]ingest.DiscoveredDoc, aspState, error) {
	body, err := s.postFilter(ctx, jenisValue, state)
	if err != nil {
		return nil, state, fmt.Errorf("post filter jenis=%s: %w", jenisValue, err)
	}

	state = extractASPState(body)

	// Page 1 is already in body.
	var out []ingest.DiscoveredDoc
	out = append(out, parseListingPage(body, docType)...)
	seen := map[int]bool{1: true}

	for window := 0; window < 50; window++ {
		pager := parsePager(body)
		if len(pager.numbered) == 0 {
			break
		}

		// Fan out unseen pages in this window.
		type pageResult struct {
			page int
			docs []ingest.DiscoveredDoc
		}
		var unseen []struct {
			page int
			ctl  string
		}
		for pg, ctl := range pager.numbered {
			if !seen[pg] {
				unseen = append(unseen, struct {
					page int
					ctl  string
				}{pg, ctl})
			}
		}

		if len(unseen) > 0 {
			results := make([]pageResult, len(unseen))
			var wg sync.WaitGroup
			for i, u := range unseen {
				wg.Add(1)
				go func(idx int, pg int, ctl string) {
					defer wg.Done()
					resp, err := s.postPager(ctx, ctl, jenisValue, state)
					if err != nil {
						s.log.Warn("ojkweb: page failed", "jenis", jenisValue, "page", pg, "err", err)
						return
					}
					results[idx] = pageResult{page: pg, docs: parseListingPage(resp, docType)}
				}(i, u.page, u.ctl)
			}
			wg.Wait()

			for _, r := range results {
				seen[r.page] = true
				out = append(out, r.docs...)
			}
		}

		// Advance to next window via "...".
		if pager.ellipsisCtl == "" {
			break
		}
		body, err = s.postPager(ctx, pager.ellipsisCtl, jenisValue, state)
		if err != nil {
			s.log.Warn("ojkweb: ellipsis failed", "jenis", jenisValue, "err", err)
			break
		}
		state = extractASPState(body)

		// The "..." response is itself a page — parse its docs too.
		pager2 := parsePager(body)
		currentPage := 0
		for pg := range pager2.numbered {
			if !seen[pg] && (currentPage == 0 || pg < currentPage) {
				currentPage = pg
			}
		}
		// The current page of the "..." response is the page just before
		// the first unseen numbered link (i.e. the page we landed on).
		if cp := parseInt(extractCurrentPage(body)); cp > 0 && !seen[cp] {
			seen[cp] = true
			out = append(out, parseListingPage(body, docType)...)
		}
	}

	s.log.Info("ojkweb jenis done", "jenis", jenisValue, "type", docType, "pages", len(seen), "docs", len(out))
	return out, state, nil
}

// extractCurrentPage returns the current page number from the pager's
// <span class="currentPagingButton">N</span>.
func extractCurrentPage(body string) string {
	re := regexp.MustCompile(`currentPagingButton">(\d+)</span>`)
	if m := re.FindStringSubmatch(body); m != nil {
		return m[1]
	}
	return ""
}

func (s *Source) postFilter(ctx context.Context, jenisValue string, state aspState) (string, error) {
	form := url.Values{
		fieldViewState:   {state.viewState},
		fieldValidation:  {state.validation},
		fieldGenerator:   {state.generator},
		fieldDigest:      {state.digest},
		fieldDisplayMode: {"Browse"},
		fieldEventTarget: {""},
		fieldEventArg:    {""},
		fieldJenis:       {jenisValue},
		fieldSektor:      {"0"},
		fieldSubSektor:   {"0"},
		fieldTahun:       {"0"},
		fieldSearch:      {"Cari"},
	}
	return s.doPost(ctx, form)
}

func (s *Source) postPager(ctx context.Context, controlID, jenisValue string, state aspState) (string, error) {
	form := url.Values{
		fieldViewState:   {state.viewState},
		fieldValidation:  {state.validation},
		fieldGenerator:   {state.generator},
		fieldDigest:      {state.digest},
		fieldDisplayMode: {"Browse"},
		fieldEventTarget: {controlID},
		fieldEventArg:    {""},
		fieldJenis:       {jenisValue},
		fieldSektor:      {"0"},
		fieldSubSektor:   {"0"},
		fieldTahun:       {"0"},
	}
	return s.doPost(ctx, form)
}

func (s *Source) doPost(ctx context.Context, form url.Values) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, challengeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("build post request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*")
	req.Header.Set("Referer", challengeURL)
	cookies, ua, err := s.client.Session(ctx)
	if err != nil {
		return "", fmt.Errorf("mint session: %w", err)
	}
	if ua == "" {
		ua = userAgent
	}
	req.Header.Set("User-Agent", ua)
	if cookies != "" {
		req.Header.Set("Cookie", cookies)
	}

	resp, err := s.client.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("post listing: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 512))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("listing POST status %d", resp.StatusCode)
	}

	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
	if err != nil {
		return "", fmt.Errorf("read listing body: %w", err)
	}
	return string(b), nil
}

func extractASPState(body string) aspState {
	return aspState{
		viewState:  extractField(viewStateRe, body),
		validation: extractField(validationRe, body),
		generator:  extractField(generatorRe, body),
		digest:     extractField(digestRe, body),
	}
}

func extractField(re *regexp.Regexp, body string) string {
	m := re.FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	return m[1]
}

func parseListingPage(body string, docType ingest.DocType) []ingest.DiscoveredDoc {
	var out []ingest.DiscoveredDoc

	for _, row := range listRowRe.FindAllStringSubmatch(body, -1) {
		numberCell := row[1]
		titleCell := row[2]

		lm := listLinkRe.FindStringSubmatch(titleCell)
		if lm == nil {
			continue
		}
		href := lm[1]
		title := cleanText(lm[2])

		if !strings.Contains(href, "/regulasi/Pages/") {
			continue
		}

		raw := cleanText(numberCell)

		// ojk.go.id's Undang-Undang listing is noisy: it also returns
		// SEOJK/POJK sub-sector rows (site mis-tagging). A genuine UU number
		// never contains "/".
		if docType == "UU" && strings.Contains(raw, "/") {
			continue
		}

		number := canonicalNumber(raw, docType)

		var sector, year string
		if cm := captionRe.FindStringSubmatch(titleCell); cm != nil {
			parts := strings.Split(cleanText(cm[1]), "•")
			if len(parts) >= 2 {
				sector = strings.TrimSpace(parts[1])
			}
			if len(parts) >= 3 {
				year = strings.TrimSpace(parts[2])
			}
		}

		detailURL := href
		if strings.HasPrefix(href, "/") {
			detailURL = baseURL + href
		}

		externalID := strings.TrimPrefix(href, "/")

		doc := ingest.DiscoveredDoc{
			SourceID:   SourceID,
			ExternalID: externalID,
			Number:     number,
			Title:      title,
			Abstract:   sector,
			DocType:    docType,
			DetailURL:  detailURL,
		}
		if year != "" {
			doc.DocTypeCode = year
		}
		out = append(out, doc)
	}
	return out
}

func cleanText(s string) string {
	s = tagRe.ReplaceAllString(s, " ")
	s = stdhtml.UnescapeString(s)
	s = strings.ReplaceAll(s, " ", " ")
	return strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
}
