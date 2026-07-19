// Package serc crawls the Securities and Exchange Regulator of Cambodia portal
// (serc.gov.kh) for securities regulations, Prakas, Anukrets, and guidelines.
// SERC publishes English-language PDFs organized by board sections. Cloudflare
// protected — uses browser UA. English corpus.
package serc

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
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

// SourceID is the stable identifier for this source.
const SourceID = "serc"

const (
	defaultBaseURL = "https://www.serc.gov.kh"
	userAgent      = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36"
)

const (
	maxRetries  = 3
	baseBackoff = time.Second
	pacePage    = 500 * time.Millisecond
)

// boardSection defines one SERC English board listing to crawl.
type boardSection struct {
	ID      string // board ID parameter (e.g. "m21laws")
	DocType string // document type label
}

// englishBoards are the English-language board sections to crawl.
var englishBoards = []boardSection{
	{ID: "m21laws", DocType: "Law"},
	{ID: "m22anukrets", DocType: "Anukret"},
	{ID: "m23prakas", DocType: "Prakas"},
	{ID: "m24orderguide", DocType: "Order/Guideline"},
	{ID: "m25others", DocType: "Other"},
}

// Source is a serc.gov.kh crawler. The zero value is not usable; call New.
type Source struct {
	http    *http.Client
	log     *slog.Logger
	baseURL string
}

// New returns a SERC source. A nil client uses a 60s timeout; a nil logger discards.
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

// get fetches a URL with bounded retries on 429/5xx and returns the body.
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
		body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
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

// Download streams a SERC PDF into w while computing its SHA-256.
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
		req.Header.Set("Referer", s.baseURL+"/")
		resp, err := s.http.Do(req)
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
