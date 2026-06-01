package congbao

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode"

	"danny.vn/banhmi/pkg/ingest"
)

const searchAPIURL = "https://api-searchcongbao.chinhphu.vn/search/van-ban/nhom/vbqpp"

// searchPageSize scans enough ranked results to find the exact số-ký-hiệu match.
// The endpoint is a fuzzy full-text ranker (see SearchByNumber), so a generous
// page keeps the exact match in range once the title hint promotes it.
const searchPageSize = 50

type searchRequest struct {
	Filters  map[string]any `json:"filters"`
	Page     int            `json:"page"`
	PageSize int            `json:"page_size"`
	Query    string         `json:"query"`
}

type searchResponse struct {
	Data    []json.RawMessage `json:"data"`
	Success bool              `json:"success"`
	Total   int               `json:"total"`
}

type searchItem struct {
	ID            any             `json:"id_van_ban"`
	Number        string          `json:"so_ky_hieu"`
	Title         string          `json:"tieu_de"`
	Abstract      string          `json:"trich_yeu"`
	IssuedAt      string          `json:"ngay_ban_hanh"`
	EffectiveAt   string          `json:"ngay_co_hieu_luc"`
	DocType       string          `json:"loai_van_ban"`
	Issuers       []string        `json:"ten_co_quan"`
	Signer        string          `json:"nguoi_ky"`
	GazetteText   string          `json:"danh_sach_dang_trong_cong_bao"`
	GazetteIssues []searchGazette `json:"danh_sach_ky_cong_bao"`
	Files         []searchFile    `json:"danh_sach_tep_van_ban"`
	raw           json.RawMessage
}

type searchGazette struct {
	Name string `json:"ten"`
	Date string `json:"ngay_ban_hanh"`
}

type searchFile struct {
	URL   string `json:"duong_dan"`
	Order int    `json:"thu_tu"`
	Name  string `json:"ten_file"`
	Ext   string `json:"file_extension"`
}

// SearchByNumber looks up one legal document in the Công báo search API by its
// exact số ký hiệu. The endpoint is a fuzzy full-text ranker: a bare số-ký-hiệu
// query can rank near misses (e.g. 01/2016/TT-BTTTT) above — or instead of — the
// exact document, which may be absent from the top results entirely. Callers pass
// the known title as a disambiguating hint, which reliably promotes the exact
// match to the top; the result is still trusted only when it passes normalized
// số-ký-hiệu equality.
func (s *Source) SearchByNumber(ctx context.Context, number, titleHint string) (*ingest.DiscoveredDoc, bool, error) {
	number = strings.TrimSpace(number)
	if number == "" {
		return nil, false, nil
	}
	query := number
	if h := strings.TrimSpace(titleHint); h != "" {
		query = number + " " + h
	}

	payload, err := json.Marshal(searchRequest{
		Filters:  map[string]any{},
		Page:     1,
		PageSize: searchPageSize,
		Query:    query,
	})
	if err != nil {
		return nil, false, fmt.Errorf("build search request: %w", err)
	}
	resp, err := s.postJSON(ctx, searchAPIURL, payload, map[string]string{
		"Accept":  "application/json",
		"Origin":  baseURL,
		"Referer": refererURL,
	})
	if err != nil {
		return nil, false, fmt.Errorf("search by number %q: %w", number, err)
	}
	defer drainClose(resp.Body)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, fmt.Errorf("read search response: %w", err)
	}
	var sr searchResponse
	if err := json.Unmarshal(body, &sr); err != nil {
		return nil, false, fmt.Errorf("parse search response: %w", err)
	}

	want := normalizeDocNumber(number)
	for _, raw := range sr.Data {
		var item searchItem
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, false, fmt.Errorf("parse search item: %w", err)
		}
		if normalizeDocNumber(item.Number) != want {
			continue
		}
		item.raw = raw
		doc := item.discoveredDoc()
		return &doc, true, nil
	}
	return nil, false, nil
}

