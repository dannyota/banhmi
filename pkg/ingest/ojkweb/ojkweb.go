// Package ojkweb crawls ojk.go.id, the public-facing website of Indonesia's
// Otoritas Jasa Keuangan (Financial Services Authority). This supplements the
// jdih.ojk.go.id API source (pkg/ingest/ojk) with regulation types that JDIH
// does not expose: PADK, PP, PMK, PPBI, SEBI, and Bapepam classifications.
//
// The site is a SharePoint deployment with ASP.NET postback pagination. It
// geo-blocks non-Indonesian IPs, so all requests route through a forward proxy
// (Config.ProxyURL). Discovery mints a browser session once via OJKMinter,
// then scrapes listing + detail pages over plain HTTP with the minted cookies.
//
// See also docs/design/jurisdictions/INDONESIA.md.
package ojkweb

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"danny.vn/banhmi/pkg/fetch"
	"danny.vn/banhmi/pkg/ingest"
)

// SourceID is the stable identifier for this source.
const SourceID = "ojkweb"

const (
	baseURL      = "https://ojk.go.id"
	listingPage  = "/id/regulasi/Default.aspx"
	challengeURL = baseURL + listingPage
	userAgent    = "banhmi/0.1 (+https://github.com/dannyota/banhmi)"
	maxBodySize  = 32 << 20 // 32 MB
)

// Source is an ojk.go.id SharePoint scraper. The zero value is not usable;
// call New.
type Source struct {
	client *fetch.Client
	log    *slog.Logger
}

// Config holds ojkweb source settings.
type Config struct {
	// ProxyURL is an HTTP/SOCKS5 proxy for ojk.go.id requests (bypasses
	// geo-blocking). Required — the site blocks non-Indonesian IPs.
	ProxyURL string
}

// New returns an ojkweb source. cfg.ProxyURL is required for geo-blocked
// access. A nil logger discards logs.
func New(cfg *Config, logger *slog.Logger) *Source {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	var proxyURL string
	if cfg != nil {
		proxyURL = cfg.ProxyURL
	}

	minter := &fetch.OJKMinter{
		ChallengeURL: challengeURL,
		ProxyURL:     proxyURL,
		Log:          logger,
	}

	client := fetch.New(minter, logger)
	// Replace the default ChromeTransport with a proxied variant when a
	// proxy is configured. ProxiedChromeTransport keeps the Chrome TLS
	// fingerprint while tunnelling through the forward proxy.
	if proxyURL != "" {
		u, _ := url.Parse(proxyURL)
		if u != nil {
			client.HTTP = &http.Client{
				Transport: fetch.ProxiedChromeTransport(u),
				Timeout:   120 * time.Second,
			}
		}
	}

	return &Source{client: client, log: logger}
}

// ID implements ingest.Source.
func (s *Source) ID() string { return SourceID }

// Download streams a regulation PDF into w and returns byte count and SHA-256
// hex digest. PDFs are served from ojk.go.id and need proxy routing for
// geo-blocking, but cookies are carried by the fetch.Client automatically.
func (s *Source) Download(ctx context.Context, ref ingest.FileRef, w io.Writer) (int64, string, error) {
	if ref.URL == "" {
		return 0, "", fmt.Errorf("download: empty url")
	}
	return s.client.Download(ctx, ref.URL, w)
}
