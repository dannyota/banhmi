// Package bnm crawls Bank Negara Malaysia (bnm.gov.my), the primary Malaysian
// banking regulator, for its banking & financial regulation and cross-cutting
// technology policy documents (RMiT, e-KYC, cloud, outsourcing, business continuity,
// e-money, digital banks, open finance, operational resilience). BNM is the Malaysian
// analog of Vietnam's SBV portal.
//
// BNM sits behind AWS WAF "Challenge": a JS proof-of-work mints an `aws-waf-token`
// cookie that plain HTTP cannot compute. The shared fetch.Client + AWSWAFMinter
// handles minting once and reusing the session for all requests, re-minting on
// challenge. See docs/design/jurisdictions/MALAYSIA.md.
package bnm

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"danny.vn/banhmi/pkg/fetch"
	"danny.vn/banhmi/pkg/ingest"
)

// SourceID is the stable identifier for this source.
const SourceID = "bnm"

const (
	defaultBaseURL = "https://www.bnm.gov.my"
	// mintPath is a cheap page used to solve the WAF challenge and mint the token.
	mintPath = "/banking-islamic-banking"

	pacePage = 400 * time.Millisecond
)

// Source is a bnm.gov.my crawler. The zero value is not usable; call New.
type Source struct {
	client  *fetch.Client
	log     *slog.Logger
	baseURL string
}

// New returns a bnm source. A nil client constructs a default fetch.Client with
// an AWSWAFMinter for the BNM WAF challenge. A nil logger discards logs.
func New(client *fetch.Client, logger *slog.Logger) *Source {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if client == nil {
		client = fetch.New(&fetch.AWSWAFMinter{
			ChallengeURL: defaultBaseURL + mintPath,
			Log:          logger,
		}, logger)
	}
	return &Source{client: client, log: logger, baseURL: defaultBaseURL}
}

// ID implements ingest.Source.
func (s *Source) ID() string { return SourceID }

// get fetches a URL reusing the WAF session. Delegates to fetch.Client.Get
// which handles minting, re-minting on challenge, and retries.
func (s *Source) get(ctx context.Context, rawURL string) (string, error) {
	return s.client.Get(ctx, rawURL)
}

// Download streams a BNM PDF into w and returns the byte count and SHA-256
// hex digest. Delegates to fetch.Client.Download which handles WAF session
// and retries.
func (s *Source) Download(ctx context.Context, ref ingest.FileRef, w io.Writer) (int64, string, error) {
	if ref.URL == "" {
		return 0, "", fmt.Errorf("download: empty url")
	}
	return s.client.Download(ctx, ref.URL, w)
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
