// Package fetch provides a browser-impersonating HTTP client for sites behind
// WAF challenges (Cloudflare, AWS WAF) — the reusable core of the BNM/BPK mint-
// and-reuse pattern. Two layers:
//
//  1. A utls-backed http.Transport that presents a Chrome TLS fingerprint
//     (ClientHello, extensions, cipher suites) — defeats TLS-fingerprinting bots
//     without needing a full browser for every request.
//
//  2. A chromedp-based cookie minter that solves JS challenges once, caches the
//     session cookies, and re-mints on expiry. The minted cookies are reused on
//     the utls transport so bulk fetches are fast plain-HTTP, not headless per req.
//
// Sources compose these: BNM uses WAFSession (AWS challenge token); BPK needs
// CloudflareSession (cf_clearance + __cf_bm via Chrome); BI needs neither.
package fetch

import (
	"context"
	"crypto/sha256"
	stdtls "crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"sync"
	"time"

	tls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
)

// Client wraps an http.Client with browser-impersonating TLS and optional WAF
// session management. Create with New.
type Client struct {
	HTTP *http.Client
	Log  *slog.Logger

	mu      sync.Mutex
	cookies string // cached session cookies (raw Cookie header value)
	ua      string // the UA the cookies are bound to
	minter  Minter // nil = no auto-minting
}

// Minter solves a WAF challenge and returns (cookieHeader, userAgent, err).
// Implementations: AWSWAFMinter, CloudflareMinter.
type Minter interface {
	Mint(ctx context.Context) (cookies string, ua string, err error)
}

// New returns a Client with a Chrome-fingerprinted TLS transport and the given
// minter. If minter is nil the client works as a plain browser-UA client (for
// sources like BI that have no WAF).
func New(minter Minter, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Client{
		HTTP:   &http.Client{Transport: ChromeTransport(), Timeout: 90 * time.Second},
		Log:    logger,
		minter: minter,
	}
}

// Get fetches a URL using the WAF session (minting on first use, re-minting on
// challenge). Returns the response body as a string. Limit 32 MB.
// Retries up to 2 times on transient transport errors.
func (c *Client) Get(ctx context.Context, rawURL string) (string, error) {
	cookies, ua, err := c.session(ctx, false)
	if err != nil {
		return "", err
	}

	var lastErr error
	for attempt := 0; attempt <= 2; attempt++ {
		if attempt > 0 {
			if err := sleep(ctx, time.Duration(float64(time.Second)*math.Pow(2, float64(attempt-1)))); err != nil {
				return "", err
			}
			c.Log.Info("retrying Get on transport error", "url", rawURL, "attempt", attempt, "err", lastErr)
		}
		body, status, err := c.doGet(ctx, rawURL, cookies, ua)
		if err != nil {
			if transientErr(err) {
				lastErr = err
				continue
			}
			return "", err
		}
		if !challenged(status) {
			return body, nil
		}
		// WAF challenge — re-mint and try once more (no further retries).
		c.Log.Info("WAF challenge; re-minting", "url", rawURL, "status", status)
		cookies, ua, err = c.session(ctx, true)
		if err != nil {
			return "", err
		}
		body, status, err = c.doGet(ctx, rawURL, cookies, ua)
		if err != nil {
			return "", err
		}
		if challenged(status) || status < 200 || status >= 300 {
			return "", fmt.Errorf("status %d after re-mint", status)
		}
		return body, nil
	}
	return "", fmt.Errorf("get %s: exhausted retries: %w", rawURL, lastErr)
}

