package fetch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// CloudflareMinter solves a Cloudflare Managed Challenge by loading a page in
// headless Chrome and waiting for the cf_clearance cookie to appear. Returns all
// cookies as a raw header value (cf_clearance + __cf_bm + _cfuvid + any app
// cookies) bound to the browser's UA.
type CloudflareMinter struct {
	// URL to load for the challenge (pick a cheap/fast page on the target site).
	ChallengeURL string
	// WaitFor is the cookie name that signals challenge completion. Default: cf_clearance.
	WaitFor string
	// Timeout for the full mint. Default: 60s.
	Timeout time.Duration
	Log     *slog.Logger
}

func (m *CloudflareMinter) Mint(ctx context.Context) (string, string, error) {
	waitFor := m.WaitFor
	if waitFor == "" {
		waitFor = "cf_clearance"
	}
	timeout := m.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	log := m.Log
	if log == nil {
		log = slog.Default()
	}

	chromePath := FindChrome()
	log.Debug("cloudflare mint: starting chrome", "url", m.ChallengeURL, "chrome", chromePath, "timeout", timeout)

	opts := chromeOpts()
	if chromePath != "" {
		opts = append(opts, chromedp.ExecPath(chromePath))
	}
	allocCtx, cancelA := chromedp.NewExecAllocator(ctx, opts...)
	defer cancelA()
	bctx, cancelB := chromedp.NewContext(allocCtx)
	defer cancelB()
	runCtx, cancelT := context.WithTimeout(bctx, timeout)
	defer cancelT()

	var ua, cookieHeader string
	err := chromedp.Run(runCtx,
		chromedp.Navigate(m.ChallengeURL),
		chromedp.Evaluate(`navigator.userAgent`, &ua),
		chromedp.ActionFunc(func(ctx context.Context) error {
			log.Debug("cloudflare mint: navigated, waiting for cookie", "cookie", waitFor)
			deadline := time.Now().Add(timeout - 5*time.Second)
			for {
				cookies, err := network.GetCookies().Do(ctx)
				if err != nil {
					return err
				}
				var parts []string
				found := false
				for _, c := range cookies {
					parts = append(parts, c.Name+"="+c.Value)
					if c.Name == waitFor {
						found = true
					}
				}
				if found {
					cookieHeader = strings.Join(parts, "; ")
					return nil
				}
				if time.Now().After(deadline) {
					return fmt.Errorf("%s not minted within deadline", waitFor)
				}
				if err := sleep(ctx, time.Second); err != nil {
					return err
				}
			}
		}),
	)
	if err != nil {
		return "", "", fmt.Errorf("cloudflare mint: %w", err)
	}
	log.Info("minted Cloudflare session", "url", m.ChallengeURL, "ua", truncate(ua, 50))
	return cookieHeader, ua, nil
}

// AWSWAFMinter solves an AWS WAF JS challenge (bnm.gov.my pattern) by loading
// a page and waiting for the aws-waf-token cookie.
type AWSWAFMinter struct {
	ChallengeURL string
	Timeout      time.Duration
	Log          *slog.Logger
}

