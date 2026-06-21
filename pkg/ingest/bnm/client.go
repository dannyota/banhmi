// Package bnm crawls Bank Negara Malaysia (bnm.gov.my), the primary Malaysian
// banking regulator, for its digital/technology policy documents (RMiT, e-KYC,
// cloud, outsourcing, business continuity, e-money, digital banks, open finance,
// operational resilience). BNM is the Malaysian analog of Vietnam's SBV portal.
//
// BNM sits behind AWS WAF "Challenge": a JS proof-of-work mints an `aws-waf-token`
// cookie that plain HTTP cannot compute. So the crawler mints the token ONCE per
// session with a headless Chrome (chromedp), then reuses the cookie + matching
// User-Agent in a plain net/http client for all listing fetches and PDF downloads,
// re-minting on a 202/403 challenge. The sector listing pages are server-rendered
// (the whole list is in the raw HTML; JS only paginates the display), and each row
// links directly to a born-digital PDF. English only, per the one-language policy.
// See docs/design/MALAYSIA.md.
package bnm

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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"danny.vn/banhmi/pkg/ingest"
)

// SourceID is the stable identifier for this source.
const SourceID = "bnm"

const (
	defaultBaseURL = "https://www.bnm.gov.my"
	// mintPath is a cheap page used to solve the WAF challenge and mint the token.
	mintPath   = "/banking-islamic-banking"
	maxRetries = 3

	baseBackoff = time.Second
	pacePage    = 400 * time.Millisecond
)

// Source is a bnm.gov.my crawler. The zero value is not usable; call New.
type Source struct {
	http       *http.Client
	log        *slog.Logger
	baseURL    string
	chromePath string

	mu        sync.Mutex // guards the cached WAF session below
	wafCookie string     // "aws-waf-token=…; AWSALB=…" reused on every request
	wafUA     string     // the minting browser's UA (the token is UA-bound)
}

