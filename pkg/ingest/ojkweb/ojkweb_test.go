package ojkweb

import (
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

func TestExtractASPState(t *testing.T) {
	body := readTestdata(t, "listing.html")
	state := extractASPState(body)
	if state.viewState == "" {
		t.Fatal("viewState not extracted")
	}
	if state.validation == "" {
		t.Fatal("validation not extracted")
	}
	if state.generator != "ABCD1234" {
		t.Errorf("generator = %q, want ABCD1234", state.generator)
	}
	if state.digest == "" {
		t.Fatal("digest not extracted")
	}
}

func TestParseListingPage(t *testing.T) {
	body := readTestdata(t, "listing.html")
	docs := parseListingPage(body, "PADK")

	if len(docs) != 5 {
		t.Fatalf("docs = %d, want 5 (all regulation types included)", len(docs))
	}

	tests := []struct {
		name       string
		idx        int
		number     string
		title      string
		externalID string
		detailURL  string
		abstract   string
	}{
		{
			name:       "PADK with slash-format number",
			idx:        0,
			number:     "45/PADK.06/2025",
			title:      "Ketentuan Produk Asuransi Jiwa Dan Asuransi Umum Bagi Perusahaan Perasuransian",
			externalID: "id/regulasi/Pages/PADK-45-2025.aspx",
			detailURL:  baseURL + "/id/regulasi/Pages/PADK-45-2025.aspx",
			abstract:   "PVML",
		},
		{
			name:       "PP with Tahun format",
			idx:        1,
			number:     "3 Tahun 2026",
			title:      "Peraturan Pemerintah tentang Lembaga Keuangan Mikro",
			externalID: "id/regulasi/Pages/PP-3-2026.aspx",
			detailURL:  baseURL + "/id/regulasi/Pages/PP-3-2026.aspx",
			abstract:   "Perbankan",
		},
		{
			name:       "PMK with slash-format",
			idx:        2,
			number:     "18/PMK.010/2024",
			title:      "Pajak Penghasilan atas Penghasilan dari Usaha Jasa Konstruksi",
			externalID: "id/regulasi/Pages/PMK-18-2024.aspx",
			detailURL:  baseURL + "/id/regulasi/Pages/PMK-18-2024.aspx",
			abstract:   "Pajak",
		},
		{
			// Sub-sector POJK shapes canonicalize by shape regardless of the
			// jenis docType, matching the bpk/ojk citation form (with the
			// redundant "Tahun YYYY" those sources append to POJK numbers).
			name:       "POJK with slash-format",
			idx:        3,
			number:     "Peraturan Otoritas Jasa Keuangan Nomor 9/POJK.04/2015 Tahun 2015",
			title:      "Penerapan Prinsip Keterbukaan pada Emiten",
			externalID: "id/regulasi/Pages/POJK-9-2015.aspx",
			detailURL:  baseURL + "/id/regulasi/Pages/POJK-9-2015.aspx",
			abstract:   "Pasar Modal",
		},
		{
			// SEOJK canonical form carries no "Tahun" suffix (bpk/ojk convention).
			name:       "SEOJK with slash-format",
			idx:        4,
			number:     "Surat Edaran Otoritas Jasa Keuangan Nomor 12/SEOJK.07/2022",
			title:      "Pedoman Penyelesaian Pengaduan Konsumen",
			externalID: "id/regulasi/Pages/SEOJK-12-2022.aspx",
			detailURL:  baseURL + "/id/regulasi/Pages/SEOJK-12-2022.aspx",
			abstract:   "EDPK",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := docs[tt.idx]
			if d.SourceID != SourceID {
				t.Errorf("SourceID = %q, want %q", d.SourceID, SourceID)
			}
			if d.Number != tt.number {
				t.Errorf("Number = %q, want %q", d.Number, tt.number)
			}
			if d.Title != tt.title {
				t.Errorf("Title = %q, want %q", d.Title, tt.title)
			}
			if d.ExternalID != tt.externalID {
				t.Errorf("ExternalID = %q, want %q", d.ExternalID, tt.externalID)
			}
			if d.DetailURL != tt.detailURL {
				t.Errorf("DetailURL = %q, want %q", d.DetailURL, tt.detailURL)
			}
			if d.Abstract != tt.abstract {
				t.Errorf("Abstract = %q, want %q", d.Abstract, tt.abstract)
			}
			if d.DocType != "PADK" {
				t.Errorf("DocType = %q, want PADK", d.DocType)
			}
		})
	}
}

