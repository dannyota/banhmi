// Package sbvhanoi crawls the State Bank of Vietnam Region 1 legal-document
// portal (sbv.hanoi.gov.vn). It supplements VBPL/Công báo with SBV-hosted
// decisions and attachments that are not always present in the national sources.
package sbvhanoi

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
const SourceID = "sbv_hanoi"

const (
	defaultBaseURL = "https://sbv.hanoi.gov.vn"
	listPath       = "/van-ban-quy-pham-phap-luat"
	userAgent      = "banhmi/0.1 (+https://github.com/dannyota/banhmi)"
)

const (
	maxRetries  = 3
	baseBackoff = time.Second
)

// Source is an SBV Hanoi legal-document crawler. The zero value is not usable;
// call New.
type Source struct {
	http    *http.Client
	log     *slog.Logger
	baseURL string
}

// New returns an SBV Hanoi source. A nil client uses a 60s timeout; a nil logger
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

func (s *Source) get(ctx context.Context, rawURL string) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(float64(baseBackoff) * math.Pow(2, float64(attempt-1)))
			s.log.Debug("sbv_hanoi retry", "url", rawURL, "attempt", attempt, "backoff", backoff)
			t := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				t.Stop()
				return nil, ctx.Err()
			case <-t.C:
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		req.Header.Set("Referer", s.baseURL+"/")

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
			return nil, fmt.Errorf("status %d", resp.StatusCode)
		}
		return resp, nil
	}
	if lastErr == nil {
		lastErr = errors.New("exhausted retries")
	}
	return nil, lastErr
}

func drainClose(r io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(r, 512))
	_ = r.Close()
}

var (
	tagRe         = regexp.MustCompile(`(?s)<[^>]+>`)
	spaceRe       = regexp.MustCompile(`\s+`)
	labelMarkRe   = regexp.MustCompile(`[\p{Mn}]`)
	detailIDRe    = regexp.MustCompile(`_4_WAR_portalvbpqportlet_(?:id|entryId)=([0-9]+)`)
	listPageCurRe = regexp.MustCompile(`_4_WAR_portalvbpqportlet_cur=([0-9]+)`)
)

func cleanText(s string) string {
	s = stdhtml.UnescapeString(s)
	s = tagRe.ReplaceAllString(s, " ")
	s = strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
	return s
}

func normalizeLabel(s string) string {
	s = norm.NFD.String(strings.ToLower(strings.TrimSpace(s)))
	s = strings.Map(func(r rune) rune {
		switch r {
		case 'đ':
			return 'd'
		case 'Đ':
			return 'd'
		default:
			return r
		}
	}, s)
	return labelMarkRe.ReplaceAllString(s, "")
}

func parseVNDate(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse("02/01/2006", s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func canonicalDetailURL(baseURL, id string) string {
	u, _ := url.Parse(strings.TrimRight(baseURL, "/") + listPath)
	q := u.Query()
	q.Set("p_p_id", "4_WAR_portalvbpqportlet")
	q.Set("p_p_lifecycle", "0")
	q.Set("p_p_state", "normal")
	q.Set("p_p_mode", "view")
	q.Set("p_p_col_id", "column-2")
	q.Set("p_p_col_pos", "1")
	q.Set("p_p_col_count", "2")
	q.Set("_4_WAR_portalvbpqportlet_id", id)
	q.Set("_4_WAR_portalvbpqportlet_mvcPath", "/html/portlet/list/view_detail.jsp")
	u.RawQuery = q.Encode()
	return u.String()
}

func docTypeFromNumber(number, title string) ingest.DocType {
	n := strings.ToUpper(strings.TrimSpace(number))
	t := strings.ToLower(strings.TrimSpace(title))
	switch {
	case strings.Contains(n, "/TT-") || strings.Contains(t, "thông tư"):
		return "Thông tư"
	case strings.Contains(n, "/QĐ-") || strings.Contains(n, "/QD-") || strings.Contains(t, "quyết định"):
		return "Quyết định"
	case strings.Contains(n, "/NĐ-") || strings.Contains(n, "/ND-") || strings.Contains(t, "nghị định"):
		return "Nghị định"
	case strings.Contains(n, "/NQ-") || strings.Contains(t, "nghị quyết"):
		return "Nghị quyết"
	case strings.Contains(t, "chỉ thị"):
		return "Chỉ thị"
	default:
		return ""
	}
}
