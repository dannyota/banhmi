package bi

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

// --- Card parsing tests ---

func TestParseCard(t *testing.T) {
	html, err := os.ReadFile("testdata/listing_minimal.html")
	if err != nil {
		t.Fatalf("read listing fixture: %v", err)
	}

	cards := cardRe.FindAllStringSubmatch(string(html), -1)
	if len(cards) != 4 {
		t.Fatalf("expected 4 cards, got %d", len(cards))
	}

	tests := []struct {
		name    string
		idx     int
		wantID  string
		wantOK  bool
		status  string
		jenisID int
		dateStr string
		summary string
	}{
		{
			name:    "PBI Berlaku",
			idx:     0,
			wantID:  "1295",
			wantOK:  true,
			status:  "Berlaku",
			jenisID: 1,
			dateStr: "24/12/2025",
			summary: "Pengaturan Industri Sistem Pembayaran",
		},
		{
			name:    "PBI Tidak Berlaku",
			idx:     1,
			wantID:  "461",
			wantOK:  true,
			status:  "Tidak Berlaku",
			jenisID: 1,
			dateStr: "29/12/2020",
			summary: "Sistem Pembayaran",
		},
		{
			name:    "PADG card",
			idx:     2,
			wantID:  "537",
			wantOK:  true,
			status:  "Berlaku",
			jenisID: 2,
			dateStr: "10/10/2024",
			summary: "Perubahan Atas Peraturan Anggota Dewan Gubernur Nomor 23/16/PADG/2021",
		},
		{
			name:    "SE Ekstern (out of scope)",
			idx:     3,
			wantID:  "9999",
			wantOK:  true,
			status:  "Berlaku",
			jenisID: 3,
			dateStr: "15/01/2025",
			summary: "Some SE Ekstern regulation",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, ok := parseCard(cards[tt.idx][1])
			if ok != tt.wantOK {
				t.Fatalf("parseCard ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if c.id != tt.wantID {
				t.Errorf("id = %q, want %q", c.id, tt.wantID)
			}
			if c.status != tt.status {
				t.Errorf("status = %q, want %q", c.status, tt.status)
			}
			if c.jenisID != tt.jenisID {
				t.Errorf("jenisID = %d, want %d", c.jenisID, tt.jenisID)
			}
			wantDate, _ := time.Parse("02/01/2006", tt.dateStr)
			if !c.date.Equal(wantDate) {
				t.Errorf("date = %v, want %v", c.date, wantDate)
			}
			if c.summary != tt.summary {
				t.Errorf("summary = %q, want %q", c.summary, tt.summary)
			}
		})
	}
}

func TestParseCardFiltersScope(t *testing.T) {
	html, err := os.ReadFile("testdata/listing_minimal.html")
	if err != nil {
		t.Fatalf("read listing fixture: %v", err)
	}

	cards := cardRe.FindAllStringSubmatch(string(html), -1)
	var inScope int
	for _, m := range cards {
		c, ok := parseCard(m[1])
		if !ok {
			continue
		}
		if c.jenisID == jenisPeraturanPBI || c.jenisID == jenisPeraturanPADG {
			inScope++
		}
	}

	// 3 in scope (2 PBI + 1 PADG), 1 SE Ekstern filtered out.
	if inScope != 3 {
		t.Errorf("in-scope cards = %d, want 3", inScope)
	}
}

func TestParseCardIncrementalWatermark(t *testing.T) {
	html, err := os.ReadFile("testdata/listing_minimal.html")
	if err != nil {
		t.Fatalf("read listing fixture: %v", err)
	}

	cards := cardRe.FindAllStringSubmatch(string(html), -1)
	// Watermark: 2024-12-01 — should exclude the 2020 card, keep 2025 and 2024-10-10.
	since := time.Date(2024, 12, 1, 0, 0, 0, 0, time.UTC)

	var after int
	for _, m := range cards {
		c, ok := parseCard(m[1])
		if !ok {
			continue
		}
		if c.jenisID != jenisPeraturanPBI && c.jenisID != jenisPeraturanPADG {
			continue
		}
		if !c.date.IsZero() && c.date.After(since) {
			after++
		}
	}

	// Only the 2025-12-24 PBI card should pass the watermark.
	if after != 1 {
		t.Errorf("cards after watermark = %d, want 1", after)
	}
}

