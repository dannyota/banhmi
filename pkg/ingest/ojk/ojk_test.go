package ojk

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

func readTestdata(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read testdata/%s: %v", name, err)
	}
	return string(b)
}

// --- Listing parse tests ---

// TestParseListing exercises parseListRow over the real captured listing rows
// (testdata/listing.json, live 2026-07-12, trimmed to 5 representative rows).
func TestParseListing(t *testing.T) {
	var lr listResponse
	if err := json.Unmarshal([]byte(readTestdata(t, "listing.json")), &lr); err != nil {
		t.Fatalf("decode listing fixture: %v", err)
	}
	if len(lr.AaData) != 5 {
		t.Fatalf("fixture rows = %d, want 5", len(lr.AaData))
	}

	// docType is the long-form label for POJK (aligns with BPK's doc_type
	// for cross-source doc_key dedup).
	pojkType := ingest.DocType("Peraturan Otoritas Jasa Keuangan")

	tests := []struct {
		name        string
		idx         int
		externalID  string
		number      string
		title       string
		status      string
		publishedAt time.Time
	}{
		{
			name:       "classic number format",
			idx:        0,
			externalID: "e036e7ad-82e6-7ea5-d849-e5ebdb745985",
			number:     "Peraturan Otoritas Jasa Keuangan Nomor 9/POJK.04/2015 Tahun 2015",
			title:      "Pedoman Transaksi Repurchase Agreement Bagi Lembaga Jasa Keuangan",
			status:     "Berlaku",
		},
		{
			name:        "new number format with date",
			idx:         1,
			externalID:  "eaa484f3-3475-58f7-97b0-7b6b8f2938a1",
			number:      "Peraturan Otoritas Jasa Keuangan Nomor 3 Tahun 2026",
			title:       "Penyelenggaraan Kegiatan Usaha Perusahaan Efek yang Melakukan Kegiatan Usaha sebagai Penjamin Emisi Efek dan Perantara Pedagang Efek",
			status:      "Berlaku",
			publishedAt: time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC),
		},
		{
			name:        "dated 2024 row",
			idx:         2,
			externalID:  "acaff1f2-7aa3-7c21-70ec-93f899da3f96",
			number:      "Peraturan Otoritas Jasa Keuangan Nomor 47 Tahun 2024",
			title:       "Koperasi Sektor Jasa Keuangan",
			status:      "Berlaku",
			publishedAt: time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC),
		},
		{
			name:       "partially repealed",
			idx:        3,
			externalID: "f8e8afee-28b5-7808-3e45-ca6f045e144b",
			number:     "Peraturan Otoritas Jasa Keuangan Nomor 73/POJK.05/2016 Tahun 2016",
			title:      "Tata Kelola Perusahaan Yang Baik Bagi Perusahaan Perasuransian",
			status:     "Berlaku (Dicabut Sebagian)",
		},
		{
			name:       "fully repealed, title without Republik Indonesia",
			idx:        4,
			externalID: "0f1bd143-3a7d-48ad-e142-5d4266f61d75",
			number:     "Peraturan Otoritas Jasa Keuangan Nomor 8/POJK.05/2018 Tahun 2018",
			title:      "Pendanaan Dana Pensiun",
			status:     "Tidak Berlaku",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, ok := parseListRow(lr.AaData[tt.idx], "06", pojkType)
			if !ok {
				t.Fatal("parseListRow returned !ok")
			}
			if d.SourceID != "ojk" {
				t.Errorf("SourceID = %q, want ojk", d.SourceID)
			}
			if d.ExternalID != tt.externalID {
				t.Errorf("ExternalID = %q, want %q", d.ExternalID, tt.externalID)
			}
			if d.Number != tt.number {
				t.Errorf("Number = %q, want %q", d.Number, tt.number)
			}
			if d.Title != tt.title {
				t.Errorf("Title = %q, want %q", d.Title, tt.title)
			}
			if d.Status != tt.status {
				t.Errorf("Status = %q, want %q", d.Status, tt.status)
			}
			if d.DocType != pojkType {
				t.Errorf("DocType = %q, want %q", d.DocType, pojkType)
			}
			if d.DocTypeCode != "06" {
				t.Errorf("DocTypeCode = %q, want 06", d.DocTypeCode)
			}
			if !d.PublishedAt.Equal(tt.publishedAt) {
				t.Errorf("PublishedAt = %v, want %v", d.PublishedAt, tt.publishedAt)
			}
			wantURL := baseURL + "/Web/ViewPeraturan/Detail/" + tt.externalID + "//06"
			if d.DetailURL != wantURL {
				t.Errorf("DetailURL = %q, want %q", d.DetailURL, wantURL)
			}
			// Files come from FetchDetail (per-file UUIDs), never from the listing.
			if len(d.Files) != 0 {
				t.Errorf("Files = %d, want 0 (files are detail-page only)", len(d.Files))
			}
		})
	}
}

