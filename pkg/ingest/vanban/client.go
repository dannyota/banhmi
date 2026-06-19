// Package vanban crawls the Vietnamese Government legal-document database
// (vanban.chinhphu.vn, "Hệ thống văn bản", operated by Văn phòng Chính phủ). It is
// banhmi's source #2 after vbpl: the freshest, broadest central VBQPPL feed, which
// carries brand-new central laws before vbpl's MOJ database indexes them (e.g. the
// 2025 AI Law). Role: discovery + authoritative born-digital file + core metadata —
// NOT structure/relations (vbpl stays authoritative for those).
//
// The site is ASP.NET WebForms: the document list paginates by __doPostBack, and
// there is no RSS or JSON API. Discovery walks the newest-first list via the
// GridView Page$N postback (verified reproducible from a plain HTTP client) and
// lets the pipeline's scope.Match filter topically — exactly like the congbao RSS
// feed. See docs/design/SOURCES.md. Standard library only.
package vanban

import (
	"context"
	"errors"
	"fmt"
	stdhtml "html"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"

	"danny.vn/banhmi/pkg/ingest"
)

// SourceID is the stable identifier for this source.
const SourceID = "vanban"

const (
	defaultBaseURL = "https://vanban.chinhphu.vn"
	// listPath is the central VBQPPL document list. classid=1 selects văn bản quy
	// phạm pháp luật (normative law), mode=1 the newest-first list view.
	listPath  = "/he-thong-van-ban?classid=1&mode=1"
	userAgent = "banhmi/0.1 (+https://github.com/dannyota/banhmi)"
)

const (
	maxRetries  = 3
	baseBackoff = time.Second
	// pacePostback is the polite delay between page postbacks during a walk.
	pacePostback = 300 * time.Millisecond
)

// Source is a vanban.chinhphu.vn crawler. The zero value is not usable; call New.
type Source struct {
	http    *http.Client
	log     *slog.Logger
	baseURL string
}

// New returns a vanban source. A nil client uses a 60s timeout; a nil logger
// discards logs.
func New(client *http.Client, logger *slog.Logger) *Source {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Source{http: client, log: logger, baseURL: defaultBaseURL}
}

// ID implements ingest.Source.
func (s *Source) ID() string { return SourceID }

// do performs an HTTP request (GET when body is nil, else a form POST) with bounded
// retries on 429/5xx and returns the decoded UTF-8 body. The Go transport adds and
// transparently decodes gzip, so no manual Accept-Encoding handling is needed.
func (s *Source) do(ctx context.Context, rawURL string, form url.Values) (string, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(float64(baseBackoff) * math.Pow(2, float64(attempt-1)))
			s.log.Debug("vanban retry", "url", rawURL, "attempt", attempt, "backoff", backoff)
			if err := sleep(ctx, backoff); err != nil {
				return "", err
			}
		}
		var (
			req *http.Request
			err error
		)
		if form == nil {
			req, err = http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		} else {
			req, err = http.NewRequestWithContext(ctx, http.MethodPost, rawURL, strings.NewReader(form.Encode()))
		}
		if err != nil {
			return "", fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		req.Header.Set("Referer", s.baseURL+listPath)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Origin", s.baseURL)
		}

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
		body, err := io.ReadAll(resp.Body)
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

func drainClose(r io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(r, 512))
	_ = r.Close()
}

var (
	tagRe      = regexp.MustCompile(`(?s)<[^>]+>`)
	spaceRe    = regexp.MustCompile(`\s+`)
	hiddenRe   = regexp.MustCompile(`(?is)<input[^>]*type="hidden"[^>]*>`)
	attrNameRe = regexp.MustCompile(`(?i)\bname="([^"]*)"`)
	attrValRe  = regexp.MustCompile(`(?i)\bvalue="([^"]*)"`)
	docidRe    = regexp.MustCompile(`docid=(\d+)`)
	// pagerTargetRe extracts the GridView postback target and the page numbers it
	// offers, e.g. __doPostBack('ctrl_191017_163$grvDocument','Page$2'). The served
	// HTML encodes the quotes as &#39;, so accept both that and a literal quote (the
	// argument's "$" is rendered raw). The control id is read from the page, not
	// hardcoded, so a re-skinned grid still paginates.
	pagerTargetRe = regexp.MustCompile(`__doPostBack\((?:&#39;|')([^&']*grvDocument)(?:&#39;|'),(?:&#39;|')Page\$(\d+)`)
	// list-row field spans.
	codeSpanRe      = regexp.MustCompile(`(?is)<span class="code">(.*?)</span>`)
	substractSpanRe = regexp.MustCompile(`(?is)<span class="substract">(.*?)</span>`)
	issuedDateRe    = regexp.MustCompile(`(?is)<span class="issued-date[^"]*">(.*?)</span>`)
	// seqPrefixRe strips a leading list-sequence prefix ("66.") that the grid renders
	// before the số ký hiệu on some rows, but only when a full số ký hiệu follows.
	seqPrefixRe = regexp.MustCompile(`^\d+\.(\d+/\d{4}/)`)
	// hrefAttrRe extracts href targets from anchors (detail-page file links).
	hrefAttrRe = regexp.MustCompile(`(?i)href="([^"]+)"`)
	// labelMarkRe drops combining marks when normalizing a metadata label.
	labelMarkRe = regexp.MustCompile(`[\p{Mn}]`)
)

