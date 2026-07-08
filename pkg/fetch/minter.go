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

	opts := chromeOpts()
	if p := FindChrome(); p != "" {
		opts = append(opts, chromedp.ExecPath(p))
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

	opts := chromeOpts()
	if p := FindChrome(); p != "" {
		opts = append(opts, chromedp.ExecPath(p))
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
		// Suppress dbus errors in containers — Chrome logs dbus connection
		// failures to stderr, which chromedp merges into stdout for DevTools
		// URL parsing, causing "chrome failed to start" false positives.
		// Setting DBUS_SESSION_BUS_ADDRESS to an invalid value makes Chrome
		// skip dbus entirely instead of logging errors.
		chromedp.Env("DBUS_SESSION_BUS_ADDRESS=disabled:"),
	}
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		opts = append(opts, chromedp.Flag("headless", "new"))
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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
