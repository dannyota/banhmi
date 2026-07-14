package ojkweb

import (
	"context"
	"fmt"
	stdhtml "html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

// webJenis lists the regulation types on ojk.go.id that jdih.ojk.go.id does
// NOT expose. The Value is the dropdown option text used in postback form data.
var webJenis = []struct {
	Value   string
	DocType ingest.DocType
}{
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

	pagerLinkRe = regexp.MustCompile(`__doPostBack\('(ctl00\$PlaceHolderMain\$ctl01\$DataPagerArticles[^']+)',''\)`)

	tagRe   = regexp.MustCompile(`<[^>]+>`)
	spaceRe = regexp.MustCompile(`\s+`)
)

// aspState holds the ASP.NET hidden fields needed for postbacks.
type aspState struct {
	viewState  string
	validation string
	generator  string
	digest     string
}

// Discover enumerates the ojk.go.id listing page for each jenis type in
// webJenis and returns all discovered documents. Discovery is sweep-all — no
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

	var out []ingest.DiscoveredDoc
	for i, jenis := range webJenis {
		if i > 0 {
			if err := sleep(ctx, paceDelay); err != nil {
				return out, err
			}
		}

		s.log.Info("ojkweb: discovering jenis", "value", jenis.Value, "doctype", jenis.DocType)

		docs, newState, err := s.discoverJenis(ctx, jenis.Value, jenis.DocType, state)
		if err != nil {
			s.log.Warn("ojkweb jenis discover failed", "value", jenis.Value, "err", err)
			continue
		}
		if newState.viewState != "" {
			state = newState
		}
		out = append(out, docs...)
	}

	s.log.Info("ojkweb discover", "docs", len(out))
	return out, nil
}

// discoverJenis POSTs a filter for one jenis type and pages through results.
func (s *Source) discoverJenis(ctx context.Context, jenisValue string, docType ingest.DocType, state aspState) ([]ingest.DiscoveredDoc, aspState, error) {
	body, err := s.postFilter(ctx, jenisValue, state)
	if err != nil {
		return nil, state, fmt.Errorf("post filter jenis=%s: %w", jenisValue, err)
	}

	newState := extractASPState(body)
	if newState.viewState != "" {
		state = newState
	}

	var out []ingest.DiscoveredDoc
	page := 1

	for {
		docs := parseListingPage(body, docType)
		out = append(out, docs...)

		s.log.Debug("ojkweb: parsed listing page", "jenis", jenisValue, "page", page, "docs", len(docs))

		pagerLinks := pagerLinkRe.FindAllStringSubmatch(body, -1)
		if len(pagerLinks) == 0 || page >= 10 {
			break
		}

		var nextControl string
		for _, pl := range pagerLinks {
			nextControl = pl[1]
		}
		if nextControl == "" {
			break
		}

		if err := sleep(ctx, paceDelay); err != nil {
			return out, state, err
		}

		body, err = s.postPager(ctx, nextControl, jenisValue, state)
		if err != nil {
			s.log.Warn("ojkweb: pager failed", "jenis", jenisValue, "page", page+1, "err", err)
			break
		}

		newState = extractASPState(body)
		if newState.viewState != "" {
			state = newState
		}
		page++
	}

	s.log.Info("ojkweb jenis done", "jenis", jenisValue, "type", docType, "docs", len(out))
	return out, state, nil
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
	cookies, ua, _ := s.client.Session(ctx)
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

		number := cleanText(numberCell)

		// Skip documents whose number indicates a JDIH-covered type.
		// The SharePoint filter is unreliable and sometimes returns mixed types.
		if isJDIHCovered(number) {
			continue
		}

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

// isJDIHCovered returns true if the doc_number contains a pattern that
// indicates the document is already crawled by the ojk JDIH source.
func isJDIHCovered(number string) bool {
	return strings.Contains(number, "/POJK.") || strings.Contains(number, "/SEOJK.")
}

func cleanText(s string) string {
	s = tagRe.ReplaceAllString(s, " ")
	s = stdhtml.UnescapeString(s)
	s = strings.ReplaceAll(s, " ", " ")
	return strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
}