func TestParseListingTotal(t *testing.T) {
	var lr listResponse
	if err := json.Unmarshal([]byte(readTestdata(t, "listing.json")), &lr); err != nil {
		t.Fatalf("decode listing fixture: %v", err)
	}
	if got := parseTotal(lr.ITotalRecords); got != 560 {
		t.Fatalf("total = %d, want 560", got)
	}
}

func TestParseListRowWatermark(t *testing.T) {
	var lr listResponse
	if err := json.Unmarshal([]byte(readTestdata(t, "listing.json")), &lr); err != nil {
		t.Fatalf("decode listing fixture: %v", err)
	}

	// Watermark 2025-01-01: the 2026 dated row passes, the 2024 dated row is
	// skipped, undated rows (zero PublishedAt) are always kept.
	since := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	var kept int
	for _, row := range lr.AaData {
		d, ok := parseListRow(row, "06", "Peraturan Otoritas Jasa Keuangan")
		if !ok {
			continue
		}
		if !since.IsZero() && !d.PublishedAt.IsZero() && !d.PublishedAt.After(since) {
			continue
		}
		kept++
	}
	if kept != 4 {
		t.Fatalf("kept = %d, want 4 (3 undated + 1 dated 2026)", kept)
	}
}

func TestParseTotal(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int
	}{
		{"live array form", "[560]", 560},
		{"bare int", "407", 407},
		{"string", `"12"`, 12},
		{"string array", `["99"]`, 99},
		{"empty", "", 0},
		{"null", "null", 0},
		{"garbage", `"abc"`, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseTotal(json.RawMessage(tt.raw)); got != tt.want {
				t.Fatalf("parseTotal(%s) = %d, want %d", tt.raw, got, tt.want)
			}
		})
	}
}

func TestSplitNumberTitle(t *testing.T) {
	tests := []struct {
		full   string
		number string
		title  string
	}{
		{
			"Peraturan Otoritas Jasa Keuangan Republik Indonesia Nomor 9/POJK.04/2015 tentang Pedoman Transaksi Repurchase Agreement Bagi Lembaga Jasa Keuangan",
			"9/POJK.04/2015",
			"Pedoman Transaksi Repurchase Agreement Bagi Lembaga Jasa Keuangan",
		},
		{
			"Peraturan Otoritas Jasa Keuangan Nomor 8/POJK.05/2018 tentang Pendanaan Dana Pensiun",
			"8/POJK.05/2018",
			"Pendanaan Dana Pensiun",
		},
		{
			"Peraturan Otoritas Jasa Keuangan Republik Indonesia Nomor 47 Tahun 2024 tentang Koperasi Sektor Jasa Keuangan",
			"47 Tahun 2024",
			"Koperasi Sektor Jasa Keuangan",
		},
		// No-space edge case: "Nomor34/POJK.05/2015tentang ..." (live data).
		{
			"Peraturan Otoritas Jasa Keuangan Nomor34/POJK.05/2015 tentang Dana Pensiun",
			"34/POJK.05/2015",
			"Dana Pensiun",
		},
		// No space before tentang.
		{
			"Peraturan Otoritas Jasa Keuangan Nomor 8/POJK.05/2018tentang Pendanaan Dana Pensiun",
			"8/POJK.05/2018",
			"Pendanaan Dana Pensiun",
		},
		{"a title without the pattern", "", "a title without the pattern"},
		{"", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.full, func(t *testing.T) {
			number, title := splitNumberTitle(tt.full)
			if number != tt.number || title != tt.title {
				t.Fatalf("splitNumberTitle(%q) = (%q, %q), want (%q, %q)", tt.full, number, title, tt.number, tt.title)
			}
		})
	}
}