func cleanText(s string) string {
	s = stdhtml.UnescapeString(s)
	s = tagRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
}

// normalizeLabel folds a metadata label to ASCII-ish lowercase (diacritics dropped,
// đ→d) so detail-table rows can be matched by a stable key, e.g. "Số ký hiệu" →
// "so ky hieu".
func normalizeLabel(s string) string {
	s = norm.NFD.String(strings.ToLower(strings.TrimSpace(s)))
	s = strings.Map(func(r rune) rune {
		if r == 'đ' {
			return 'd'
		}
		return r
	}, s)
	return strings.TrimSpace(spaceRe.ReplaceAllString(labelMarkRe.ReplaceAllString(s, ""), " "))
}

// splitCells returns each <td>…</td> block in htmlText.
func splitCells(htmlText string) []string {
	var out []string
	for {
		start := strings.Index(htmlText, "<td")
		if start < 0 {
			return out
		}
		htmlText = htmlText[start:]
		end := strings.Index(htmlText, "</td>")
		if end < 0 {
			return out
		}
		end += len("</td>")
		out = append(out, htmlText[:end])
		htmlText = htmlText[end:]
	}
}

// parseHidden returns every ASP.NET hidden form field on the page (__VIEWSTATE,
// __EVENTVALIDATION, __VIEWSTATEGENERATOR, and the menu/site constants). These are
// round-tripped verbatim into the next postback.
func parseHidden(htmlText string) url.Values {
	v := url.Values{}
	for _, tag := range hiddenRe.FindAllString(htmlText, -1) {
		name := attrNameRe.FindStringSubmatch(tag)
		if name == nil {
			continue
		}
		val := ""
		if m := attrValRe.FindStringSubmatch(tag); m != nil {
			val = stdhtml.UnescapeString(m[1])
		}
		v.Set(name[1], val)
	}
	return v
}

// cleanDocNumber strips a leading grid-sequence prefix from a list số ký hiệu.
func cleanDocNumber(s string) string {
	s = cleanText(s)
	if m := seqPrefixRe.FindStringSubmatch(s); m != nil {
		s = strings.TrimPrefix(s, m[0][:strings.Index(m[0], m[1])])
	}
	return s
}

func canonicalDetailURL(baseURL, docID string) string {
	return strings.TrimRight(baseURL, "/") + "/?pageid=27160&docid=" + docID + "&classid=1"
}

func parseVNDate(s string) time.Time {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"02/01/2006", "02-01-2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// docTypeFromNumber infers loại văn bản from the số ký hiệu / title, used until the
// detail page supplies the authoritative type.
func docTypeFromNumber(number, title string) ingest.DocType {
	n := strings.ToUpper(strings.TrimSpace(number))
	t := strings.ToLower(strings.TrimSpace(title))
	switch {
	case strings.Contains(n, "/QH") || strings.Contains(t, "luật"):
		return "Luật"
	case strings.Contains(n, "/TT-") || strings.Contains(t, "thông tư"):
		return "Thông tư"
	case strings.Contains(n, "/NĐ-") || strings.Contains(n, "/ND-") || strings.Contains(t, "nghị định"):
		return "Nghị định"
	case strings.Contains(n, "/NQ-") || strings.Contains(t, "nghị quyết"):
		return "Nghị quyết"
	case strings.Contains(n, "/QĐ-") || strings.Contains(n, "/QD-") || strings.Contains(t, "quyết định"):
		return "Quyết định"
	case strings.Contains(n, "/PL-") || strings.Contains(t, "pháp lệnh"):
		return "Pháp lệnh"
	default:
		return ""
	}
}
