// Package odc crawls Open Development Cambodia (data.opendevelopmentcambodia.net)
// via the CKAN API for banking and financial regulation documents. ODC is an open
// data portal that hosts government PDFs with rich metadata. No WAF, no geo-blocking.
// English corpus.
package odc

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
const SourceID = "odc"

const (
	defaultBaseURL = "https://data.opendevelopmentcambodia.net"
	userAgent      = "banhmi/0.1 (+https://github.com/dannyota/banhmi)"
)

const (
	maxRetries  = 3
	baseBackoff = time.Second
)

// Source is an ODC CKAN crawler. The zero value is not usable; call New.
type Source struct {
	http    *http.Client
	log     *slog.Logger
	baseURL string
}

// New returns an ODC source. A nil client uses a 60s timeout; a nil logger discards.
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

// get fetches a URL with bounded retries on 429/5xx.
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
		req.Header.Set("Accept", "application/json")
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
		body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
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

// Download streams an ODC resource file into w while computing its SHA-256.
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
