// Package vbpl crawls the Vietnamese national legal database (vbpl.vn, Bộ Tư
// pháp). It is banhmi's discovery engine and enrichment source: discovery runs two
// doc/all modes — a keyword-less sweep of the State Bank agency feed (scope-filtered
// on title + docAbs) and per-keyword title searches across the cross-cutting central
// issuers (the keyword is the filter) — see Discover and SOURCES.md. Per document vbpl
// supplies authoritative text files, the Điều/Khoản provision tree, the relation
// graph, and validity status. Standard library only. See docs/design/SOURCES.md.
package vbpl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"time"
)

// SourceID is the stable identifier for this source.
const SourceID = "vbpl"

const (
	apiBase   = "https://vbpl-bientap-gateway.moj.gov.vn/api/qtdc/public"
	docAllURL = apiBase + "/doc/all"
	originURL = "https://vbpl.vn"
	userAgent = "banhmi/0.1 (+https://github.com/dannyota/banhmi)"
)

// Retry policy: bounded retries with capped exponential backoff on 429/5xx.
// Fetch concurrency is controlled by Temporal worker/activity limits.
const (
	maxRetries  = 5
	baseBackoff = time.Second
	maxBackoff  = 30 * time.Second
)

// backoffFor returns the capped exponential backoff before retry attempt n
// (n >= 1): baseBackoff * 2^(n-1), clamped to maxBackoff.
func backoffFor(attempt int) time.Duration {
	d := time.Duration(float64(baseBackoff) * math.Pow(2, float64(attempt-1)))
	if d > maxBackoff {
		return maxBackoff
	}
	return d
}

// Source is a vbpl client. The zero value is not usable; call New.
type Source struct {
	http            *http.Client
	log             *slog.Logger
	sbvAgencyIDs    []string // State Bank ids (is_sbv): the keyword-less agency sweep
	nonSbvAgencyIDs []string // cross-cutting central issuers (in_scope, not is_sbv): keyword search
	// relationTypes maps a vbpl referenceType code to a relation_type label, from
	// config.relation_type. Codes with no entry decode to a neutral
	// "vbpl_type_<code>" label. nil falls back entirely to the built-in defaults.
	relationTypes map[int]string
}

// New returns a vbpl Source with two agency sets loaded from config.issuer_code
// (source='vbpl'): sbvAgencyIDs (is_sbv — 62 current + 908 legacy "Ngân hàng quốc
// gia"; 62 alone misses ~12 predecessor docs) drives the keyword-less sweep, and
// nonSbvAgencyIDs (in_scope, not is_sbv — Quốc hội, Chính phủ, Bộ Công an…) is the
// target of the keyword searches. See Discover and SOURCES.md. A nil client uses a
// sane default; a nil logger discards logs.
func New(client *http.Client, logger *slog.Logger, sbvAgencyIDs, nonSbvAgencyIDs []string, relationTypes map[int]string) *Source {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Source{http: client, log: logger, sbvAgencyIDs: sbvAgencyIDs, nonSbvAgencyIDs: nonSbvAgencyIDs, relationTypes: relationTypes}
}

// ID implements ingest.Source.
func (s *Source) ID() string { return SourceID }

// postJSON performs a JSON POST with retries on 429/5xx and decodes the response
// into out.
func (s *Source) postJSON(ctx context.Context, url string, body, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			t := time.NewTimer(backoffFor(attempt))
			select {
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			case <-t.C:
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", originURL)
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Accept", "application/json")

		resp, err := s.http.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("post %s: %w", url, err)
			return lastErr // transport error; retrying won't help in a restricted network
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("post %s: status %d", url, resp.StatusCode)
			drainClose(resp.Body)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			drainClose(resp.Body)
			return fmt.Errorf("post %s: status %d", url, resp.StatusCode)
		}
		err = json.NewDecoder(resp.Body).Decode(out)
		drainClose(resp.Body)
		if err != nil {
			return fmt.Errorf("decode %s: %w", url, err)
		}
		return nil
	}
	if lastErr == nil {
		lastErr = errors.New("exhausted retries")
	}
	return lastErr
}

// getJSON performs a JSON GET with retries on 429/5xx and decodes the response
// into out. It carries the same vbpl headers as postJSON so the gateway accepts
// the request (Origin/Referer vbpl.vn).
func (s *Source) getJSON(ctx context.Context, url string, out any) error {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			t := time.NewTimer(backoffFor(attempt))
			select {
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			case <-t.C:
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Origin", originURL)
		req.Header.Set("Referer", originURL+"/")
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Accept", "application/json")

		resp, err := s.http.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("get %s: %w", url, err)
			return lastErr // transport error; retrying won't help in a restricted network
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("get %s: status %d", url, resp.StatusCode)
			drainClose(resp.Body)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			drainClose(resp.Body)
			return fmt.Errorf("get %s: status %d", url, resp.StatusCode)
		}
		err = json.NewDecoder(resp.Body).Decode(out)
		drainClose(resp.Body)
		if err != nil {
			return fmt.Errorf("decode %s: %w", url, err)
		}
		return nil
	}
	if lastErr == nil {
		lastErr = errors.New("exhausted retries")
	}
	return lastErr
}

func drainClose(rc io.ReadCloser) {
	_, _ = io.Copy(io.Discard, rc)
	_ = rc.Close()
}