func TestParseListDate(t *testing.T) {
	tests := []struct {
		input string
		want  time.Time
	}{
		{"31-12-2024", time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)},
		{"29-04-2026", time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC)},
		{"01-01-2020", time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"", time.Time{}},
		{"not-a-date", time.Time{}},
		{"32-13-2024", time.Time{}},
		{"2024-12-31", time.Time{}}, // ISO order is not the OJK format
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := parseListDate(tt.input); !got.Equal(tt.want) {
				t.Fatalf("parseListDate(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// --- Detail parse tests ---

// TestParseDetail exercises parseDetail over the real captured detail page
// (POJK 73/POJK.05/2016, partially repealed, with relations and one file).
func TestParseDetail(t *testing.T) {
	body := readTestdata(t, "detail.html")
	pageURL := baseURL + "/Web/ViewPeraturan/Detail/f8e8afee-28b5-7808-3e45-ca6f045e144b//06"
	d, err := parseDetail(body, "f8e8afee-28b5-7808-3e45-ca6f045e144b", pageURL)
	if err != nil {
		t.Fatalf("parseDetail: %v", err)
	}

	if d.SourceID != "ojk" {
		t.Errorf("SourceID = %q, want ojk", d.SourceID)
	}
	if d.ExternalID != "f8e8afee-28b5-7808-3e45-ca6f045e144b" {
		t.Errorf("ExternalID = %q", d.ExternalID)
	}
	wantNumber := "Peraturan Otoritas Jasa Keuangan Nomor 73/POJK.05/2016 Tahun 2016"
	if d.Number != wantNumber {
		t.Errorf("Number = %q, want %q", d.Number, wantNumber)
	}
	if d.Title != "Peraturan Otoritas Jasa Keuangan Republik Indonesia Nomor 73/POJK.05/2016 tentang Tata Kelola Perusahaan Yang Baik Bagi Perusahaan Perasuransian" {
		t.Errorf("Title = %q", d.Title)
	}
	if d.DocType != "Peraturan Otoritas Jasa Keuangan" {
		t.Errorf("DocType = %q, want Peraturan Otoritas Jasa Keuangan", d.DocType)
	}
	if d.DocTypeCode != "06" {
		t.Errorf("DocTypeCode = %q, want 06", d.DocTypeCode)
	}
	if d.Issuer != "Indonesia.Otoritas Jasa Keuangan" {
		t.Errorf("Issuer = %q", d.Issuer)
	}
	if d.Abstract != "Tata Kelola Perusahaan Yang Baik Bagi Perusahaan Perasuransian" {
		t.Errorf("Abstract = %q", d.Abstract)
	}

	// Status: partial repeal must stay distinguishable from full repeal.
	if d.Status != "Berlaku (Dicabut Sebagian)" {
		t.Errorf("Status = %q, want Berlaku (Dicabut Sebagian)", d.Status)
	}

	// Dates.
	if want := time.Date(2016, 12, 23, 0, 0, 0, 0, time.UTC); !d.IssuedAt.Equal(want) {
		t.Errorf("IssuedAt = %v, want %v", d.IssuedAt, want)
	}
	if want := time.Date(2016, 12, 28, 0, 0, 0, 0, time.UTC); !d.PublishedAt.Equal(want) {
		t.Errorf("PublishedAt = %v, want %v", d.PublishedAt, want)
	}
	// No "Sejak Tanggal" suffix on this page → no effective date.
	if !d.EffectiveAt.IsZero() {
		t.Errorf("EffectiveAt = %v, want zero", d.EffectiveAt)
	}

	// Files: one main PDF with a per-file UUID distinct from the doc UUID.
	if len(d.Files) != 1 {
		t.Fatalf("Files = %d, want 1", len(d.Files))
	}
	f := d.Files[0]
	if f.URL != baseURL+"/Web/ViewPeraturan/DownloadDokumen/6e542fd2-c883-807d-f51b-f28e9a513cd0" {
		t.Errorf("File URL = %q", f.URL)
	}
	if f.Name != "pojk 73-2016.pdf" {
		t.Errorf("File name = %q", f.Name)
	}
	if f.Ext != "pdf" || f.Kind != "main" {
		t.Errorf("File ext/kind = %q/%q, want pdf/main", f.Ext, f.Kind)
	}

	// Relations: Riwayat (4 Dicabut + 1 Diubah) + 2 Landasan Hukum.
	wantRels := []ingest.Relation{
		{Type: "Dicabut", TargetNumber: "POJK Nomor 4 Tahun 2021", TargetURL: baseURL + "/Web/ViewPeraturan/DownloadFileRiwayat/1286"},
		{Type: "Dicabut", TargetNumber: "POJK Nomor 23 Tahun 2023", TargetURL: baseURL + "/Web/ViewPeraturan/DownloadFileRiwayat/1294"},
		{Type: "Dicabut", TargetNumber: "POJK Nomor 24 Tahun 2023", TargetURL: baseURL + "/Web/ViewPeraturan/DownloadFileRiwayat/1295"},
		{Type: "Dicabut", TargetNumber: "POJK Nomor 8 Tahun 2024", TargetURL: baseURL + "/Web/ViewPeraturan/DownloadFileRiwayat/1547"},
		{Type: "Diubah", TargetNumber: "Peraturan Otoritas Jasa Keuangan Nomor 43/POJK.05/2019", TargetURL: baseURL + "/Web/ViewPeraturan/DownloadFileRiwayat/950"},
		{Type: "Landasan Hukum", TargetNumber: "UU Nomor 21 Tahun 2011", TargetTitle: "Otoritas Jasa Keuangan", TargetURL: baseURL + "/peraturan/peraturan/downloadfilelandasan/1603"},
		{Type: "Landasan Hukum", TargetNumber: "UU Nomor 40 Tahun 2014", TargetTitle: "Perasuransian", TargetURL: baseURL + "/peraturan/peraturan/downloadfilelandasan/1604"},
	}
	if len(d.Relations) != len(wantRels) {
		t.Fatalf("Relations = %d, want %d: %+v", len(d.Relations), len(wantRels), d.Relations)
	}
	for i, want := range wantRels {
		got := d.Relations[i]
		if got.Type != want.Type || got.TargetNumber != want.TargetNumber ||
			got.TargetTitle != want.TargetTitle || got.TargetURL != want.TargetURL {
			t.Errorf("Relations[%d] = %+v, want %+v", i, got, want)
		}
	}

	// RawMeta preserves the raw status string and the pengundangan date.
	var meta map[string]string
	if err := json.Unmarshal(d.RawMeta, &meta); err != nil {
		t.Fatalf("RawMeta decode: %v", err)
	}
	if meta["Status Peraturan"] != "Berlaku (Dicabut Sebagian)" {
		t.Errorf("RawMeta status = %q", meta["Status Peraturan"])
	}
	if meta["Tanggal Pengundangan"] != "28-12-2016" {
		t.Errorf("RawMeta pengundangan = %q", meta["Tanggal Pengundangan"])
	}
	if meta["Sumber LN"] == "" {
		t.Error("RawMeta missing Sumber LN gazette ref")
	}
}

func TestParseRiwayatBelumAda(t *testing.T) {
	// Live empty-history marker (POJK 47/2024's page shows this exact block).
	section := `<div class="box-holder"><div class="row">
		<label class="text-danger" style="font-size:16px">
			&nbsp; Keterangan Status/Riwayat Belum Ada &nbsp;
		</label></div></div>`
	if rels := parseRiwayat(section); rels != nil {
		t.Fatalf("relations = %+v, want nil for Riwayat Belum Ada", rels)
	}
	if rels := parseRiwayat(""); rels != nil {
		t.Fatalf("relations = %+v, want nil for missing section", rels)
	}
}

func TestSplitStatus(t *testing.T) {
	tests := []struct {
		raw    string
		status string
		eff    time.Time
	}{
		{"Berlaku Sejak Tanggal 31-12-2024", "Berlaku", time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)},
		{"Berlaku (Dicabut Sebagian)", "Berlaku (Dicabut Sebagian)", time.Time{}},
		{"Tidak Berlaku", "Tidak Berlaku", time.Time{}},
		{"Berlaku (Perubahan) (Diubah)", "Berlaku (Perubahan) (Diubah)", time.Time{}},
		{"", "", time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			status, eff := splitStatus(tt.raw)
			if status != tt.status || !eff.Equal(tt.eff) {
				t.Fatalf("splitStatus(%q) = (%q, %v), want (%q, %v)", tt.raw, status, eff, tt.status, tt.eff)
			}
		})
	}
}

