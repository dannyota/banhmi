package congbao

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

// rssPath is the newest-first feed of freshly published documents. <link> is
// the detail URL ending in the numeric doc id; <pubDate> is the watermark;
// <title> is empty so we parse số ký hiệu/type from the slug, and use
// <description> as the trích yếu.
const rssPath = "/cac-van-ban-moi-ban-hanh.rss"

type rssFeed struct {
	XMLName xml.Name  `xml:"rss"`
	Items   []rssItem `xml:"channel>item"`
}

type rssItem struct {
	Title       string `xml:"title"`
	Description string `xml:"description"`
	Link        string `xml:"link"`
	GUID        string `xml:"guid"`
	PubDate     string `xml:"pubDate"`
}

// slugIDRe extracts the trailing numeric doc id from a detail-page slug:
// /van-ban/{slug}-{id}.htm
var slugIDRe = regexp.MustCompile(`-(\d+)\.htm$`)

// Discover fetches the RSS feed and returns documents published strictly after
// since (pass the zero time to take the whole feed). Items are returned in feed
// order (newest first). Each result carries the cheaply derivable fields
// (number, doc type, id, title, dates); call FetchDetail to enrich with the
// metadata table and download links.
func (s *Source) Discover(ctx context.Context, since time.Time, _ string) ([]ingest.DiscoveredDoc, error) {
	resp, err := s.get(ctx, baseURL+rssPath, crawlerUA, nil)
	if err != nil {
		return nil, fmt.Errorf("fetch rss: %w", err)
	}
	defer drainClose(resp.Body)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read rss: %w", err)
	}

	var feed rssFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("parse rss: %w", err)
	}

	docs := make([]ingest.DiscoveredDoc, 0, len(feed.Items))
	for _, it := range feed.Items {
		link := canonicalDetailURL(it.Link, it.GUID)
		if link == "" {
			continue
		}
		id, slug := parseSlug(link)
		if id == "" {
			// Without the numeric detail id the ledger cannot fetch the page
			// idempotently. Skip malformed feed items rather than enqueueing a
			// row that cannot progress.
			continue
		}
		pub := parseRSSDate(it.PubDate)
		if !since.IsZero() && !pub.IsZero() && !pub.After(since) {
			continue
		}

		number, docType := parseNumberAndType(slug)
		title := cleanText(it.Description)
		if title == "" {
			title = cleanText(it.Title)
		}

		docs = append(docs, ingest.DiscoveredDoc{
			SourceID:    SourceID,
			ExternalID:  id,
			Number:      number,
			DocType:     docType,
			Title:       title,
			DetailURL:   link,
			PublishedAt: pub,
		})
	}
	return docs, nil
}

func canonicalDetailURL(link, guid string) string {
	raw := strings.TrimSpace(link)
	if raw == "" {
		raw = strings.TrimSpace(guid)
	}
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "//") {
		return "https:" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if u.IsAbs() {
		return raw
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return raw
	}
	return base.ResolveReference(u).String()
}

// parseSlug returns the trailing numeric id and the slug stem (the part before
// "-{id}.htm") from a detail URL.
func parseSlug(detailURL string) (id, slug string) {
	if i := strings.IndexAny(detailURL, "?#"); i >= 0 {
		detailURL = detailURL[:i]
	}
	detailURL = strings.TrimRight(detailURL, "/")
	// Reduce to the last path element.
	last := detailURL
	if i := strings.LastIndex(last, "/"); i >= 0 {
		last = last[i+1:]
	}
	m := slugIDRe.FindStringSubmatch(last)
	if m == nil {
		// No trailing id; return the bare stem without ".htm".
		return "", strings.TrimSuffix(last, ".htm")
	}
	id = m[1]
	stem := strings.TrimSuffix(last, m[0]) // drop "-{id}.htm"
	return id, stem
}