func TestParseListingPageIncludesAll(t *testing.T) {
	body := readTestdata(t, "listing.html")
	docs := parseListingPage(body, "PADK")

	// The listing has 5 data rows (PADK, PP, PMK, POJK, SEOJK) plus 1 header.
	// All types are included (no JDIH filter).
	if len(docs) != 5 {
		t.Fatalf("docs = %d, want 5 (all types included)", len(docs))
	}
}

// The Undang-Undang jenis on ojk.go.id is noisy: the site also lists
// SEOJK/POJK sub-sector rows under it. A genuine UU number never contains
// "/", so those rows are dropped for the UU docType only.
func TestParseListingPageFiltersMisclassifiedUU(t *testing.T) {
	body := readTestdata(t, "listing.html")
	docs := parseListingPage(body, "UU")

	// Only "3 Tahun 2026" survives; the 4 slash-numbered rows are dropped.
	if len(docs) != 1 {
		t.Fatalf("docs = %d, want 1 (slash-numbered rows dropped for UU)", len(docs))
	}
	if docs[0].Number != "3 Tahun 2026" {
		t.Fatalf("number = %q, want 3 Tahun 2026", docs[0].Number)
	}
}

// webJenis must carry the full Indonesian type names for POJK and SEOJK — the
// exact doc_type the bpk and ojk sources store — or doc_key convergence breaks
// and the same regulation forks into per-source silver documents.
func TestWebJenisConvergentDocTypes(t *testing.T) {
	want := map[string]ingest.DocType{
		"Peraturan OJK":    "Peraturan Otoritas Jasa Keuangan",
		"Surat Edaran OJK": "Surat Edaran Otoritas Jasa Keuangan",
	}
	seen := map[string]bool{}
	for _, j := range webJenis {
		if w, ok := want[j.Value]; ok {
			seen[j.Value] = true
			if j.DocType != w {
				t.Errorf("webJenis[%q].DocType = %q, want %q", j.Value, j.DocType, w)
			}
		}
	}
	for v := range want {
		if !seen[v] {
			t.Errorf("webJenis missing entry for %q", v)
		}
	}
}

func TestCanonicalNumber(t *testing.T) {
	tests := []struct {
		name    string
		number  string
		docType ingest.DocType
		want    string
	}{
		// Plain shape + full-name docType (the post-2022 numbering).
		{"POJK plain", "40 Tahun 2024", pojkFullName, "Peraturan Otoritas Jasa Keuangan Nomor 40 Tahun 2024"},
		{"SEOJK plain", "5 Tahun 2025", seojkFullName, "Surat Edaran Otoritas Jasa Keuangan Nomor 5 Tahun 2025"},

		// Sub-sector shapes identify their type on their own; POJK gets the
		// redundant "Tahun YYYY" suffix bpk/ojk store, SEOJK does not.
		{"POJK sub-sector", "40/POJK.03/2019", pojkFullName, "Peraturan Otoritas Jasa Keuangan Nomor 40/POJK.03/2019 Tahun 2019"},
		{"POJK sub-sector no docType", "9/POJK.04/2015", "PADK", "Peraturan Otoritas Jasa Keuangan Nomor 9/POJK.04/2015 Tahun 2015"},
		{"SEOJK sub-sector", "29/SEOJK.03/2022", seojkFullName, "Surat Edaran Otoritas Jasa Keuangan Nomor 29/SEOJK.03/2022"},
		{"SEOJK sub-sector no docType", "12/SEOJK.07/2022", "PADK", "Surat Edaran Otoritas Jasa Keuangan Nomor 12/SEOJK.07/2022"},

		// Stray spaces around slashes tighten (real listing shapes).
		{"space before slash", "40 /POJK.05/2020", pojkFullName, "Peraturan Otoritas Jasa Keuangan Nomor 40/POJK.05/2020 Tahun 2020"},
		{"SEOJK space before slash", "29 /SEOJK.05/2016", seojkFullName, "Surat Edaran Otoritas Jasa Keuangan Nomor 29/SEOJK.05/2016"},

		// Idempotence: an already-canonical number is returned as-is.
		{"idempotent POJK", "Peraturan Otoritas Jasa Keuangan Nomor 40 Tahun 2024", pojkFullName, "Peraturan Otoritas Jasa Keuangan Nomor 40 Tahun 2024"},
		{"idempotent SEOJK", "Surat Edaran Otoritas Jasa Keuangan Nomor 29/SEOJK.03/2022", seojkFullName, "Surat Edaran Otoritas Jasa Keuangan Nomor 29/SEOJK.03/2022"},

		// Other types stay verbatim: PPBI/SEBI converge with the bi source's
		// bare form; PADK/PMK/UU/PP have no cross-source convention yet.
		{"PADK unchanged", "45/PADK.06/2025", "PADK", "45/PADK.06/2025"},
		{"PPBI unchanged", "7/ 45 /PBI/2005", "PPBI", "7/ 45 /PBI/2005"},
		{"SEBI unchanged", "15/6/DPNP", "SEBI", "15/6/DPNP"},
		{"PMK unchanged", "18/PMK.010/2024", "PMK", "18/PMK.010/2024"},
		{"UU plain unchanged", "40 Tahun 2014", "UU", "40 Tahun 2014"},
		{"empty", "", pojkFullName, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := canonicalNumber(tt.number, tt.docType); got != tt.want {
				t.Fatalf("canonicalNumber(%q, %q) = %q, want %q", tt.number, tt.docType, got, tt.want)
			}
		})
	}
}