// --- API detail mapping tests ---

func TestMapDetailPBI1295(t *testing.T) {
	raw, err := os.ReadFile("testdata/api_detail_1295.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	var resp apiResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	var d apiDetail
	if err := json.Unmarshal(resp.Data, &d); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}

	doc := mapDetail("1295", &d, resp.Data)

	if doc.ExternalID != "1295" {
		t.Errorf("ExternalID = %q, want 1295", doc.ExternalID)
	}
	if doc.Number != "PBI 10/2025" {
		t.Errorf("Number = %q", doc.Number)
	}
	if doc.Title != "Pengaturan Industri Sistem Pembayaran" {
		t.Errorf("Title = %q", doc.Title)
	}
	if doc.DocType != "pbi" {
		t.Errorf("DocType = %q, want Peraturan Bank Indonesia", doc.DocType)
	}
	if doc.Status != "Berlaku" {
		t.Errorf("Status = %q, want Berlaku", doc.Status)
	}
	if doc.Issuer != "BANK INDONESIA" {
		t.Errorf("Issuer = %q", doc.Issuer)
	}

	// Dates.
	wantIssued := time.Date(2025, 12, 24, 0, 0, 0, 0, time.UTC)
	if !doc.IssuedAt.Equal(wantIssued) {
		t.Errorf("IssuedAt = %v, want %v", doc.IssuedAt, wantIssued)
	}
	wantEffective := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	if !doc.EffectiveAt.Equal(wantEffective) {
		t.Errorf("EffectiveAt = %v, want %v", doc.EffectiveAt, wantEffective)
	}

	// Relations: Mencabut = "22/23/PBI/2020;" — one forward revoke relation.
	if len(doc.Relations) != 1 {
		t.Fatalf("Relations len = %d, want 1", len(doc.Relations))
	}
	if doc.Relations[0].Type != "Mencabut" {
		t.Errorf("Relation type = %q, want Mencabut", doc.Relations[0].Type)
	}
	if doc.Relations[0].TargetNumber != "22/23/PBI/2020" {
		t.Errorf("Relation target = %q, want 22/23/PBI/2020", doc.Relations[0].TargetNumber)
	}

	// Files: one PDF.
	if len(doc.Files) != 1 {
		t.Fatalf("Files len = %d, want 1", len(doc.Files))
	}
	if doc.Files[0].Kind != "main" {
		t.Errorf("File kind = %q, want main", doc.Files[0].Kind)
	}
	if doc.Files[0].Ext != "pdf" {
		t.Errorf("File ext = %q, want pdf", doc.Files[0].Ext)
	}

	// RawMeta should be the full Data JSON.
	if len(doc.RawMeta) == 0 {
		t.Error("RawMeta is empty")
	}
}

func TestMapDetailPADG537NullPengundangan(t *testing.T) {
	raw, err := os.ReadFile("testdata/api_detail_537.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	var resp apiResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	var d apiDetail
	if err := json.Unmarshal(resp.Data, &d); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}

	// TanggalPengundangan is null for this PADG — must not crash.
	if d.TanggalPengundangan != nil {
		t.Errorf("TanggalPengundangan should be nil, got %q", *d.TanggalPengundangan)
	}

	doc := mapDetail("537", &d, resp.Data)

	if doc.DocType != "padg" {
		t.Errorf("DocType = %q, want padg", doc.DocType)
	}
	if doc.Number != "PADG 15/2024" {
		t.Errorf("Number = %q", doc.Number)
	}

	// Status should be "Tidak Berlaku" (this one is correctly marked).
	if doc.Status != "Tidak Berlaku" {
		t.Errorf("Status = %q, want Tidak Berlaku", doc.Status)
	}

	// Subjek is null.
	// Forward relations: Mengubah = "23/16/PADG/2021;" — one amends relation.
	hasAmends := false
	for _, r := range doc.Relations {
		if r.Type == "Mengubah" && r.TargetNumber == "23/16/PADG/2021" {
			hasAmends = true
		}
	}
	if !hasAmends {
		t.Errorf("expected Mengubah relation for 23/16/PADG/2021, got %v", doc.Relations)
	}
}