// Download streams a URL into w, returning bytes written and SHA-256 hex digest.
// Re-mints once on challenge; retries transient errors up to 3 times.
func (c *Client) Download(ctx context.Context, rawURL string, w io.Writer) (int64, string, error) {
	cookies, ua, err := c.session(ctx, false)
	if err != nil {
		return 0, "", err
	}
	for attempt := 0; attempt <= 3; attempt++ {
		if attempt > 0 {
			if err := sleep(ctx, time.Duration(float64(time.Second)*math.Pow(2, float64(attempt-1)))); err != nil {
				return 0, "", err
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return 0, "", fmt.Errorf("build request: %w", err)
		}
		c.setHeaders(req, cookies, ua)
		resp, err := c.HTTP.Do(req)
		if err != nil {
			if transientErr(err) {
				c.Log.Info("retrying download on transport error", "url", rawURL, "attempt", attempt, "err", err)
				continue
			}
			return 0, "", err
		}
		if challenged(resp.StatusCode) {
			drainClose(resp.Body)
			cookies, ua, err = c.session(ctx, true)
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
			return 0, "", fmt.Errorf("download %s: status %d", rawURL, resp.StatusCode)
		}
		h := sha256.New()
		n, err := io.Copy(io.MultiWriter(w, h), resp.Body)
		drainClose(resp.Body)
		if err != nil {
			return n, "", fmt.Errorf("download: copy body: %w", err)
		}
		return n, hex.EncodeToString(h.Sum(nil)), nil
	}
	return 0, "", errors.New("download: exhausted retries")
}

// Session returns the current cached (cookies, ua) or mints fresh ones.
// Exported for callers that need to set cookies on custom requests (e.g.
// SharePoint postbacks) while still using the minter lifecycle.
func (c *Client) Session(ctx context.Context) (string, string, error) {
	return c.session(ctx, false)
}

// session returns the current cached (cookies, ua) or mints fresh ones.
func (c *Client) session(ctx context.Context, force bool) (string, string, error) {
	if c.minter == nil {
		return "", DefaultUA, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if force {
		c.cookies, c.ua = "", ""
	}
	if c.cookies != "" {
		return c.cookies, c.ua, nil
	}
	cookies, ua, err := c.minter.Mint(ctx)
	if err != nil {
		return "", "", err
	}
	c.cookies, c.ua = cookies, ua
	return cookies, ua, nil
}

func (c *Client) doGet(ctx context.Context, rawURL, cookies, ua string) (string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", 0, fmt.Errorf("build request: %w", err)
	}
	c.setHeaders(req, cookies, ua)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer drainClose(resp.Body)
	if challenged(resp.StatusCode) {
		return "", resp.StatusCode, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", resp.StatusCode, fmt.Errorf("status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return "", resp.StatusCode, fmt.Errorf("read body: %w", err)
	}
	return string(b), resp.StatusCode, nil
}

func (c *Client) setHeaders(req *http.Request, cookies, ua string) {
	if ua == "" {
		ua = DefaultUA
	}
	req.Header.Set("User-Agent", ua)
	if cookies != "" {
		req.Header.Set("Cookie", cookies)
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/pdf,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9,id;q=0.8,vi;q=0.7")
}

// DefaultUA is the browser UA used when no WAF-minted UA is set.
const DefaultUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36"

// ChromeTransport returns an http.RoundTripper that impersonates Chrome's TLS
// fingerprint via utls. Supports both HTTP/2 (Cloudflare, most CDNs) and
// HTTP/1.1 (BI JDIH) transparently by checking ALPN after handshake.
func ChromeTransport() http.RoundTripper {
	return &chromeRT{}
}

type chromeRT struct{}

func (t *chromeRT) RoundTrip(req *http.Request) (*http.Response, error) {
	addr := req.URL.Host
	if !hasPort(addr) {
		if req.URL.Scheme == "https" {
			addr += ":443"
		} else {
			addr += ":80"
		}
	}
	host, _, _ := net.SplitHostPort(addr)
	if host == "" {
		host = req.URL.Hostname()
	}

	dialer := &net.Dialer{Timeout: 30 * time.Second}
	tcpConn, err := dialer.DialContext(req.Context(), "tcp", addr)
	if err != nil {
		return nil, err
	}
	config := &tls.Config{ServerName: host}
	utlsConn := tls.UClient(tcpConn, config, tls.HelloChrome_Auto)
	if err := utlsConn.HandshakeContext(req.Context()); err != nil {
		_ = tcpConn.Close()
		return nil, err
	}

	alpn := utlsConn.ConnectionState().NegotiatedProtocol
	if alpn == "h2" {
		h2t := &http2.Transport{
			DialTLSContext: func(_ context.Context, _, _ string, _ *stdtls.Config) (net.Conn, error) {
				return utlsConn, nil
			},
		}
		resp, err := h2t.RoundTrip(req)
		if err != nil {
			_ = utlsConn.Close()
			return nil, err
		}
		resp.Body = &connClosingBody{ReadCloser: resp.Body, conn: utlsConn}
		return resp, nil
	}

	// HTTP/1.1 fallback.
	h1t := &http.Transport{
		DialTLSContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return utlsConn, nil
		},
		DisableKeepAlives: true,
	}
	resp, err := h1t.RoundTrip(req)
	if err != nil {
		_ = utlsConn.Close()
		return nil, err
	}
	resp.Body = &connClosingBody{ReadCloser: resp.Body, conn: utlsConn}
	return resp, nil
}

func hasPort(host string) bool {
	_, _, err := net.SplitHostPort(host)
	return err == nil
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

// connClosingBody wraps a response body so that Close also closes the
// underlying TCP/TLS connection. This prevents connection leaks in chromeRT,
// which dials a fresh connection per request and uses throwaway transports.
type connClosingBody struct {
	io.ReadCloser
	conn net.Conn
}

func (b *connClosingBody) Close() error {
	err := b.ReadCloser.Close()
	_ = b.conn.Close()
	return err
}

// transientErr reports whether err is a transient transport-level error worth
// retrying: timeouts, temporary net errors, connection resets/refused, and DNS
// failures. Context cancellation and non-network errors are NOT transient.
func transientErr(err error) bool {
	if err == nil {
		return false
	}
	// Context canceled/deadline — not retryable (caller gave up).
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	// net.Error with Timeout or Temporary.
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout() || netErr.Temporary() //nolint:staticcheck // Temporary is deprecated but still useful
	}
	// Connection refused/reset and DNS errors surface as *net.OpError.
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr)
}
