// Package bot crawls Bank of Thailand's FIPCS portal (app.bot.or.th/FIPCS) for
// regulatory notifications and circulars. ASP.NET WebForms with session-bound
// ViewState pagination. Thai corpus. See docs/design/jurisdictions/THAILAND.md.
package bot

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
	"net/http/cookiejar"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

// SourceID is the stable identifier for this source.
const SourceID = "bot"

const (
	defaultBaseURL    = "https://app.bot.or.th/FIPCS"
	defaultPDFBaseURL = "https://www.bot.or.th/content/dam/bot/fipcs/documents"
	userAgent         = "banhmi/0.1 (+https://github.com/dannyota/banhmi)"
)

const (
	maxRetries  = 3
	baseBackoff = time.Second
	pacePage    = 100 * time.Millisecond
)

// Source is an app.bot.or.th/FIPCS crawler. The zero value is not usable; call New.
type Source struct {
	http       *http.Client
	log        *slog.Logger
	baseURL    string
	pdfBaseURL string
}

// New returns a BOT FIPCS source. A nil client creates one with a 60s timeout
// and a cookie jar (required for ASP.NET session tracking). A nil logger discards.
func New(client *http.Client, logger *slog.Logger) *Source {
	if client == nil {
		jar, _ := cookiejar.New(nil)
		client = &http.Client{Timeout: 60 * time.Second, Jar: jar}
	}
	if client.Jar == nil {
		jar, _ := cookiejar.New(nil)
		client.Jar = jar
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Source{
		http:       client,
		log:        logger,
		baseURL:    defaultBaseURL,
		pdfBaseURL: defaultPDFBaseURL,
	}
}

// ID implements ingest.Source.
func (s *Source) ID() string { return SourceID }

// Download streams a PDF file into w and returns the byte count and SHA-256 hex
// digest. BOT PDFs are directly accessible with no session cookies required.
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