func (m *AWSWAFMinter) Mint(ctx context.Context) (string, string, error) {
	timeout := m.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	log := m.Log
	if log == nil {
		log = slog.Default()
	}

	chromePath := FindChrome()
	log.Debug("aws waf mint: starting chrome", "url", m.ChallengeURL, "chrome", chromePath, "timeout", timeout)

	opts := chromeOpts()
	if chromePath != "" {
		opts = append(opts, chromedp.ExecPath(chromePath))
	}
	allocCtx, cancelA := chromedp.NewExecAllocator(ctx, opts...)
	defer cancelA()
	bctx, cancelB := chromedp.NewContext(allocCtx)
	defer cancelB()
	runCtx, cancelT := context.WithTimeout(bctx, timeout)
	defer cancelT()

	var ua, cookieHeader string
	err := chromedp.Run(runCtx,
		chromedp.Navigate(m.ChallengeURL),
		chromedp.Evaluate(`navigator.userAgent`, &ua),
		chromedp.ActionFunc(func(ctx context.Context) error {
			log.Debug("aws waf mint: navigated, waiting for aws-waf-token")
			deadline := time.Now().Add(timeout - 5*time.Second)
			for {
				cookies, err := network.GetCookies().Do(ctx)
				if err != nil {
					return err
				}
				var parts []string
				found := false
				for _, c := range cookies {
					parts = append(parts, c.Name+"="+c.Value)
					if c.Name == "aws-waf-token" {
						found = true
					}
				}
				if found {
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
		return "", "", fmt.Errorf("aws waf mint: %w", err)
	}
	log.Info("minted AWS WAF token", "url", m.ChallengeURL, "ua", truncate(ua, 50))
	return cookieHeader, ua, nil
}

// chromeOpts returns chromedp allocator options that evade WAF bot detection.
// Uses --headless=new (Chrome's "new headless" since v112) which is
// indistinguishable from headed Chrome — no navigator.webdriver=true leak, no
// HeadlessChrome UA suffix, passes Cloudflare Managed Challenge.
// chromeOpts returns chromedp allocator options that evade WAF bot detection.
// Runs HEADED (not headless) when DISPLAY is set — Cloudflare's latest managed
// challenge detects all headless modes (including --headless=new). On headless
// environments (Cloud Run, CI) falls back to --headless=new and relies on the
// minted cookies from a prior local run or a xvfb wrapper.
func chromeOpts() []chromedp.ExecAllocatorOption {
	opts := []chromedp.ExecAllocatorOption{
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("disable-features", "AutomationControlled"),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("disable-software-rasterizer", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("window-size", "1920,1080"),
	}
	headed := os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
	if !headed {
		opts = append(opts, chromedp.Flag("headless", "new"))
		// Suppress dbus errors in containers — Chrome logs dbus connection
		// failures to stderr, which chromedp merges into stdout for DevTools
		// URL parsing, causing "chrome failed to start" false positives.
		opts = append(opts, chromedp.Env("DBUS_SESSION_BUS_ADDRESS=disabled:"))
	}
	return opts
}

// FindChrome locates a Chrome/Chromium binary: BANHMI_CHROME_PATH → Playwright
// cache (dev) → PATH lookup. Empty lets chromedp use its default.
func FindChrome() string {
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

// OJKMinter solves the ojk.go.id SharePoint session by loading a page through
// a proxy (the site geo-blocks non-Indonesian IPs) and waiting for the c_ojk
// cookie. ProxyURL is the HTTP/SOCKS5 forward proxy address (required).
type OJKMinter struct {
	ChallengeURL string
	ProxyURL     string
	Timeout      time.Duration
	Log          *slog.Logger
}

func (m *OJKMinter) Mint(ctx context.Context) (string, string, error) {
	timeout := m.Timeout
	if timeout == 0 {
		timeout = 90 * time.Second
	}
	log := m.Log
	if log == nil {
		log = slog.Default()
	}

	chromePath := FindChrome()
	log.Debug("ojk mint: starting chrome", "url", m.ChallengeURL, "proxy", m.ProxyURL, "chrome", chromePath, "timeout", timeout)

	opts := chromeOpts()
	if chromePath != "" {
		opts = append(opts, chromedp.ExecPath(chromePath))
	}
	if m.ProxyURL != "" {
		opts = append(opts, chromedp.ProxyServer(m.ProxyURL))
	}
	allocCtx, cancelA := chromedp.NewExecAllocator(ctx, opts...)
	defer cancelA()
	bctx, cancelB := chromedp.NewContext(allocCtx)
	defer cancelB()
	runCtx, cancelT := context.WithTimeout(bctx, timeout)
	defer cancelT()

	var ua, cookieHeader string
	err := chromedp.Run(runCtx,
		chromedp.Navigate(m.ChallengeURL),
		chromedp.Evaluate(`navigator.userAgent`, &ua),
		chromedp.ActionFunc(func(ctx context.Context) error {
			log.Debug("ojk mint: navigated, waiting for c_ojk cookie")
			// Derive the cookie-poll deadline from the context rather than
			// computing a fresh duration from time.Now() (which would ignore
			// time already consumed by Navigate+Evaluate). Use whichever is
			// sooner: the context deadline or now+remaining-budget.
			pollBudget := timeout - 5*time.Second
			if pollBudget < 2*time.Second {
				pollBudget = 2 * time.Second
			}
			deadline := time.Now().Add(pollBudget)
			if ctxDL, ok := ctx.Deadline(); ok && ctxDL.Before(deadline) {
				deadline = ctxDL.Add(-500 * time.Millisecond) // stay inside ctx
			}
			for {
				cookies, err := network.GetCookies().Do(ctx)
				if err != nil {
					return err
				}
				var parts []string
				found := false
				for _, c := range cookies {
					parts = append(parts, c.Name+"="+c.Value)
					if c.Name == "c_ojk" {
						found = true
					}
				}
				if found {
					cookieHeader = strings.Join(parts, "; ")
					return nil
				}
				if time.Now().After(deadline) {
					// SharePoint may not always set c_ojk; accept any cookies
					// gathered after the page loads so the session is usable.
					if len(parts) > 0 {
						log.Warn("ojk mint: c_ojk not found, using available cookies", "count", len(parts))
						cookieHeader = strings.Join(parts, "; ")
						return nil
					}
					return fmt.Errorf("c_ojk not minted within deadline")
				}
				if err := sleep(ctx, time.Second); err != nil {
					return err
				}
			}
		}),
	)
	if err != nil {
		return "", "", fmt.Errorf("ojk mint: %w", err)
	}
	log.Info("minted OJK session", "url", m.ChallengeURL, "ua", truncate(ua, 50))
	return cookieHeader, ua, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
