package bpk

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"danny.vn/banhmi/pkg/fetch"
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

func TestParseListing(t *testing.T) {
	body := readTestdata(t, "listing.html")
	docs := parseListing(body, "pojk")
	if len(docs) != 3 {
		t.Fatalf("docs = %d, want 3", len(docs))
	}

	// First card: POJK 5/2026, no relations.
	d := docs[0]
	if d.ExternalID != "350261" {
		t.Fatalf("d[0] external id = %q, want 350261", d.ExternalID)
	}
	if d.Number != "POJK 5/2026" {
		t.Fatalf("d[0] number = %q, want POJK 5/2026", d.Number)
	}
	if d.Title != "Penyelenggaraan Kegiatan Usaha Manajer Investasi" {
		t.Fatalf("d[0] title = %q", d.Title)
	}
	if d.DetailURL != "https://peraturan.bpk.go.id/Details/350261/peraturan-ojk-no-5-tahun-2026" {
		t.Fatalf("d[0] detail url = %q", d.DetailURL)
	}
	if string(d.DocType) != "pojk" {
		t.Fatalf("d[0] doc type = %q, want pojk", d.DocType)
	}
	if len(d.Files) != 1 {
		t.Fatalf("d[0] files = %d, want 1", len(d.Files))
	}
	if d.Files[0].URL != "https://peraturan.bpk.go.id/Download/413974/POJK%205%20Tahun%202026.pdf" {
		t.Fatalf("d[0] file url = %q", d.Files[0].URL)
	}
	if d.Files[0].Name != "POJK 5 Tahun 2026.pdf" {
		t.Fatalf("d[0] file name = %q", d.Files[0].Name)
	}
	if d.Files[0].Ext != "pdf" || d.Files[0].Kind != "main" {
		t.Fatalf("d[0] file ext/kind = %q/%q", d.Files[0].Ext, d.Files[0].Kind)
	}
	if want := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC); !d.PublishedAt.Equal(want) {
		t.Fatalf("d[0] published at = %v, want %v (year-granularity watermark)", d.PublishedAt, want)
	}
	if len(d.Relations) != 0 {
		t.Fatalf("d[0] relations = %d, want 0", len(d.Relations))
	}

	// Second card: UU 27/2022, no relations.
	d = docs[1]
	if d.ExternalID != "229798" {
		t.Fatalf("d[1] external id = %q, want 229798", d.ExternalID)
	}
	// Note: the listing passes docType "pojk" since parseListing is called with
	// that type; in production each jenis maps to its own type. Here we test the
	// Number extraction with a UU-style line but pojk docType parameter.
	if d.Title != "Pelindungan Data Pribadi" {
		t.Fatalf("d[1] title = %q", d.Title)
	}
	if d.Number != "POJK 27/2022" {
		// The number uses the docType passed in (pojk); in production UU listing
		// would pass "uu" and produce "UU 27/2022".
		t.Fatalf("d[1] number = %q, want POJK 27/2022", d.Number)
	}

	// Third card: POJK 1/2026, WITH inline relations.
	d = docs[2]
	if d.ExternalID != "347285" {
		t.Fatalf("d[2] external id = %q, want 347285", d.ExternalID)
	}
	if d.Title != "Pemanfaatan Tenaga Kerja Asing" {
		t.Fatalf("d[2] title = %q", d.Title)
	}
	if len(d.Relations) != 1 {
		t.Fatalf("d[2] relations = %d, want 1", len(d.Relations))
	}
	rel := d.Relations[0]
	if rel.Type != "Mencabut" {
		t.Fatalf("relation type = %q, want Mencabut", rel.Type)
	}
	if rel.TargetID != "129691" {
		t.Fatalf("relation target id = %q, want 129691", rel.TargetID)
	}
	if rel.TargetNumber != "Peraturan OJK No. 37/POJK.03/2017 Tahun 2017" {
		t.Fatalf("relation target number = %q", rel.TargetNumber)
	}
	if rel.TargetTitle != "Pemanfaatan Tenaga Kerja Asing di Sektor Perbankan" {
		t.Fatalf("relation target title = %q", rel.TargetTitle)
	}
}

