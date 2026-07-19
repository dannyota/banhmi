// Package bi crawls Bank Indonesia's legal database (jdih.bi.go.id) for PBI
// (Peraturan Bank Indonesia) and PADG (Peraturan Anggota Dewan Gubernur)
// regulations. BI owns payment-systems regulation in Indonesia.
//
// Discovery parses the server-rendered HTML listing (one page, ~4.3 MB, all
// regulations) filtered by JenisPeraturanID (1=PBI, 2=PADG). Per-document
// metadata comes from a clean JSON API (no bot protection, no WAF).
// Relations use the forward fields (Mengubah, Mencabut) only; reverse fields
// (Diubah, Dicabut) and PeraturanTerkait are preserved in RawMeta but not
// emitted as authoritative Relations (see INDONESIA.md). See also
// docs/design/jurisdictions/INDONESIA.md.
package bi

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"danny.vn/banhmi/pkg/fetch"
	"danny.vn/banhmi/pkg/ingest"
)

// SourceID is the stable identifier for this source.
const SourceID = "bi"

const (
	baseURL   = "https://jdih.bi.go.id"
	userAgent = "banhmi/0.1 (+https://github.com/dannyota/banhmi)"

	listingPath = "/Web/DaftarPeraturan"
	detailAPI   = "/api/WebJDIH/GetDataWebPeraturan"    // ?PeraturanID={id}
	downloadAPI = "/api/WebJDIH/DownloadFilePeraturan/" // {id}
)

// jenisPeraturanPBI and jenisPeraturanPADG are the JenisPeraturanID values for the
// two regulation types banhmi crawls. SE Ekstern (3) and UU (4) are excluded — UU
// comes from BPK; SE Ekstern is out of scope.
const (
	jenisPeraturanPBI  = 1
	jenisPeraturanPADG = 2
)

// Source is a jdih.bi.go.id crawler. The zero value is not usable; call New.
type Source struct {
	client *fetch.Client
	log    *slog.Logger
}

// New returns a BI source. A nil client uses fetch.New(nil, log) (Chrome TLS
// fingerprint, no WAF minter). A nil logger discards logs.
func New(client *fetch.Client, logger *slog.Logger) *Source {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if client == nil {
		client = fetch.New(nil, logger)
	}
	return &Source{client: client, log: logger}
}

// ID implements ingest.Source.
func (s *Source) ID() string { return SourceID }

// Download streams a BI regulation PDF into w and returns the byte count and
// SHA-256 hex digest. Uses the DownloadFilePeraturan API endpoint.
func (s *Source) Download(ctx context.Context, ref ingest.FileRef, w io.Writer) (int64, string, error) {
	if ref.URL == "" {
		return 0, "", fmt.Errorf("download: empty url")
	}
	return s.client.Download(ctx, ref.URL, w)
}

// detailURL returns the human-visible detail page URL for a PeraturanID.
func detailURL(id string) string {
	return baseURL + "/Web/DaftarPeraturan/Detail/" + id
}

// apiDetailURL returns the JSON API URL for a PeraturanID.
func apiDetailURL(id string) string {
	return baseURL + detailAPI + "?PeraturanID=" + id
}

// downloadURL returns the PDF download URL for a PeraturanID.
func downloadURL(id string) string {
	return baseURL + downloadAPI + id
}
