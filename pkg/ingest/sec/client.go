// Package sec crawls Thailand's Securities and Exchange Commission
// (capital.sec.or.th) NRS portal for digital asset and IT system regulations.
// Discovery is accessible worldwide; PDF downloads from publish.sec.or.th are
// geo-blocked (F5 BIG-IP) and require BANHMI_SEC_PROXY_URL.
// Thai corpus. See docs/design/jurisdictions/THAILAND.md.
package sec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"time"

	"danny.vn/banhmi/pkg/fetch"
	"danny.vn/banhmi/pkg/ingest"
)

// SourceID is the stable identifier for this source.
const SourceID = "sec"

const (
	discoveryBaseURL = "https://capital.sec.or.th"
	publishBaseURL   = "https://publish.sec.or.th"
	userAgent        = "banhmi/0.1 (+https://github.com/dannyota/banhmi)"

	nrsSearchPath = "/webapp/nrs/nrs_main_search.php"
)

const (
	maxRetries  = 3
	baseBackoff = time.Second
	paceCat     = 500 * time.Millisecond
)

// Config holds optional SEC source settings.
type Config struct {
	// ProxyURL is an HTTP/SOCKS5 proxy for publish.sec.or.th downloads
	// (bypasses F5 BIG-IP geo-blocking). Discovery (capital.sec.or.th) is
	// not proxied. Same pattern as BANHMI_OJK_PROXY_URL.
	ProxyURL string
}

// Source is a capital.sec.or.th NRS crawler. The zero value is not usable; call New.
type Source struct {
	discovery *http.Client // for capital.sec.or.th (no proxy needed)
	download  *http.Client // for publish.sec.or.th (proxy when configured)
	log       *slog.Logger
}

// New returns an SEC source. A nil client uses a 60s-timeout default.
// When cfg.ProxyURL is set, downloads route through the proxy; discovery
// always uses a direct connection.
func New(cfg *Config, client *http.Client, logger *slog.Logger) *Source {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	dlClient := client
	if cfg != nil && cfg.ProxyURL != "" {
		proxyURL, err := url.Parse(cfg.ProxyURL)
		if err == nil {
			// Chrome TLS fingerprint over CONNECT (the OJK pattern): publish.sec.or.th
			// sits behind F5 BIG-IP, which 403s Go's native TLS even from a Thai IP —
			// the block fingerprints the client, not just the geography.
			dlClient = &http.Client{
				Transport: fetch.ProxiedChromeTransport(proxyURL),
				Timeout:   120 * time.Second,
			}
		} else {
			logger.Warn("sec: invalid proxy URL, downloads will 403", "url", cfg.ProxyURL, "err", err)
		}
	}

	return &Source{discovery: client, download: dlClient, log: logger}
}

// ID implements ingest.Source.
func (s *Source) ID() string { return SourceID }

// Download streams a file from publish.sec.or.th into w using the proxy client
// and returns the byte count and SHA-256 hex digest.
func (s *Source) Download(ctx context.Context, ref ingest.FileRef, w io.Writer) (int64, string, error) {
	if ref.URL == "" {
		return 0, "", fmt.Errorf("download: empty url")
	}
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			if err := sleep(ctx, time.Duration(float64(baseBackoff)*math.Pow(2, float64(attempt-1)))); err != nil {
				return 0, "", err
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ref.URL, nil)
		if err != nil {
			return 0, "", fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Referer", discoveryBaseURL+"/")
		resp, err := s.download.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("download %s: status %d", ref.Name, resp.StatusCode)
			drainClose(resp.Body)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			drainClose(resp.Body)
			return 0, "", fmt.Errorf("download %s: status %d", ref.Name, resp.StatusCode)
		}
		h := sha256.New()
		n, err := io.Copy(io.MultiWriter(w, h), resp.Body)
		drainClose(resp.Body)
		if err != nil {
			return n, "", fmt.Errorf("download %s: copy body: %w", ref.Name, err)
		}
		return n, hex.EncodeToString(h.Sum(nil)), nil
	}
	if lastErr == nil {
		lastErr = errors.New("exhausted retries")
	}
	return 0, "", lastErr
}

// get fetches a URL with bounded retries on 429/5xx. Uses the discovery client.
// Returns raw bytes (caller must handle encoding).
func (s *Source) get(ctx context.Context, rawURL string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			if err := sleep(ctx, time.Duration(float64(baseBackoff)*math.Pow(2, float64(attempt-1)))); err != nil {
				return nil, err
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
		resp, err := s.discovery.Do(req)
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
		body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		drainClose(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("read body: %w", err)
		}
		return body, nil
	}
	if lastErr == nil {
		lastErr = errors.New("exhausted retries")
	}
	return nil, lastErr
}

// post sends a form POST to the discovery endpoint. Returns raw bytes.
func (s *Source) post(ctx context.Context, rawURL string, body io.Reader) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			if err := sleep(ctx, time.Duration(float64(baseBackoff)*math.Pow(2, float64(attempt-1)))); err != nil {
				return nil, err
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, body)
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
		resp, err := s.discovery.Do(req)
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
		data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		drainClose(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("read body: %w", err)
		}
		return data, nil
	}
	if lastErr == nil {
		lastErr = errors.New("exhausted retries")
	}
	return nil, lastErr
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