func TestParseListingDocTypeMapping(t *testing.T) {
	body := readTestdata(t, "listing.html")

	tests := []struct {
		docType ingest.DocType
		want    string
	}{
		{"uu", "UU 5/2026"},
		{"pp", "PP 5/2026"},
		{"pojk", "POJK 5/2026"},
		{"seojk", "SEOJK 5/2026"},
	}
	for _, tt := range tests {
		t.Run(string(tt.docType), func(t *testing.T) {
			docs := parseListing(body, tt.docType)
			if len(docs) == 0 {
				t.Fatal("no docs parsed")
			}
			if docs[0].Number != tt.want {
				t.Fatalf("number = %q, want %q", docs[0].Number, tt.want)
			}
		})
	}
}

func TestParseTotalCount(t *testing.T) {
	body := readTestdata(t, "listing.html")
	if got := parseTotalCount(body); got != 503 {
		t.Fatalf("total count = %d, want 503", got)
	}
}

func TestParseLastPage(t *testing.T) {
	body := readTestdata(t, "listing.html")
	if got := parseLastPage(body); got != 51 {
		t.Fatalf("last page = %d, want 51", got)
	}
}

func TestParseTotalCountMissing(t *testing.T) {
	if got := parseTotalCount("<p>nothing here</p>"); got != 0 {
		t.Fatalf("total count = %d, want 0", got)
	}
}

func TestParseLastPageMissing(t *testing.T) {
	if got := parseLastPage("<p>no pagination</p>"); got != 0 {
		t.Fatalf("last page = %d, want 0", got)
	}
}

// --- Detail parse tests ---

func TestParseDetail(t *testing.T) {
	body := readTestdata(t, "detail.html")
	d, err := parseDetail(body, "229798", "https://peraturan.bpk.go.id/Details/229798/uu-no-27-tahun-2022")
	if err != nil {
		t.Fatalf("parseDetail: %v", err)
	}

	if d.SourceID != "bpk" {
		t.Fatalf("source id = %q, want bpk", d.SourceID)
	}
	if d.ExternalID != "229798" {
		t.Fatalf("external id = %q, want 229798", d.ExternalID)
	}

	// Metadata fields.
	if d.Title != "Undang-undang (UU) Nomor 27 Tahun 2022 tentang Pelindungan Data Pribadi" {
		t.Fatalf("title = %q", d.Title)
	}
	if d.Issuer != "Indonesia, Pemerintah Pusat" {
		t.Fatalf("issuer = %q", d.Issuer)
	}
	if string(d.DocType) != "uu" {
		t.Fatalf("doc type = %q, want uu", d.DocType)
	}
	if d.DocTypeCode != "UU" {
		t.Fatalf("doc type code = %q, want UU", d.DocTypeCode)
	}
	if d.Status != "Berlaku" {
		t.Fatalf("status = %q, want Berlaku", d.Status)
	}

	// Number from header — normalized through parseNumber to match listing path.
	if d.Number != "UU 27/2022" {
		t.Fatalf("number = %q, want UU 27/2022", d.Number)
	}

	// Dates.
	wantIssued := time.Date(2022, 10, 17, 0, 0, 0, 0, time.UTC)
	if d.IssuedAt != wantIssued {
		t.Fatalf("issued at = %v, want %v", d.IssuedAt, wantIssued)
	}
	wantEffective := time.Date(2024, 10, 17, 0, 0, 0, 0, time.UTC)
	if d.EffectiveAt != wantEffective {
		t.Fatalf("effective at = %v, want %v", d.EffectiveAt, wantEffective)
	}

	// Subjek + MATERI POKOK combined in Abstract; HTML must stay empty — the
	// extract cascade treats HTML as inline law body, and the BPK detail page
	// never carries the law text.
	if !strings.HasPrefix(d.Abstract, "HAK ASASI MANUSIA - TELEKOMUNIKASI, INFORMATIKA, SIBER, DAN INTERNET") {
		t.Fatalf("abstract = %q", d.Abstract)
	}
	if !strings.Contains(d.Abstract, " — ") {
		t.Fatalf("abstract missing materi pokok suffix: %q", d.Abstract)
	}
	if d.HTML != "" {
		t.Fatalf("html must be empty (abstract shadowing the PDF), got %q", d.HTML[:min(len(d.HTML), 60)])
	}

	// Files.
	if len(d.Files) != 1 {
		t.Fatalf("files = %d, want 1", len(d.Files))
	}
	if d.Files[0].URL != "https://peraturan.bpk.go.id/Download/224884/UU%20Nomor%2027%20Tahun%202022.pdf" {
		t.Fatalf("file url = %q", d.Files[0].URL)
	}
	if d.Files[0].Name != "UU Nomor 27 Tahun 2022.pdf" {
		t.Fatalf("file name = %q", d.Files[0].Name)
	}

	// STATUS PERATURAN: "Belum Tersedia" → no relations.
	if len(d.Relations) != 0 {
		t.Fatalf("relations = %d, want 0 (Belum Tersedia)", len(d.Relations))
	}

	// RawMeta contains parsed metadata.
	if d.RawMeta == nil {
		t.Fatal("raw meta is nil")
	}
}

