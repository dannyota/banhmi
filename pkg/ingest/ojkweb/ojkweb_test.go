package ojkweb

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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

	if len(docs) != 3 {
		t.Fatalf("docs = %d, want 3 (header + JDIH-covered rows should be skipped)", len(docs))
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

func TestParseListingPageFiltersJDIH(t *testing.T) {
	body := readTestdata(t, "listing.html")
	docs := parseListingPage(body, "PADK")

	for _, d := range docs {
		if isJDIHCovered(d.Number) {
			t.Errorf("JDIH-covered doc not filtered: %q", d.Number)
		}
	}

	// The listing has 5 data rows (PADK, PP, PMK, POJK, SEOJK) plus 1 header.
	// POJK and SEOJK should be filtered, leaving 3.
	if len(docs) != 3 {
		t.Fatalf("docs = %d, want 3 (POJK and SEOJK should be filtered)", len(docs))
	}
}

func TestIsJDIHCovered(t *testing.T) {
	tests := []struct {
		number string
		want   bool
	}{
		{"9/POJK.04/2015", true},
		{"12/SEOJK.07/2022", true},
		{"45/PADK.06/2025", false},
		{"18/PMK.010/2024", false},
		{"3 Tahun 2026", false},
		{"1/POJK.03/2019", true},
	}
	for _, tt := range tests {
		t.Run(tt.number, func(t *testing.T) {
			if got := isJDIHCovered(tt.number); got != tt.want {
				t.Errorf("isJDIHCovered(%q) = %v, want %v", tt.number, got, tt.want)
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
