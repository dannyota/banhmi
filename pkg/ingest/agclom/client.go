// Package agclom crawls Malaysia's official federal-legislation database, the
// Attorney General's Chambers "Laws of Malaysia" portal (lom.agc.gov.my). It is
// the Malaysian analog of Vietnam's VBPL: the authoritative source for principal
// Acts (born-digital PDF) plus their validity dates and the P.U.(A)/(B)
// subsidiary-legislation timeline (relations). banhmi ingests the English (BI)
// text only, per the one-main-language-per-country policy (see CLAUDE.md).
//
// All access is plain HTTP — no headless browser. Discovery is a DataTables
// JSON endpoint (POST json-updated-2024.php); the per-Act detail page carries the
// validity dates and reprint PDF links in server-rendered HTML; relations come
// from POST json-subsid-2024.php?act=<id>. The full federal corpus (~885 Acts) is
// returned by Discover and scope-filtered topically downstream (pkg/scope), like
// the congbao RSS feed. Standard library only. See docs/design/MALAYSIA.md.
package agclom

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
	"strings"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

// SourceID is the stable identifier for this source.
const SourceID = "agclom"

const (
	defaultBaseURL = "https://lom.agc.gov.my"
	userAgent      = "banhmi/0.1 (+https://github.com/dannyota/banhmi)"
	// lang is the language edition banhmi ingests: BI = Bahasa Inggeris (English),
	// the one main language for the Malaysian corpus.
	lang = "BI"
)

const (
	maxRetries  = 3
	baseBackoff = time.Second
	pacePage    = 300 * time.Millisecond // polite delay between discovery pages
)

// Source is a lom.agc.gov.my crawler. The zero value is not usable; call New.
type Source struct {
	http    *http.Client
	log     *slog.Logger
	baseURL string
}

// New returns an agclom source. A nil client uses a 60s timeout; a nil logger
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

// do performs an HTTP request (GET when form is nil, else a form POST) with
// bounded retries on 429/5xx and returns the decoded UTF-8 body. The Go transport
// adds and transparently decodes gzip.
func (s *Source) do(ctx context.Context, rawURL string, form url.Values) (string, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(float64(baseBackoff) * math.Pow(2, float64(attempt-1)))
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
		req.Header.Set("Accept", "application/json, text/javascript, text/html;q=0.9, */*;q=0.8")
		req.Header.Set("Referer", s.baseURL+"/")
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
			req.Header.Set("X-Requested-With", "XMLHttpRequest")
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
		body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20)) // 32 MiB cap on text responses
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

// Download streams a LOM file (the /ilims born-digital PDF) into w while computing
// its SHA-256. Plain GET, no token required; bytes are streamed.
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

// pdfURL builds an absolute download URL from a LOM file path + name. path is the
// JSON "path" (e.g. "/upload/portal/akta/outputaktap/3552389_BI/"); the file lives
// under the /ilims app root. The name segment is path-escaped (names carry spaces
// and parentheses).
func pdfURL(baseURL, path, name string) string {
	return strings.TrimRight(baseURL, "/") + "/ilims" + path + url.PathEscape(name)
}

// detailURL is the per-Act detail page (validity dates + reprint PDF links).
func detailURL(baseURL, actID string) string {
	return fmt.Sprintf("%s/act-detail.php?act=%s&lang=%s", strings.TrimRight(baseURL, "/"), actID, lang)
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