func TestParseDetailWithRelations(t *testing.T) {
	body := readTestdata(t, "detail_with_relations.html")
	d, err := parseDetail(body, "302684", "https://peraturan.bpk.go.id/Details/302684/peraturan-ojk-no-21-tahun-2023")
	if err != nil {
		t.Fatalf("parseDetail: %v", err)
	}

	// DocType and Number must match the listing path's format so doc_key dedup
	// merges both paths into one silver document.
	if string(d.DocType) != "pojk" {
		t.Fatalf("doc type = %q, want pojk", d.DocType)
	}
	if d.Number != "POJK 21/2023" {
		t.Fatalf("number = %q, want POJK 21/2023", d.Number)
	}

	if d.Status != "Berlaku" {
		t.Fatalf("status = %q, want Berlaku", d.Status)
	}

	// Dates.
	wantIssued := time.Date(2023, 12, 19, 0, 0, 0, 0, time.UTC)
	if d.IssuedAt != wantIssued {
		t.Fatalf("issued at = %v, want %v", d.IssuedAt, wantIssued)
	}
	wantEffective := time.Date(2023, 12, 22, 0, 0, 0, 0, time.UTC)
	if d.EffectiveAt != wantEffective {
		t.Fatalf("effective at = %v, want %v", d.EffectiveAt, wantEffective)
	}

	// Files.
	if len(d.Files) != 1 {
		t.Fatalf("files = %d, want 1", len(d.Files))
	}
	if d.Files[0].Name != "POJK 21 Tahun 2023.pdf" {
		t.Fatalf("file name = %q", d.Files[0].Name)
	}

	// Relations (STATUS PERATURAN with Mencabut).
	if len(d.Relations) != 1 {
		t.Fatalf("relations = %d, want 1", len(d.Relations))
	}
	rel := d.Relations[0]
	if rel.Type != "Mencabut" {
		t.Fatalf("relation type = %q, want Mencabut", rel.Type)
	}
	if rel.TargetID != "128000" {
		t.Fatalf("relation target id = %q, want 128000", rel.TargetID)
	}
	if rel.TargetNumber != "Peraturan OJK No. 12/POJK.03/2018 Tahun 2018" {
		t.Fatalf("relation target number = %q", rel.TargetNumber)
	}
	if rel.TargetTitle != "Penyelenggaraan Layanan Perbankan Digital oleh Bank Umum" {
		t.Fatalf("relation target title = %q", rel.TargetTitle)
	}
}

// --- Detail/listing alignment ---