func TestMapDetailStatusCaveat461(t *testing.T) {
	// PeraturanID 461 = PBI 22/23/2020 is named in 1295's Mencabut yet self-reports
	// Berlaku. This test confirms we capture the API status as-is (the listing
	// badge status, captured during Discover, is the more reliable source).
	raw, err := os.ReadFile("testdata/api_detail_461.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	var resp apiResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	var d apiDetail
	if err := json.Unmarshal(resp.Data, &d); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}

	doc := mapDetail("461", &d, resp.Data)

	// API reports Berlaku even though this is revoked.
	if doc.Status != "Berlaku" {
		t.Errorf("Status = %q, want Berlaku (the API incorrectly self-reports)", doc.Status)
	}

	// No forward relations (Mengubah and Mencabut are both empty).
	if len(doc.Relations) != 0 {
		t.Errorf("expected 0 relations (reverse only), got %d: %v", len(doc.Relations), doc.Relations)
	}

	// Reverse fields should be empty for this doc (both Diubah and Dicabut are "").
	// PeraturanTerkait has values but is NOT emitted as a Relation.
}

func TestMapDetailPBI131Relations(t *testing.T) {
	raw, err := os.ReadFile("testdata/api_detail_131.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	var resp apiResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	var d apiDetail
	if err := json.Unmarshal(resp.Data, &d); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}

	doc := mapDetail("131", &d, resp.Data)

	// Mencabut = "18/9/PBI/2016;" — one revoke.
	if len(doc.Relations) != 1 {
		t.Fatalf("Relations len = %d, want 1", len(doc.Relations))
	}
	if doc.Relations[0].Type != "Mencabut" {
		t.Errorf("type = %q", doc.Relations[0].Type)
	}
	if doc.Relations[0].TargetNumber != "18/9/PBI/2016" {
		t.Errorf("target = %q", doc.Relations[0].TargetNumber)
	}
}

// --- Relation splitting tests ---

