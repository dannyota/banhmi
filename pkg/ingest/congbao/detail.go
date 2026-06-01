package congbao

import (
	"context"
	"fmt"
	"html"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

// The detail page is server-rendered HTML. The metadata table is a sequence of
//
//	<div class="row"><span class="name">LABEL</span>
//	   <div class="value"><span class="child-value">VALUE</span></div></div>
//
// (the Công báo row uses an <a class="child-value"> instead). Download links
// sit in <div class="list--open"> as anchors carrying data-file with the file
// name and href to the CDN stream. We parse with targeted regexps rather than a
// DOM library to stay within the standard library.

var (
	// rowRe captures one metadata row: the label and the inner HTML of its value.
	rowRe = regexp.MustCompile(`(?s)<div class="row"[^>]*>\s*<span class="name">(.*?)</span>.*?<div class="value">(.*?)</div>\s*</div>`)

	// childValueRe pulls the text out of the value cell, whether it is wrapped
	// in <span class="child-value"> or <a ... class="child-value">.
	childValueRe = regexp.MustCompile(`(?s)class="child-value"[^>]*>(.*?)</`)

	// downloadRe captures each CDN download anchor: href and the data-file name.
	downloadRe = regexp.MustCompile(`(?s)<a\s+href="(https://g7\.cdnchinhphu\.vn/api/download/stream\?[^"]+)"[^>]*?data-file="([^"]*)"`)

	// tagRe strips residual HTML tags from a value cell.
	tagRe = regexp.MustCompile(`<[^>]+>`)
)

// FetchDetail fetches and parses a document detail page. It returns the parsed
// metadata and the PDF/DOCX download references. The server-rendered HTML is
// the source of truth here; congbao does not serve the body inline.
func (s *Source) FetchDetail(ctx context.Context, ref ingest.DetailRef) (*ingest.DiscoveredDoc, error) {
	detailURL := strings.TrimSpace(ref.DetailURL)
	if detailURL == "" {
		return nil, fmt.Errorf("fetch detail: empty detail URL")
	}
	if !strings.HasPrefix(detailURL, "http") {
		detailURL = baseURL + "/" + strings.TrimPrefix(detailURL, "/")
	}
	resp, err := s.get(ctx, detailURL, browserUA, map[string]string{
		"Referer": refererURL,
	})
	if err != nil {
		return nil, fmt.Errorf("fetch detail: %w", err)
	}
	defer drainClose(resp.Body)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read detail: %w", err)
	}
	htmlText := string(body)

	id, slug := parseSlug(detailURL)
	number, docType := parseNumberAndType(slug)
	doc := &ingest.DiscoveredDoc{
		SourceID:   SourceID,
		ExternalID: id,
		Number:     number,
		DocType:    docType,
		DetailURL:  detailURL,
	}

	parseMetadataTable(htmlText, doc)
	doc.Files = parseDownloadLinks(htmlText)
	return doc, nil
}

// parseMetadataTable fills doc from the detail page's metadata rows. Values
// that the table provides override the slug-derived fields.
func parseMetadataTable(htmlText string, doc *ingest.DiscoveredDoc) {
	for _, m := range rowRe.FindAllStringSubmatch(htmlText, -1) {
		label := normalizeLabel(cleanText(m[1]))
		value := extractValue(m[2])
		if value == "" {
			continue
		}
		switch label {
		case "loai van ban":
			doc.DocType = ingest.DocType(value)
		case "so ky hieu":
			doc.Number = value
		case "co quan ban hanh":
			doc.Issuer = value
		case "trich yeu":
			doc.Title = value
		case "nguoi ky":
			doc.Signer = value
		case "ngay ban hanh":
			if t := parseVNDate(value); !t.IsZero() {
				doc.IssuedAt = t
			}
		case "ngay hieu luc":
			if t := parseVNDate(value); !t.IsZero() {
				doc.EffectiveAt = t
			}
		case "cong bao":
			doc.GazetteNumber = value
		}
	}
}