func (it searchItem) discoveredDoc() ingest.DiscoveredDoc {
	number := strings.TrimSpace(it.Number)
	title := strings.TrimSpace(it.Abstract)
	if title == "" {
		title = strings.TrimSpace(it.Title)
	}
	gazetteNumber := strings.TrimSpace(it.GazetteText)
	var gazetteDate time.Time
	if len(it.GazetteIssues) > 0 {
		if gazetteNumber == "" {
			gazetteNumber = strings.TrimSpace(it.GazetteIssues[0].Name)
		}
		gazetteDate = parseAPIDate(it.GazetteIssues[0].Date)
	}

	return ingest.DiscoveredDoc{
		SourceID:      SourceID,
		ExternalID:    searchIDString(it.ID),
		Number:        number,
		Title:         title,
		Abstract:      strings.TrimSpace(it.Abstract),
		DocType:       ingest.DocType(strings.TrimSpace(it.DocType)),
		Issuer:        strings.Join(it.Issuers, "; "),
		Signer:        strings.TrimSpace(it.Signer),
		IssuedAt:      parseAPIDate(it.IssuedAt),
		EffectiveAt:   parseAPIDate(it.EffectiveAt),
		DetailURL:     detailURLFromSearch(it.DocType, number, searchIDString(it.ID)),
		Files:         fileRefsFromSearch(it.Files),
		PublishedAt:   gazetteDate,
		GazetteNumber: gazetteNumber,
		GazetteDate:   gazetteDate,
		RawMeta:       it.raw,
	}
}

func searchIDString(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case float64:
		return fmt.Sprintf("%.0f", x)
	default:
		return strings.TrimSpace(fmt.Sprint(x))
	}
}

func fileRefsFromSearch(files []searchFile) []ingest.FileRef {
	files = append([]searchFile(nil), files...)
	sort.SliceStable(files, func(i, j int) bool {
		return files[i].Order < files[j].Order
	})
	refs := make([]ingest.FileRef, 0, len(files))
	seen := map[string]bool{}
	for _, f := range files {
		url := strings.TrimSpace(f.URL)
		if url == "" || seen[url] {
			continue
		}
		seen[url] = true
		// The search API's file_extension field is often a path tail (not "pdf"),
		// and the URL glues the format on with no dot (".../...16587pdf") while the
		// CDN serves application/octet-stream — so infer the extension from a known
		// trailing token on the URL instead of trusting file_extension.
		ext := extFromCongbaoURL(url)
		name := strings.TrimSpace(f.Name)
		if name == "" {
			name = "congbao-file"
		}
		if ext != "" && !strings.HasSuffix(strings.ToLower(name), "."+ext) {
			name += "." + ext
		}
		refs = append(refs, ingest.FileRef{
			URL:      url,
			Name:     name,
			Ext:      ext,
			Kind:     "main",
			MIMEType: mimeForExt(ext),
		})
	}
	return refs
}

// extFromCongbaoURL infers a file extension from a Công báo file URL. The search
// API appends the format to the path with no separator (".../...16587pdf"), so
// filepath.Ext and the file_extension field are both unreliable; match a known
// document extension as a trailing token (longest first) instead.
func extFromCongbaoURL(url string) string {
	u := strings.ToLower(strings.TrimSpace(url))
	for _, ext := range []string{"docx", "pdf", "doc", "rtf"} {
		if strings.HasSuffix(u, ext) {
			return ext
		}
	}
	return ""
}

func detailURLFromSearch(docType, number, id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	slug := documentSlug(docType, number)
	if slug == "" {
		slug = "van-ban"
	}
	return baseURL + "/van-ban/" + slug + "-" + id + ".htm"
}

func documentSlug(docType, number string) string {
	prefix := "van-ban"
	docType = strings.TrimSpace(docType)
	for _, p := range docTypePrefixes {
		if string(p.docType) == docType {
			prefix = p.prefix
			break
		}
	}
	numberSlug := slugPart(number)
	if numberSlug == "" {
		return prefix
	}
	return prefix + "-so-" + numberSlug
}

func slugPart(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastHyphen := true
	for _, r := range s {
		r = foldDiacritic(r)
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastHyphen = false
			continue
		}
		if !lastHyphen {
			b.WriteByte('-')
			lastHyphen = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func normalizeDocNumber(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		r = foldDiacritic(r)
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func parseAPIDate(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "null") {
		return time.Time{}
	}
	layouts := []string{
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02T15:04:05.999999999",
		"2006-01-02",
	}
	for _, layout := range layouts {
		t, err := time.Parse(layout, s)
		if err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
