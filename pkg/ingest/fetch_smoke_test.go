package ingest_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"danny.vn/banhmi/pkg/ingest"
	"danny.vn/banhmi/pkg/ingest/agclom"
	"danny.vn/banhmi/pkg/ingest/bi"
	"danny.vn/banhmi/pkg/ingest/bnm"
	"danny.vn/banhmi/pkg/ingest/bpk"
	"danny.vn/banhmi/pkg/ingest/congbao"
	"danny.vn/banhmi/pkg/ingest/sbvhanoi"
	"danny.vn/banhmi/pkg/ingest/sc"
	"danny.vn/banhmi/pkg/ingest/vanban"
	"danny.vn/banhmi/pkg/ingest/vbpl"
)

// TestFetchSmoke is a temporary live-network smoke test that fetches one
// document from every source across all jurisdictions (VN, MY, ID). It
// calls FetchDetail to get file refs, then Download to pull one file.
//
// Run with: FETCH_SMOKE=1 go test -v -run TestFetchSmoke -timeout 5m ./pkg/ingest/
// Skip with: go test -short, or omit FETCH_SMOKE=1.
func TestFetchSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live-network smoke test in short mode")
	}
	if os.Getenv("FETCH_SMOKE") != "1" {
		t.Skip("set FETCH_SMOKE=1 to run live-network smoke tests")
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	tests := []struct {
		name      string
		source    ingest.Source
		detailRef ingest.DetailRef
		minBytes  int64
	}{
		// ── VN ──────────────────────────────────────────────────────
		{
			name:   "VN/congbao",
			source: congbao.New(nil, log),
			detailRef: ingest.DetailRef{
				ExternalID: "noi-dung/thong-tu-11-2026-tt-nhnn-52974",
			},
			minBytes: 1000,
		},
		{
			name:   "VN/vbpl",
			source: vbpl.New(nil, log, nil, nil, nil),
			detailRef: ingest.DetailRef{
				// Thông tư 39/2016/TT-NHNN — well-known SBV circular.
				ExternalID: "26928",
			},
			minBytes: 1000,
		},
		{
			name:   "VN/vanban",
			source: vanban.New(nil, log),
			detailRef: ingest.DetailRef{
				ExternalID: "216334",
			},
			minBytes: 1000,
		},
		{
			name:   "VN/sbvhanoi",
			source: sbvhanoi.New(nil, log),
			detailRef: ingest.DetailRef{
				ExternalID: "77102",
			},
			minBytes: 1000,
		},

		// ── MY ──────────────────────────────────────────────────────
		{
			name:   "MY/agclom",
			source: agclom.New(nil, log),
			detailRef: ingest.DetailRef{
				// Financial Services Act 2013 (Act 758).
				ExternalID: "758",
			},
			minBytes: 1000,
		},
		{
			name:   "MY/bnm",
			source: bnm.New(nil, log),
			detailRef: ingest.DetailRef{
				// Risk Management in Technology (RMiT) policy document.
				ExternalID: "/documents/20124/938039/pd-rmit-nov25.pdf",
			},
			minBytes: 1000,
		},
		{
			name:   "MY/sc",
			source: sc.New(nil, log),
			detailRef: ingest.DetailRef{
				// SC Guidelines — a known published guideline GUID.
				ExternalID: "2f253636-07dd-4355-b89e-010b2ef581c1",
			},
			minBytes: 1000,
		},

		// ── ID ──────────────────────────────────────────────────────
		{
			name:   "ID/bpk",
			source: bpk.New(nil, log),
			detailRef: ingest.DetailRef{
				// UU No. 4 Tahun 2023 — a known BPK regulation.
				ExternalID: "350261",
			},
			minBytes: 1000,
		},
		{
			name:   "ID/bi",
			source: bi.New(nil, log),
			detailRef: ingest.DetailRef{
				// PBI No.24/4/PBI/2022 — a Bank Indonesia regulation.
				ExternalID: "1295",
			},
			minBytes: 1000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("FetchDetail %s id=%s", tt.source.ID(), tt.detailRef.ExternalID)

			doc, err := tt.source.FetchDetail(ctx, tt.detailRef)
			if err != nil {
				t.Fatalf("FetchDetail: %v", err)
			}
			t.Logf("  number=%s title=%s files=%d html=%d",
				doc.Number, truncate(doc.Title, 60), len(doc.Files), len(doc.HTML))

			if len(doc.Files) == 0 {
				if len(doc.HTML) > 0 {
					t.Logf("  no files but has inline HTML (%d bytes) — OK", len(doc.HTML))
					return
				}
				t.Fatal("no files and no inline HTML")
			}

			ref := doc.Files[0]
			t.Logf("  downloading %s (%s, %s) url=%s", ref.Name, ref.Ext, ref.Kind, ref.URL)

			n, sha, err := tt.source.Download(ctx, ref, io.Discard)
			if err != nil {
				t.Fatalf("Download: %v", err)
			}
			t.Logf("  OK: %d bytes, sha256=%s", n, sha)

			if n < tt.minBytes {
				t.Errorf("downloaded only %d bytes, want >= %d", n, tt.minBytes)
			}
			if sha == "" {
				t.Error("empty SHA-256")
			}
		})
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