// extractValue pulls the human text from a value cell, preferring the
// child-value span/anchor and falling back to stripping tags.
func extractValue(cell string) string {
	if m := childValueRe.FindStringSubmatch(cell); m != nil {
		if v := cleanText(m[1]); v != "" {
			return v
		}
	}
	return cleanText(cell)
}

// parseDownloadLinks returns the PDF/DOCX file references from the detail HTML,
// de-duplicated by URL. The href is HTML-unescaped (&amp; -> &) so it is a
// usable request URL; the extension comes from data-file.
func parseDownloadLinks(htmlText string) []ingest.FileRef {
	var refs []ingest.FileRef
	seen := make(map[string]bool)
	for _, m := range downloadRe.FindAllStringSubmatch(htmlText, -1) {
		rawURL := html.UnescapeString(m[1])
		if seen[rawURL] {
			continue
		}
		seen[rawURL] = true

		dataFile := html.UnescapeString(m[2])
		name := dataFile
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		ext := ""
		if i := strings.LastIndex(dataFile, "."); i >= 0 {
			ext = strings.ToLower(dataFile[i+1:])
		}
		refs = append(refs, ingest.FileRef{
			URL:      rawURL,
			Name:     name,
			Ext:      ext,
			Kind:     "main",
			MIMEType: mimeForExt(ext),
		})
	}
	return refs
}

func mimeForExt(ext string) string {
	switch ext {
	case "pdf":
		return "application/pdf"
	case "docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case "doc":
		return "application/msword"
	default:
		return ""
	}
}

// cleanText unescapes HTML entities and collapses whitespace.
func cleanText(s string) string {
	s = tagRe.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	return collapseWS(s)
}

func collapseWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// normalizeLabel folds a Vietnamese metadata label to a diacritic-free,
// punctuation-free, lowercase key for matching (e.g. "Số, ký hiệu" ->
// "so ky hieu").
func normalizeLabel(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		b.WriteRune(foldDiacritic(r))
	}
	out := b.String()
	out = strings.ReplaceAll(out, ",", " ")
	return collapseWS(out)
}

// foldDiacritic maps a Vietnamese vowel/đ with diacritics to its ASCII base.
func foldDiacritic(r rune) rune {
	switch r {
	case 'à', 'á', 'ả', 'ã', 'ạ', 'ă', 'ằ', 'ắ', 'ẳ', 'ẵ', 'ặ', 'â', 'ầ', 'ấ', 'ẩ', 'ẫ', 'ậ':
		return 'a'
	case 'è', 'é', 'ẻ', 'ẽ', 'ẹ', 'ê', 'ề', 'ế', 'ể', 'ễ', 'ệ':
		return 'e'
	case 'ì', 'í', 'ỉ', 'ĩ', 'ị':
		return 'i'
	case 'ò', 'ó', 'ỏ', 'õ', 'ọ', 'ô', 'ồ', 'ố', 'ổ', 'ỗ', 'ộ', 'ơ', 'ờ', 'ớ', 'ở', 'ỡ', 'ợ':
		return 'o'
	case 'ù', 'ú', 'ủ', 'ũ', 'ụ', 'ư', 'ừ', 'ứ', 'ử', 'ữ', 'ự':
		return 'u'
	case 'ỳ', 'ý', 'ỷ', 'ỹ', 'ỵ':
		return 'y'
	case 'đ':
		return 'd'
	default:
		return r
	}
}

// vnDateRe matches dd/mm/yyyy as congbao renders dates.
var vnDateRe = regexp.MustCompile(`(\d{1,2})/(\d{1,2})/(\d{4})`)

func parseVNDate(s string) time.Time {
	m := vnDateRe.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}
	}
	day, _ := strconv.Atoi(m[1])
	mon, _ := strconv.Atoi(m[2])
	year, _ := strconv.Atoi(m[3])
	if mon < 1 || mon > 12 || day < 1 || day > 31 {
		return time.Time{}
	}
	return time.Date(year, time.Month(mon), day, 0, 0, 0, 0, time.UTC)
}