func TestCanonicalDetailNumber(t *testing.T) {
	tests := []struct {
		name   string
		number string
		want   string
	}{
		// Sub-sector shapes self-identify and canonicalize.
		{"SEOJK sub-sector", "29/SEOJK.03/2022", "Surat Edaran Otoritas Jasa Keuangan Nomor 29/SEOJK.03/2022"},
		{"POJK sub-sector", "40/POJK.03/2019", "Peraturan Otoritas Jasa Keuangan Nomor 40/POJK.03/2019 Tahun 2019"},

		// The plain shape is type-ambiguous on a detail page: return "" so the
		// discovery-time canonical number survives the bronze COALESCE upsert.
		{"plain ambiguous", "40 Tahun 2024", ""},

		// Other types pass through verbatim.
		{"PADK verbatim", "45/PADK.06/2025", "45/PADK.06/2025"},
		{"PPBI verbatim", "7/38/PBI/2005", "7/38/PBI/2005"},
		{"already canonical", "Peraturan Otoritas Jasa Keuangan Nomor 40 Tahun 2024", "Peraturan Otoritas Jasa Keuangan Nomor 40 Tahun 2024"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := canonicalDetailNumber(tt.number); got != tt.want {
				t.Fatalf("canonicalDetailNumber(%q) = %q, want %q", tt.number, got, tt.want)
			}
		})
	}
}

func TestJenisLabelDocType(t *testing.T) {
	tests := []struct {
		label string
		want  ingest.DocType
	}{
		{"Peraturan OJK", "Peraturan Otoritas Jasa Keuangan"},
		{"Surat Edaran OJK", "Surat Edaran Otoritas Jasa Keuangan"},
		{"peraturan ojk", "Peraturan Otoritas Jasa Keuangan"}, // case-insensitive
		{"Undang-Undang", "UU"},
		{"Something Unknown", "Something Unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			if got := jenisLabelDocType(tt.label); got != tt.want {
				t.Fatalf("jenisLabelDocType(%q) = %q, want %q", tt.label, got, tt.want)
			}
		})
	}
}

func TestParsePagerTargets(t *testing.T) {
	body := readTestdata(t, "listing.html")
	targets := pagerLinkRe.FindAllStringSubmatch(body, -1)
	if len(targets) != 2 {
		t.Fatalf("pager targets = %d, want 2", len(targets))
	}
	if targets[0][1] != "ctl00$PlaceHolderMain$ctl01$DataPagerArticles$ctl01$ctl01" {
		t.Errorf("target[0] = %q", targets[0][1])
	}
	if targets[1][1] != "ctl00$PlaceHolderMain$ctl01$DataPagerArticles$ctl01$ctl02" {
		t.Errorf("target[1] = %q", targets[1][1])
	}
}

