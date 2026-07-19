// Package ocs crawls Thailand's Office of the Council of State (www.ocs.go.th)
// for consolidated Acts via the legacy listing JSON API. Discovery paginates
// all laws and filters by lawCode type (-1B- = Acts). Thai corpus.
//
// TLS note: www.ocs.go.th serves an incomplete certificate chain (missing
// intermediate), so the HTTP client uses InsecureSkipVerify. The full-text
// API host (searchlaw.ocs.go.th) has a valid chain and uses a standard client.
//
// See docs/design/jurisdictions/THAILAND.md.
package ocs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
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
const SourceID = "ocs"

const (
	defaultBaseURL     = "https://www.ocs.go.th"
	defaultTextBaseURL = "https://searchlaw.ocs.go.th"
	userAgent          = "banhmi/0.1 (+https://github.com/dannyota/banhmi)"
)

const (
	maxRetries  = 3
	baseBackoff = time.Second
)

// Source is an ocs.go.th crawler. The zero value is not usable; call New.
type Source struct {
	http        *http.Client
	log         *slog.Logger
	baseURL     string
	textBaseURL string
}

// New returns an OCS source. A nil client creates one with InsecureSkipVerify
// (required: www.ocs.go.th serves an incomplete certificate chain). A nil
// logger discards.
func New(client *http.Client, logger *slog.Logger) *Source {
	if client == nil {
		client = &http.Client{
			Timeout: 60 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // bad cert on www.ocs.go.th
			},
		}
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Source{
		http:        client,
		log:         logger,
		baseURL:     defaultBaseURL,
		textBaseURL: defaultTextBaseURL,
	}
}

// ID implements ingest.Source.
func (s *Source) ID() string { return SourceID }

// get fetches a URL with bounded retries on 429/5xx and returns the body bytes.
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
		req.Header.Set("Accept", "application/json,*/*;q=0.8")
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

// post sends a POST request with the given JSON body and returns the response
// body bytes. It retries on 429/5xx like get.
func (s *Source) post(ctx context.Context, rawURL string, body []byte) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			if err := sleep(ctx, time.Duration(float64(baseBackoff)*math.Pow(2, float64(attempt-1)))); err != nil {
				return nil, err
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Content-Type", "application/json")
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
			return nil, fmt.Errorf("status %d", resp.StatusCode)
		}
		respBody, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		drainClose(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("read body: %w", err)
		}
		return respBody, nil
	}
	if lastErr == nil {
		lastErr = errors.New("exhausted retries")
	}
	return nil, lastErr
}

// Download streams a file's bytes into w and returns the byte count and SHA-256
// hex digest.
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