// TestDetailListingNumberAlignment verifies that parseDetail normalizes Number
// and DocType to the same short format used by the listing path, preventing
// duplicate silver documents from divergent doc_keys.
func TestDetailListingNumberAlignment(t *testing.T) {
	// UU fixture: listing would produce DocType "uu", Number "UU 27/2022".
	body := readTestdata(t, "detail.html")
	d, err := parseDetail(body, "229798", "https://peraturan.bpk.go.id/Details/229798/uu-no-27-tahun-2022")
	if err != nil {
		t.Fatalf("parseDetail: %v", err)
	}
	if d.DocType != "uu" {
		t.Fatalf("DocType = %q, want uu (short code matching listing)", d.DocType)
	}
	if d.Number != "UU 27/2022" {
		t.Fatalf("Number = %q, want UU 27/2022 (matching listing parseNumber)", d.Number)
	}

	// POJK fixture: listing would produce DocType "pojk", Number "POJK 21/2023".
	body = readTestdata(t, "detail_with_relations.html")
	d, err = parseDetail(body, "302684", "https://peraturan.bpk.go.id/Details/302684/peraturan-ojk-no-21-tahun-2023")
	if err != nil {
		t.Fatalf("parseDetail: %v", err)
	}
	if d.DocType != "pojk" {
		t.Fatalf("DocType = %q, want pojk (short code matching listing)", d.DocType)
	}
	if d.Number != "POJK 21/2023" {
		t.Fatalf("Number = %q, want POJK 21/2023 (matching listing parseNumber)", d.Number)
	}
}

// TestDetailFallbackNumber verifies the metadata-only fallback when the header
// is absent produces the short-code format (e.g. "POJK 21/2023" not
// "Peraturan OJK 21/2023").
func TestDetailFallbackNumber(t *testing.T) {
	// Minimal detail page with metadata but no header h4.
	body := `<html><body>
		<div class="col-lg-3 fw-bold">Bentuk</div>
		<div class="col-lg-9">Peraturan Otoritas Jasa Keuangan</div>
		<div class="col-lg-3 fw-bold">Bentuk Singkat</div>
		<div class="col-lg-9">Peraturan OJK</div>
		<div class="col-lg-3 fw-bold">Nomor</div>
		<div class="col-lg-9">21</div>
		<div class="col-lg-3 fw-bold">Tahun</div>
		<div class="col-lg-9">2023</div>
	</body></html>`
	d, err := parseDetail(body, "999", "https://peraturan.bpk.go.id/Details/999/test")
	if err != nil {
		t.Fatalf("parseDetail: %v", err)
	}
	if d.DocType != "pojk" {
		t.Fatalf("DocType = %q, want pojk", d.DocType)
	}
	if d.Number != "POJK 21/2023" {
		t.Fatalf("Number = %q, want POJK 21/2023", d.Number)
	}
}

// --- Indonesian date parsing tests ---