// docTypePrefixes maps the leading slug token(s) to a Vietnamese doc type. The
// slug is the type name followed by "-so-" and the số ký hiệu, e.g.
// "quyet-dinh-so-840-qd-ttg", "van-ban-hop-nhat-so-43-vbhn-nhnn".
var docTypePrefixes = []struct {
	prefix  string
	docType ingest.DocType
}{
	{"van-ban-hop-nhat", "Văn bản hợp nhất"},
	{"nghi-quyet", "Nghị quyết"},
	{"nghi-dinh", "Nghị định"},
	{"quyet-dinh", "Quyết định"},
	{"thong-tu-lien-tich", "Thông tư liên tịch"},
	{"thong-tu", "Thông tư"},
	{"chi-thi", "Chỉ thị"},
	{"cong-van", "Công văn"},
	{"cong-dien", "Công điện"},
	{"phap-lenh", "Pháp lệnh"},
	{"sac-lenh", "Sắc lệnh"},
	{"lenh", "Lệnh"},
	{"luat", "Luật"},
	{"hien-phap", "Hiến pháp"},
	{"thong-bao", "Thông báo"},
	{"ke-hoach", "Kế hoạch"},
	{"quy-dinh", "Quy định"},
	{"quy-che", "Quy chế"},
}

// parseNumberAndType derives the số ký hiệu and loại văn bản from a slug stem.
// The số ký hiệu lives after the "-so-" marker; "/" separators in the original
// number are rendered as "-" in the slug, so we recover them from the known
// pattern {seq}/{year}/{code} or {seq}/{code}.
func parseNumberAndType(slug string) (number string, docType ingest.DocType) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if slug == "" {
		return "", ""
	}

	for _, p := range docTypePrefixes {
		if slug == p.prefix || strings.HasPrefix(slug, p.prefix+"-") {
			docType = p.docType
			break
		}
	}

	rest := slug
	if i := strings.Index(slug, "-so-"); i >= 0 {
		rest = slug[i+len("-so-"):]
	} else if docType != "" {
		// No explicit "-so-" marker: the number follows the type prefix.
		for _, p := range docTypePrefixes {
			if p.docType == docType {
				rest = strings.TrimPrefix(slug, p.prefix+"-")
				break
			}
		}
	}

	number = numberFromSlugTail(rest)
	return number, docType
}

// numberFromSlugTail reconstructs a số ký hiệu from the hyphenated tail of a
// slug. Vietnamese numbers look like "840/QĐ-TTg", "148/2026/NĐ-CP", or
// "43/VBHN-NHNN": a sequence number, an optional 4-digit year, then a
// dash-joined type/agency code. In the slug everything is hyphen-joined and
// lowercased, e.g. "840-qd-ttg", "148-2026-nd-cp", "43-vbhn-nhnn".
func numberFromSlugTail(tail string) string {
	tail = strings.Trim(tail, "-")
	if tail == "" {
		return ""
	}
	parts := strings.Split(tail, "-")
	if len(parts) == 0 {
		return ""
	}

	upper := make([]string, len(parts))
	for i, p := range parts {
		upper[i] = upperCode(p)
	}

	seq := upper[0]
	idx := 1
	year := ""
	if idx < len(upper) && isYear(parts[idx]) {
		year = upper[idx]
		idx++
	}
	code := strings.Join(upper[idx:], "-")

	switch {
	case year != "" && code != "":
		return seq + "/" + year + "/" + code
	case code != "":
		return seq + "/" + code
	case year != "":
		return seq + "/" + year
	default:
		return seq
	}
}

// upperCode upper-cases a slug token and restores the Vietnamese diacritics in
// the common agency/type codes that the slug strips (đ, Đ in QĐ, NĐ, VBHN...).
func upperCode(tok string) string {
	switch tok {
	case "qd":
		return "QĐ"
	case "nd":
		return "NĐ"
	case "ttlt":
		return "TTLT"
	default:
		return strings.ToUpper(tok)
	}
}

func isYear(tok string) bool {
	if len(tok) != 4 {
		return false
	}
	for _, r := range tok {
		if r < '0' || r > '9' {
			return false
		}
	}
	return tok >= "1900" && tok <= "2099"
}

// rssDateLayouts covers the RFC1123-style stamps congbao emits (GMT, sometimes
// without a weekday).
var rssDateLayouts = []string{
	time.RFC1123Z,
	time.RFC1123,
	"Mon, 2 Jan 2006 15:04:05 GMT",
	"Mon, 02 Jan 2006 15:04:05 GMT",
	"2 Jan 2006 15:04:05 GMT",
	"02 Jan 2006 15:04:05 -0700",
}

func parseRSSDate(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range rssDateLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