func TestParsePager(t *testing.T) {
	body := readTestdata(t, "listing.html")
	info := parsePager(body)
	if len(info.numbered) != 2 {
		t.Fatalf("numbered pages = %d, want 2", len(info.numbered))
	}
	if _, ok := info.numbered[2]; !ok {
		t.Error("page 2 not found")
	}
	if _, ok := info.numbered[3]; !ok {
		t.Error("page 3 not found")
	}
	if info.ellipsisCtl != "" {
		t.Errorf("unexpected ellipsis: %q", info.ellipsisCtl)
	}
}

func TestParseDetail(t *testing.T) {
	body := readTestdata(t, "detail.html")
	doc, err := parseDetail(body, "padk-45-2025", baseURL+"/id/regulasi/Pages/PADK-45-2025.aspx")
	if err != nil {
		t.Fatalf("parseDetail: %v", err)
	}

	if doc.Title != "Ketentuan Produk Asuransi Jiwa Dan Asuransi Umum Bagi Perusahaan Perasuransian" {
		t.Errorf("Title = %q", doc.Title)
	}
	if doc.Number != "45/PADK.06/2025" {
		t.Errorf("Number = %q, want 45/PADK.06/2025", doc.Number)
	}
	if doc.ExternalID != "padk-45-2025" {
		t.Errorf("ExternalID = %q", doc.ExternalID)
	}

	// Issue date: 6/15/2025.
	wantIssued := time.Date(2025, 6, 15, 0, 0, 0, 0, time.UTC)
	if !doc.IssuedAt.Equal(wantIssued) {
		t.Errorf("IssuedAt = %v, want %v", doc.IssuedAt, wantIssued)
	}

	// Effective date: 7/1/2025.
	wantEffective := time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC)
	if !doc.EffectiveAt.Equal(wantEffective) {
		t.Errorf("EffectiveAt = %v, want %v", doc.EffectiveAt, wantEffective)
	}

	// Sector + sub-sector.
	if doc.Abstract != "PVML; Asuransi; Produk Asuransi" {
		t.Errorf("Abstract = %q", doc.Abstract)
	}

	// Two PDF files.
	if len(doc.Files) != 2 {
		t.Fatalf("Files = %d, want 2", len(doc.Files))
	}
	if doc.Files[0].URL != baseURL+"/id/regulasi/Documents/Pages/PADK-45-2025/PADK-45-PADK.06-2025.pdf" {
		t.Errorf("File[0].URL = %q", doc.Files[0].URL)
	}
	if doc.Files[0].Ext != "pdf" {
		t.Errorf("File[0].Ext = %q", doc.Files[0].Ext)
	}
	if doc.Files[1].URL != baseURL+"/id/regulasi/Documents/Pages/PADK-45-2025/lampiran-padk-45.pdf" {
		t.Errorf("File[1].URL = %q", doc.Files[1].URL)
	}
}

func TestParseMDYDate(t *testing.T) {
	tests := []struct {
		input string
		want  time.Time
	}{
		{"12/27/2024", time.Date(2024, 12, 27, 0, 0, 0, 0, time.UTC)},
		{"1/5/2025", time.Date(2025, 1, 5, 0, 0, 0, 0, time.UTC)},
		{"6/15/2025", time.Date(2025, 6, 15, 0, 0, 0, 0, time.UTC)},
		{"", time.Time{}},
		{"not-a-date", time.Time{}},
		{"13/1/2024", time.Time{}},  // invalid month
		{"0/1/2024", time.Time{}},   // zero month
		{"2024-12-27", time.Time{}}, // wrong format
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := parseMDYDate(tt.input); !got.Equal(tt.want) {
				t.Errorf("parseMDYDate(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestCleanText(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"  hello  world  ", "hello world"},
		{"<strong>bold</strong> text", "bold text"},
		{"a &amp; b", "a & b"},
		{"no&nbsp;break", "no break"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := cleanText(tt.input); got != tt.want {
			t.Errorf("cleanText(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSourceID(t *testing.T) {
	s := New(nil, nil)
	if s.ID() != "ojkweb" {
		t.Fatalf("ID() = %q, want ojkweb", s.ID())
	}
}

func TestFileExt(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"document.pdf", "pdf"},
		{"file.DOCX", "docx"},
		{"noext", ""},
		{"multi.part.xlsx", "xlsx"},
	}
	for _, tt := range tests {
		if got := fileExt(tt.name); got != tt.want {
			t.Errorf("fileExt(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}
