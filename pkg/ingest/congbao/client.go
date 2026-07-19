// Package congbao crawls the Vietnamese Official Gazette
// (congbao.chinhphu.vn, Văn phòng Chính phủ). Discovery uses the newest-first
// RSS feed; detail pages are server-rendered HTML carrying the metadata table
// and the CDN download links for born-digital PDF/DOCX. Standard library only.
package congbao

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"time"
)

// SourceID is the stable identifier for this source.
const SourceID = "congbao"

// Base hosts. The gazette site itself serves RSS + detail HTML; files live on
// the CDN, which expects a congbao Referer.
const (
	baseURL    = "https://congbao.chinhphu.vn"
	refererURL = "https://congbao.chinhphu.vn/"
)

// User agents: a descriptive crawler UA for the gazette host (per crawler
// etiquette), and a browser UA for the detail page and CDN, which gate on it.
const (
	crawlerUA = "banhmi/0.1 (+https://github.com/dannyota/banhmi)"
	browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
)

// Retry policy: bounded retries with exponential backoff on 429/5xx. Fetch
// concurrency is controlled by Temporal worker/activity limits.
const (
	maxRetries  = 3
	baseBackoff = time.Second
)

// Source is a congbao crawler. The zero value is not usable; call New.
type Source struct {
	http *http.Client
	log  *slog.Logger
}

// New returns a congbao Source. A nil client uses a sane default whose TLS
// verification is augmented to complete the g7 CDN's incomplete certificate chain
// (see tls.go); a nil logger discards logs.
func New(client *http.Client, logger *slog.Logger) *Source {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if client == nil {
		client = defaultHTTPClient(logger)
	}
	return &Source{http: client, log: logger}
}

// ID implements ingest.Source.
func (s *Source) ID() string { return SourceID }

// get performs a GET with the given User-Agent and optional extra headers,
// retrying on 429/5xx with exponential backoff. The caller owns the returned
// body. A 2xx response that is not retried is returned as-is.
func (s *Source) get(ctx context.Context, url, userAgent string, headers map[string]string) (*http.Response, error) {
	return s.do(ctx, http.MethodGet, url, nil, userAgent, headers)
}

// postJSON performs a JSON POST with the same retry/backoff policy as get. The
// caller owns the returned body.
func (s *Source) postJSON(ctx context.Context, url string, payload []byte, headers map[string]string) (*http.Response, error) {
	return s.do(ctx, http.MethodPost, url, payload, browserUA, headers)
}

func (s *Source) do(ctx context.Context, method, url string, payload []byte, userAgent string, headers map[string]string) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(float64(baseBackoff) * math.Pow(2, float64(attempt-1)))
			s.log.Debug("congbao retry", "url", url, "attempt", attempt, "backoff", backoff)
			t := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				t.Stop()
				return nil, ctx.Err()
			case <-t.C:
			}
		}

		var body io.Reader
		if payload != nil {
			body = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, body)
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Accept-Language", "vi-VN,vi;q=0.9,en;q=0.8")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := s.http.Do(req)
		if err != nil {
			// Transport-level error (DNS, TLS, connection). Surface it to the
			// caller without retrying — in this sandbox the CDN fails TLS, and
			// retrying would not help.
			lastErr = fmt.Errorf("http %s %s: %w", method, url, err)
			return nil, lastErr
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("http %s %s: status %d", method, url, resp.StatusCode)
			drainClose(resp.Body)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			drainClose(resp.Body)
			return nil, fmt.Errorf("http %s %s: status %d", method, url, resp.StatusCode)
		}
		return resp, nil
	}
	if lastErr == nil {
		lastErr = errors.New("exhausted retries")
	}
	return nil, lastErr
}

func drainClose(rc io.ReadCloser) {
	_, _ = io.Copy(io.Discard, rc)
	_ = rc.Close()
}