func TestDokumenKind(t *testing.T) {
	tests := []struct {
		label string
		want  string
	}{
		{"Peraturan", "main"},
		{"", "main"}, // single unlabeled row = the regulation PDF (live POJK 73 page)
		{"Abstrak", "attachment"},
		{"FAQ", "attachment"},
		{"anything else", "attachment"},
	}
	for _, tt := range tests {
		if got := dokumenKind(tt.label); got != tt.want {
			t.Errorf("dokumenKind(%q) = %q, want %q", tt.label, got, tt.want)
		}
	}
}

// --- Source identity and URL builders ---

func TestSourceID(t *testing.T) {
	s := New(nil, nil, nil)
	if s.ID() != "ojk" {
		t.Fatalf("ID() = %q, want ojk", s.ID())
	}
}

func TestJenisMapping(t *testing.T) {
	tests := []struct {
		code string
		want ingest.DocType
	}{
		{"06", "Peraturan Otoritas Jasa Keuangan"},
		{"09", "Surat Edaran Otoritas Jasa Keuangan"},
		{"01", "UU"},
	}
	for _, tt := range tests {
		got, ok := jenisPeraturan[tt.code]
		if !ok || got != tt.want {
			t.Errorf("jenisPeraturan[%q] = %q (ok=%v), want %q", tt.code, got, ok, tt.want)
		}
	}
	if len(jenisOrder) != len(jenisPeraturan) {
		t.Fatalf("jenisOrder covers %d codes, map has %d", len(jenisOrder), len(jenisPeraturan))
	}
	for _, code := range jenisOrder {
		if _, ok := jenisPeraturan[code]; !ok {
			t.Errorf("jenisOrder code %q missing from jenisPeraturan", code)
		}
	}
}

