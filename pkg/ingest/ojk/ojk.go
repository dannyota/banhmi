// Package ojk crawls Indonesia's OJK JDIH (jdih.ojk.go.id), the legal
// repository of the Otoritas Jasa Keuangan (Financial Services Authority).
// OJK publishes POJK (Peraturan OJK), SEOJK (Surat Edaran OJK), and UU
// (Undang-Undang) regulations with metadata, status, relations, and PDF files.
//
// Discovery uses a DataTables-style POST endpoint (ListDataPeraturan) with
// offset pagination per jenisPeraturan type. Detail metadata is parsed from the
// server-rendered HTML detail page. PDF download is unauthenticated.
//
// No WAF protection: the site uses F5 BIG-IP cookies set automatically; a plain
// fetch.Client with Chrome TLS fingerprint works without a minter.
//
// See also docs/design/jurisdictions/INDONESIA.md.
package ojk

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"danny.vn/banhmi/pkg/fetch"
	"danny.vn/banhmi/pkg/ingest"
)

// SourceID is the stable identifier for this source.
const SourceID = "ojk"

const (
	baseURL   = "https://jdih.ojk.go.id"
	userAgent = "banhmi/0.1 (+https://github.com/dannyota/banhmi)"

	listingPath  = "/Web/ViewPeraturan/ListDataPeraturan"
	detailPath   = "/Web/ViewPeraturan/Detail"          // /{UUID}/{sektor}/{jenis}
	downloadPath = "/Web/ViewPeraturan/DownloadDokumen" // /{UUID}
)

// jenisPeraturan maps the OJK regulation type codes to their DocType labels.
// 06=POJK (560 docs), 09=SEOJK (407 docs), 01=UU (12 docs).
var jenisPeraturan = map[string]ingest.DocType{
	"06": "POJK",
	"09": "SEOJK",
	"01": "UU",
}

// jenisOrder is the enumeration order for discovery (deterministic: POJK first
// as largest, then SEOJK, then UU).
var jenisOrder = []string{"06", "09", "01"}

// Source is a jdih.ojk.go.id crawler. The zero value is not usable; call New.
type Source struct {
	client *fetch.Client
	log    *slog.Logger
}

// New returns an OJK source. A nil client uses fetch.New(nil, log) (Chrome TLS
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

// Download streams an OJK regulation PDF into w and returns the byte count and
// SHA-256 hex digest. Uses the DownloadDokumen endpoint.
func (s *Source) Download(ctx context.Context, ref ingest.FileRef, w io.Writer) (int64, string, error) {
	if ref.URL == "" {
		return 0, "", fmt.Errorf("download: empty url")
	}
	return s.client.Download(ctx, ref.URL, w)
}

// detailURL returns the human-visible detail page URL for a document.
func detailURL(uuid, sektor, jenis string) string {
	return baseURL + detailPath + "/" + uuid + "/" + sektor + "/" + jenis
}

// downloadURL returns the PDF download URL for a document UUID.
func downloadURL(uuid string) string {
	return baseURL + downloadPath + "/" + uuid
}