func TestParseIndonesianDate(t *testing.T) {
	tests := []struct {
		input string
		want  time.Time
	}{
		{"17 Oktober 2022", time.Date(2022, 10, 17, 0, 0, 0, 0, time.UTC)},
		{"1 Januari 2020", time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"28 Februari 2024", time.Date(2024, 2, 28, 0, 0, 0, 0, time.UTC)},
		{"15 Maret 2021", time.Date(2021, 3, 15, 0, 0, 0, 0, time.UTC)},
		{"10 April 2026", time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)},
		{"5 Mei 2019", time.Date(2019, 5, 5, 0, 0, 0, 0, time.UTC)},
		{"20 Juni 2023", time.Date(2023, 6, 20, 0, 0, 0, 0, time.UTC)},
		{"6 Juli 2022", time.Date(2022, 7, 6, 0, 0, 0, 0, time.UTC)},
		{"31 Agustus 2025", time.Date(2025, 8, 31, 0, 0, 0, 0, time.UTC)},
		{"22 September 2018", time.Date(2018, 9, 22, 0, 0, 0, 0, time.UTC)},
		{"17 November 2016", time.Date(2016, 11, 17, 0, 0, 0, 0, time.UTC)},
		{"25 Desember 2023", time.Date(2023, 12, 25, 0, 0, 0, 0, time.UTC)},
		// Edge cases.
		{"", time.Time{}},
		{"not a date", time.Time{}},
		{"17 Invalidmonth 2022", time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseIndonesianDate(tt.input)
			if got != tt.want {
				t.Fatalf("parseIndonesianDate(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// --- Jenis → DocType mapping ---

func TestJenisCodeMapping(t *testing.T) {
	tests := []struct {
		jenis int
		want  ingest.DocType
	}{
		{8, "uu"},
		{10, "pp"},
		{80, "pojk"},
		{212, "seojk"},
	}
	for _, tt := range tests {
		t.Run(string(tt.want), func(t *testing.T) {
			got, ok := jenisCode[tt.jenis]
			if !ok {
				t.Fatalf("jenis %d not in jenisCode", tt.jenis)
			}
			if got != tt.want {
				t.Fatalf("jenisCode[%d] = %q, want %q", tt.jenis, got, tt.want)
			}
		})
	}
	// PBI (78) is a fallback source — BI is primary but older PBI PDFs 404.
	if got, ok := jenisCode[78]; !ok || got != "pbi" {
		t.Fatalf("jenisCode[78] = %q, %v; want pbi, true", got, ok)
	}
}

// --- Number parsing ---

func TestParseNumber(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		docType ingest.DocType
		want    string
	}{
		// Simple "Nomor X Tahun YYYY" form.
		{"simple POJK", "Peraturan Otoritas Jasa Keuangan Nomor 5 Tahun 2026", "pojk", "POJK 5/2026"},
		{"simple UU", "Undang-undang (UU) Nomor 27 Tahun 2022", "uu", "UU 27/2022"},
		{"simple PP", "Peraturan Pemerintah (PP) Nomor 71 Tahun 2019", "pp", "PP 71/2019"},

		// Sub-sector-coded: "Nomor X/TYPE.NN/YYYY Tahun YYYY" — year is embedded,
		// trailing Tahun is redundant. Must NOT double the year.
		{"POJK.04 sub-sector", "Peraturan Otoritas Jasa Keuangan Nomor 40/POJK.04/2024 Tahun 2024", "pojk", "POJK 40/POJK.04/2024"},
		{"POJK.03 sub-sector", "Peraturan Otoritas Jasa Keuangan Nomor 59/POJK.03/2020 Tahun 2020", "pojk", "POJK 59/POJK.03/2020"},
		{"SEOJK.03 sub-sector", "Surat Edaran Otoritas Jasa Keuangan Nomor 29/SEOJK.03/2022 Tahun 2022", "seojk", "SEOJK 29/SEOJK.03/2022"},
		{"SEOJK.07 sub-sector", "Surat Edaran Otoritas Jasa Keuangan Nomor 12/SEOJK.07/2022 Tahun 2022", "seojk", "SEOJK 12/SEOJK.07/2022"},
		{"PMK.010 sub-sector", "Peraturan Menteri Keuangan (PMK) Nomor 197/PMK.010/2018 Tahun 2018", "pmk", "PMK 197/PMK.010/2018"},

		// Sub-sector with stray space around slash ("34 /POJK.04/2019").
		{"space before slash", "Peraturan Otoritas Jasa Keuangan Nomor 34 /POJK.04/2019 Tahun 2019", "pojk", "POJK 34/POJK.04/2019"},

		// PBI (jenis 78): same parseNumber path, docType "pbi".
		{"simple PBI", "Peraturan Bank Indonesia Nomor 5 Tahun 2026", "pbi", "PBI 5/2026"},

		// Edge cases.
		{"empty", "", "uu", ""},
		{"no Nomor", "something without Nomor", "uu", "something without Nomor"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseNumber(tt.raw, tt.docType)
			if got != tt.want {
				t.Fatalf("parseNumber(%q, %q) = %q, want %q", tt.raw, tt.docType, got, tt.want)
			}
		})
	}
}

// --- Source ID ---

func TestSourceID(t *testing.T) {
	s := New(nil, nil)
	if s.ID() != "bpk" {
		t.Fatalf("ID() = %q, want bpk", s.ID())
	}
}

// --- File name extraction ---

func TestFileNameFromHref(t *testing.T) {
	tests := []struct {
		href, want string
	}{
		{"/Download/413974/POJK%205%20Tahun%202026.pdf", "POJK 5 Tahun 2026.pdf"},
		{"/Download/224884/UU%20Nomor%2027%20Tahun%202022.pdf", "UU Nomor 27 Tahun 2022.pdf"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.href, func(t *testing.T) {
			got := fileNameFromHref(tt.href)
			if got != tt.want {
				t.Fatalf("fileNameFromHref(%q) = %q, want %q", tt.href, got, tt.want)
			}
		})
	}
}

// --- Listing URL ---

// The URL carries jenis + page + optional tahun only. There is no keyword param:
// BPK ignores an unrecognized filter and answers with the full listing.
func TestListingURL(t *testing.T) {
	tests := []struct {
		jenis int
		page  int
		years []int
		want  string
	}{
		{80, 1, nil, "https://peraturan.bpk.go.id/Search?jenis=80&p=1"},
		{8, 3, nil, "https://peraturan.bpk.go.id/Search?jenis=8&p=3"},
		{80, 1, []int{2025, 2026}, "https://peraturan.bpk.go.id/Search?jenis=80&p=1&tahun=2025&tahun=2026"},
		{10, 2, []int{2026}, "https://peraturan.bpk.go.id/Search?jenis=10&p=2&tahun=2026"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := listingURL(tt.jenis, tt.page, tt.years)
			if got != tt.want {
				t.Fatalf("listingURL(%d, %d, %v) = %q, want %q", tt.jenis, tt.page, tt.years, got, tt.want)
			}
		})
	}
}

// --- Discovery routing (sweep-only) ---

// recordingTransport returns an empty listing page (no cards, no pagination)
// for every request and records the requested URLs.
// TestMain shrinks the listing-page retry backoff: the failure tests exercise the
// retry path, and the production 10s/20s/30s ladder would add a minute per run.
func TestMain(m *testing.M) {
	pageRetryBackoff = time.Millisecond
	os.Exit(m.Run())
}

// recordingTransport records every requested URL. Discover fans out over jenis
// concurrently, so RoundTrip is called from several goroutines — the mutex is
// required (without it the appends race and silently drop URLs).
type recordingTransport struct {
	mu   sync.Mutex
	urls []string
}

func (rt *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	rt.urls = append(rt.urls, req.URL.String())
	rt.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("<html><body>no cards</body></html>")),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func recordingSource(rt *recordingTransport) *Source {
	c := fetch.New(nil, nil) // nil minter — no WAF session in tests
	c.HTTP = &http.Client{Transport: rt}
	return New(c, nil)
}

// Discovery is sweep-only. A keyword slice is rejected outright: BPK ignores an
// unrecognized filter param and returns the FULL listing, and the pipeline skips
// scope.Match for keyword slices — so a keyword slice would enqueue every
// document in the listing as in-scope. Nothing may be fetched.
func TestDiscoverRejectsKeyword(t *testing.T) {
	rt := &recordingTransport{}
	s := recordingSource(rt)

	docs, err := s.Discover(context.Background(), time.Time{}, "perbankan")
	if err == nil {
		t.Fatal("Discover with a keyword should return an error; bpk is sweep-only")
	}
	if !strings.Contains(err.Error(), "keyword") {
		t.Fatalf("error should name the unsupported keyword contract, got: %v", err)
	}
	if len(docs) != 0 {
		t.Fatalf("docs = %d, want 0 — a rejected keyword slice must discover nothing", len(docs))
	}
	if len(rt.urls) != 0 {
		t.Fatalf("keyword discovery must not fetch anything, got %d request(s): %v", len(rt.urls), rt.urls)
	}
}

// The sweep must cover every jenis in jenisSweep — the listing set IS the scope
// decision, so a missing jenis is a silent coverage hole.
func TestDiscoverSweepCoversAllJenis(t *testing.T) {
	rt := &recordingTransport{}
	s := recordingSource(rt)

	if _, err := s.Discover(context.Background(), time.Time{}, ""); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	// Order is non-deterministic (concurrent fan-out) and the pre-warm request
	// adds challengeURL — assert on the URL set, not the order. The want set is
	// spelled out rather than derived from jenisSweep so that adding or dropping
	// a jenis fails here and has to be a deliberate edit.
	want := map[string]bool{
		"https://peraturan.bpk.go.id/Search?jenis=80":      true, // pre-warm (challengeURL)
		"https://peraturan.bpk.go.id/Search?jenis=80&p=1":  true, // POJK
		"https://peraturan.bpk.go.id/Search?jenis=212&p=1": true, // SEOJK
		"https://peraturan.bpk.go.id/Search?jenis=78&p=1":  true, // PBI (fallback)
		"https://peraturan.bpk.go.id/Search?jenis=54&p=1":  true, // BSSN
		"https://peraturan.bpk.go.id/Search?jenis=83&p=1":  true, // LPS
		"https://peraturan.bpk.go.id/Search?jenis=81&p=1":  true, // PPATK (old)
		"https://peraturan.bpk.go.id/Search?jenis=221&p=1": true, // PPATK (new)
		"https://peraturan.bpk.go.id/Search?jenis=8&p=1":   true, // UU
		"https://peraturan.bpk.go.id/Search?jenis=10&p=1":  true, // PP
		"https://peraturan.bpk.go.id/Search?jenis=11&p=1":  true, // Perpres
		"https://peraturan.bpk.go.id/Search?jenis=42&p=1":  true, // PMK
		"https://peraturan.bpk.go.id/Search?jenis=106&p=1": true, // Kominfo
		"https://peraturan.bpk.go.id/Search?jenis=278&p=1": true, // Komdigi
	}
	got := map[string]bool{}
	for _, u := range rt.urls {
		got[u] = true
	}
	for w := range want {
		if !got[w] {
			t.Errorf("missing expected URL: %s", w)
		}
	}
	for g := range got {
		if !want[g] {
			t.Errorf("unexpected URL: %s", g)
		}
	}

	// Every jenis in the sweep must map to a doc type — an unmapped jenis would
	// discover documents with an empty DocType.
	for _, j := range jenisSweep {
		if _, ok := jenisCode[j]; !ok {
			t.Errorf("jenisSweep contains %d, which has no jenisCode doc-type mapping", j)
		}
	}
}

// failingTransport returns 500 for requests matching a jenis in failJenis,
// and an empty listing page for everything else.
type failingTransport struct {
	failJenis map[int]bool
}

func (ft *failingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Check if this is a jenis listing request that should fail.
	for j := range ft.failJenis {
		if strings.Contains(req.URL.String(), "jenis="+strconv.Itoa(j)+"&") ||
			strings.HasSuffix(req.URL.String(), "jenis="+strconv.Itoa(j)) {
			return &http.Response{
				StatusCode: http.StatusInternalServerError,
				Body:       io.NopCloser(strings.NewReader("internal server error")),
				Header:     make(http.Header),
				Request:    req,
			}, nil
		}
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("<html><body>no cards</body></html>")),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func TestDiscoverPartialJenisFailureReturnsError(t *testing.T) {
	ft := &failingTransport{failJenis: map[int]bool{212: true}}
	c := fetch.New(nil, nil)
	c.HTTP = &http.Client{Transport: ft}
	s := New(c, nil)

	docs, err := s.Discover(context.Background(), time.Time{}, "")
	if err == nil {
		t.Fatal("Discover should return non-nil error when a jenis fails")
	}
	if !strings.Contains(err.Error(), "jenis 212") {
		t.Fatalf("error should mention failed jenis 212, got: %v", err)
	}
	if !strings.Contains(err.Error(), "1 of") {
		t.Fatalf("error should report failure count, got: %v", err)
	}
	// Partial docs from other (successful) jenis types are still returned.
	_ = docs
}

func TestYearWindow(t *testing.T) {
	now := time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		since time.Time
		want  []int
	}{
		{"zero since means full scan", time.Time{}, nil},
		{"same-year watermark includes previous year", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), []int{2025, 2026}},
		{"older watermark spans to now", time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC), []int{2023, 2024, 2025, 2026}},
		{"future watermark clamps to current year", time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC), []int{2026}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := yearWindow(tt.since, now)
			if len(got) != len(tt.want) {
				t.Fatalf("yearWindow(%v) = %v, want %v", tt.since, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("yearWindow(%v) = %v, want %v", tt.since, got, tt.want)
				}
			}
		})
	}
}