func TestURLBuilders(t *testing.T) {
	// Live detail URLs have an empty sektor segment (double slash) — preserved.
	if got := detailURL("abc-123", "", "06"); got != "https://jdih.ojk.go.id/Web/ViewPeraturan/Detail/abc-123//06" {
		t.Errorf("detailURL = %q", got)
	}
	if got := detailURL("abc-123", "03", "06"); got != "https://jdih.ojk.go.id/Web/ViewPeraturan/Detail/abc-123/03/06" {
		t.Errorf("detailURL = %q", got)
	}
	if got := downloadURL("file-uuid"); got != "https://jdih.ojk.go.id/Web/ViewPeraturan/DownloadDokumen/file-uuid" {
		t.Errorf("downloadURL = %q", got)
	}
}

// --- Live integration test (network) ---

// TestLiveDiscoverUU fetches the smallest live listing (UU, 12 docs) end to
// end. Run with: OJK_LIVE=1 go test -run TestLiveDiscoverUU ./pkg/ingest/ojk/
// Skips cleanly in short mode, without the env gate, or when offline.
func TestLiveDiscoverUU(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live-network test in short mode")
	}
	if os.Getenv("OJK_LIVE") != "1" {
		t.Skip("set OJK_LIVE=1 to run live-network tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	s := New(nil, nil, nil)
	docs, err := s.discoverJenis(ctx, "01", time.Time{})
	if err != nil {
		t.Skipf("live listing unreachable (offline?): %v", err)
	}
	if len(docs) == 0 {
		t.Fatal("live UU listing returned 0 docs, expected ~12")
	}
	for _, d := range docs {
		if d.ExternalID == "" || d.DetailURL == "" || d.Title == "" {
			t.Fatalf("incomplete live doc: %+v", d)
		}
	}
	t.Logf("live UU docs: %d", len(docs))
}