// New returns a bnm source. A nil client uses a 90s timeout; a nil logger discards.
func New(client *http.Client, logger *slog.Logger) *Source {
	if client == nil {
		client = &http.Client{Timeout: 90 * time.Second}
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Source{http: client, log: logger, baseURL: defaultBaseURL, chromePath: findChrome()}
}

// ID implements ingest.Source.
func (s *Source) ID() string { return SourceID }

// get fetches a URL reusing the WAF session, minting the token on first use and
// re-minting once on a challenge (202/403). Returns the UTF-8 body.
func (s *Source) get(ctx context.Context, rawURL string) (string, error) {
	cookie, ua, err := s.session(ctx, false)
	if err != nil {
		return "", err
	}
	body, status, err := s.rawGet(ctx, rawURL, cookie, ua)
	if err == nil && !challenged(status) {
		return body, nil
	}
	if !challenged(status) {
		return "", err
	}
	// WAF challenge — re-mint once and retry.
	s.log.Info("bnm WAF challenge; re-minting token", "url", rawURL, "status", status)
	cookie, ua, err = s.session(ctx, true)
	if err != nil {
		return "", err
	}
	body, status, err = s.rawGet(ctx, rawURL, cookie, ua)
	if err != nil {
		return "", err
	}
	if challenged(status) || status < 200 || status >= 300 {
		return "", fmt.Errorf("status %d after re-mint", status)
	}
	return body, nil
}

// rawGet performs a single GET with the WAF cookie + UA and returns body+status.
func (s *Source) rawGet(ctx context.Context, rawURL, cookie, ua string) (string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/pdf,*/*;q=0.8")
	req.Header.Set("Referer", s.baseURL+mintPath)
	resp, err := s.http.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer drainClose(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", resp.StatusCode, fmt.Errorf("status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return "", resp.StatusCode, fmt.Errorf("read body: %w", err)
	}
	return string(b), resp.StatusCode, nil
}

// Download streams a BNM PDF (reusing the WAF session) into w with its SHA-256.
func (s *Source) Download(ctx context.Context, ref ingest.FileRef, w io.Writer) (int64, string, error) {
	if ref.URL == "" {
		return 0, "", fmt.Errorf("download: empty url")
	}
	cookie, ua, err := s.session(ctx, false)
	if err != nil {
		return 0, "", err
	}
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
		req.Header.Set("User-Agent", ua)
		req.Header.Set("Cookie", cookie)
		req.Header.Set("Referer", s.baseURL+mintPath)
		resp, err := s.http.Do(req)
		if err != nil {
			return 0, "", err
		}
		if challenged(resp.StatusCode) { // token expired mid-crawl — re-mint and retry
			drainClose(resp.Body)
			cookie, ua, err = s.session(ctx, true)
			if err != nil {
				return 0, "", err
			}
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
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
	return 0, "", errors.New("download: exhausted retries")
}

// session returns the cached WAF cookie + UA, minting on first use; force re-mints.
func (s *Source) session(ctx context.Context, force bool) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if force {
		s.wafCookie, s.wafUA = "", ""
	}
	if s.wafCookie != "" {
		return s.wafCookie, s.wafUA, nil
	}
	cookie, ua, err := s.mintWAF(ctx)
	if err != nil {
		return "", "", err
	}
	s.wafCookie, s.wafUA = cookie, ua
	return cookie, ua, nil
}

// mintWAF runs the AWS WAF JS challenge in a headless Chrome and returns the cookie
// header (incl. aws-waf-token) and the browser User-Agent the token is bound to.
func (s *Source) mintWAF(ctx context.Context) (string, string, error) {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.NoFirstRun, chromedp.NoDefaultBrowserCheck,
	)
	if s.chromePath != "" {
		opts = append(opts, chromedp.ExecPath(s.chromePath))
	}
	allocCtx, cancelA := chromedp.NewExecAllocator(ctx, opts...)
	defer cancelA()
	bctx, cancelB := chromedp.NewContext(allocCtx)
	defer cancelB()
	runCtx, cancelT := context.WithTimeout(bctx, 60*time.Second)
	defer cancelT()

	var ua, cookieHeader string
	err := chromedp.Run(runCtx,
		chromedp.Navigate(s.baseURL+mintPath),
		chromedp.Evaluate(`navigator.userAgent`, &ua),
		chromedp.ActionFunc(func(ctx context.Context) error {
			deadline := time.Now().Add(35 * time.Second)
			for {
				cookies, err := network.GetCookies().Do(ctx)
				if err != nil {
					return err
				}
				var parts []string
				has := false
				for _, c := range cookies {
					parts = append(parts, c.Name+"="+c.Value)
					if c.Name == "aws-waf-token" {
						has = true
					}
				}
				if has {
					cookieHeader = strings.Join(parts, "; ")
					return nil
				}
				if time.Now().After(deadline) {
					return errors.New("aws-waf-token not minted within deadline")
				}
				if err := sleep(ctx, time.Second); err != nil {
					return err
				}
			}
		}),
	)
	if err != nil {
		return "", "", fmt.Errorf("waf mint: %w", err)
	}
	s.log.Info("bnm minted WAF token", "ua", truncate(ua, 40))
	return cookieHeader, ua, nil
}

// findChrome locates a Chrome/Chromium binary for chromedp: BANHMI_CHROME_PATH, the
// local Playwright cache (dev), or a PATH lookup. Empty lets chromedp use its default.
func findChrome() string {
	if p := os.Getenv("BANHMI_CHROME_PATH"); p != "" {
		return p
	}
	if home := os.Getenv("HOME"); home != "" {
		if hits, _ := filepath.Glob(home + "/.cache/ms-playwright/chromium-*/chrome-linux/chrome"); len(hits) > 0 {
			return hits[len(hits)-1]
		}
	}
	for _, c := range []string{"google-chrome", "chromium", "chromium-browser", "google-chrome-stable"} {
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	return ""
}

func challenged(status int) bool { return status == 202 || status == 403 }

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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