func TestSplitRelationField(t *testing.T) {
	tests := []struct {
		name  string
		input *string
		want  []string
	}{
		{"nil", nil, nil},
		{"empty", strPtr(""), nil},
		{"single trailing semicolon", strPtr("22/23/PBI/2020;"), []string{"22/23/PBI/2020"}},
		{"multiple", strPtr("22/23/PBI/2020;18/9/PBI/2016;"), []string{"22/23/PBI/2020", "18/9/PBI/2016"}},
		{"no trailing semicolon", strPtr("22/23/PBI/2020"), []string{"22/23/PBI/2020"}},
		{"empty segments", strPtr(";22/23/PBI/2020;;"), []string{"22/23/PBI/2020"}},
		{"whitespace", strPtr(" 22/23/PBI/2020 ; 18/9/PBI/2016 ; "), []string{"22/23/PBI/2020", "18/9/PBI/2016"}},
		{"uu refs", strPtr("UU No.23 Tahun 1999;UU No.4 Tahun 2023;UU No.3 Tahun 2011;"), []string{
			"UU No.23 Tahun 1999", "UU No.4 Tahun 2023", "UU No.3 Tahun 2011",
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitRelationField(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d: %v", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// --- Number normalization tests ---

func TestNormalizeNumber(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// PBI short form → BPK format.
		{"PBI No.10 Tahun 2025", "PBI 10/2025"},
		{"PBI NO.4 TAHUN 2025", "PBI 4/2025"},
		{"PBI No. 1 Tahun 2026", "PBI 1/2026"},
		// PBI "Nomor" form → BPK format.
		{"PBI Nomor 5 Tahun 2026", "PBI 5/2026"},
		// PBI old slash form → prefixed.
		{"22/23/PBI/2020", "PBI 22/23/PBI/2020"},
		// PADG short form → BPK format.
		{"  PADG  NO.15   TAHUN  2024  ", "PADG 15/2024"},
		{"PADG No.2 Tahun 2025", "PADG 2/2025"},
		// PADG "Nomor" form → BPK format.
		{"PADG Nomor 4 Tahun 2026", "PADG 4/2026"},
		// PADG old slash form → prefixed.
		{"22/24/PADG/2020", "PADG 22/24/PADG/2020"},
		// Already canonical or empty — pass through.
		{"PBI 5/2026", "PBI 5/2026"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeNumber(tt.input)
			if got != tt.want {
				t.Errorf("normalizeNumber(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// --- Date parsing tests ---

func TestParseDate(t *testing.T) {
	tests := []struct {
		name  string
		input *string
		want  time.Time
	}{
		{"nil", nil, time.Time{}},
		{"empty", strPtr(""), time.Time{}},
		{"iso datetime", strPtr("2025-12-24T00:00:00"), time.Date(2025, 12, 24, 0, 0, 0, 0, time.UTC)},
		{"iso date only", strPtr("2025-12-24"), time.Date(2025, 12, 24, 0, 0, 0, 0, time.UTC)},
		{"garbage", strPtr("not-a-date"), time.Time{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDate(tt.input)
			if !got.Equal(tt.want) {
				t.Errorf("parseDate = %v, want %v", got, tt.want)
			}
		})
	}
}

// --- URL builders ---

func TestURLBuilders(t *testing.T) {
	if got := detailURL("1295"); got != "https://jdih.bi.go.id/Web/DaftarPeraturan/Detail/1295" {
		t.Errorf("detailURL = %q", got)
	}
	if got := apiDetailURL("1295"); got != "https://jdih.bi.go.id/api/WebJDIH/GetDataWebPeraturan?PeraturanID=1295" {
		t.Errorf("apiDetailURL = %q", got)
	}
	if got := downloadURL("1295"); got != "https://jdih.bi.go.id/api/WebJDIH/DownloadFilePeraturan/1295" {
		t.Errorf("downloadURL = %q", got)
	}
}

// --- DocType mapping ---

func TestDocTypeFromJenis(t *testing.T) {
	tests := []struct {
		jenis int
		want  ingest.DocType
	}{
		{1, "pbi"},
		{2, "padg"},
		{3, "BI_3"},
		{99, "BI_99"},
	}

	for _, tt := range tests {
		got := docTypeFromJenis(tt.jenis)
		if got != tt.want {
			t.Errorf("docTypeFromJenis(%d) = %q, want %q", tt.jenis, got, tt.want)
		}
	}
}

// --- toDiscoveredDoc ---

func TestToDiscoveredDoc(t *testing.T) {
	c := parsedCard{
		id:      "1295",
		title:   "PBI No.10 Tahun 2025",
		summary: "Pengaturan Industri Sistem Pembayaran",
		status:  "Berlaku",
		jenisID: 1,
		date:    time.Date(2025, 12, 24, 0, 0, 0, 0, time.UTC),
	}

	doc := c.toDiscoveredDoc()

	if doc.SourceID != "bi" {
		t.Errorf("SourceID = %q", doc.SourceID)
	}
	if doc.ExternalID != "1295" {
		t.Errorf("ExternalID = %q", doc.ExternalID)
	}
	if doc.Number != "PBI No.10 Tahun 2025" {
		t.Errorf("Number = %q", doc.Number)
	}
	if doc.Title != "Pengaturan Industri Sistem Pembayaran" {
		t.Errorf("Title = %q", doc.Title)
	}
	if doc.DocType != "pbi" {
		t.Errorf("DocType = %q", doc.DocType)
	}
	if doc.Status != "Berlaku" {
		t.Errorf("Status = %q", doc.Status)
	}
	if doc.DetailURL != "https://jdih.bi.go.id/Web/DaftarPeraturan/Detail/1295" {
		t.Errorf("DetailURL = %q", doc.DetailURL)
	}
	if !doc.PublishedAt.Equal(c.date) {
		t.Errorf("PublishedAt = %v, want %v", doc.PublishedAt, c.date)
	}
}

func strPtr(s string) *string { return &s }
