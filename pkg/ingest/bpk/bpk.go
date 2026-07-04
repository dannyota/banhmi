// Package bpk crawls Indonesia's BPK JDIH (peraturan.bpk.go.id), the national
// regulation database maintained by the Supreme Audit Board (Badan Pemeriksa
// Keuangan). BPK carries UU (laws), PP (government regulations), POJK (OJK
// financial-sector regulations), and SEOJK (OJK circulars) with status relations
// — replacing the geo-fenced OJK and peraturan.go.id sources.
//
// All HTML pages are behind a Cloudflare Managed Challenge; the crawler uses
// pkg/fetch.Client with a CloudflareMinter to mint-and-reuse session cookies.
// PDF download paths (/Download/) need no cookies.
//
// Discovery enumerates four jenis (type) listings newest-first:
// UU=8, PP=10, POJK=80, SEOJK=212. PBI (jenis=78) is intentionally excluded —
// PBI/PADG come from the separate bi source.
package bpk

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"danny.vn/banhmi/pkg/fetch"
	"danny.vn/banhmi/pkg/ingest"
)

// SourceID is the stable identifier for this source.
const SourceID = "bpk"

const (
	baseURL      = "https://peraturan.bpk.go.id"
	challengeURL = "https://peraturan.bpk.go.id/Search?jenis=80" // cheap listing page for minting
)

// Source is a peraturan.bpk.go.id crawler. The zero value is not usable; call New.
type Source struct {
	client *fetch.Client
	log    *slog.Logger
}

// New returns a bpk source. If client is nil a default fetch.Client with a
// CloudflareMinter is constructed (suitable for production; tests inject a
// custom client).
func New(client *fetch.Client, logger *slog.Logger) *Source {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if client == nil {
		client = fetch.New(&fetch.CloudflareMinter{
			ChallengeURL: challengeURL,
			Log:          logger,
		}, logger)
	}
	return &Source{client: client, log: logger}
}

// ID implements ingest.Source.
func (s *Source) ID() string { return SourceID }

// Download streams a file's bytes into w and returns the byte count and SHA-256
// hex digest. PDF paths on BPK need no cookies but we still use the fetch client
// for its retry and hash logic.
func (s *Source) Download(ctx context.Context, ref ingest.FileRef, w io.Writer) (int64, string, error) {
	if ref.URL == "" {
		return 0, "", fmt.Errorf("download: empty url")
	}
	return s.client.Download(ctx, ref.URL, w)
}
