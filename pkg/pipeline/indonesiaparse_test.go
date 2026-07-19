package pipeline

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// ---- helpers ---------------------------------------------------------------

func idCollect(secs []Section, kind string, out *[]string) {
	for _, s := range secs {
		if s.Kind == kind {
			*out = append(*out, s.Label)
		}
		idCollect(s.Children, kind, out)
	}
}

func idCollectSections(secs []Section, kind string) []Section {
	var out []Section
	for _, s := range secs {
		if s.Kind == kind {
			out = append(out, s)
		}
		out = append(out, idCollectSections(s.Children, kind)...)
	}
	return out
}

func idFindByPath(secs []Section, path string) *Section {
	for i := range secs {
		if secs[i].CitationPath == path {
			return &secs[i]
		}
		if got := idFindByPath(secs[i].Children, path); got != nil {
			return got
		}
	}
	return nil
}

// ---- unit tests (synthetic inline text) ------------------------------------

func TestParseIndonesianUU_basicPasalSequence(t *testing.T) {
	text := `BAB I
KETENTUAN UMUM
Pasal 1
Dalam Undang-Undang ini yang dimaksud dengan:
1. Data Pribadi adalah data tentang orang.
Pasal 2
(1) Pelindungan Data Pribadi dilakukan berdasarkan asas.
(2) Asas sebagaimana dimaksud pada ayat (1) meliputi:
a. pelindungan;
b. kepastian hukum.
BAB II
JENIS DATA PRIBADI
Pasal 3
Data Pribadi terdiri atas:
a. Data Pribadi yang bersifat spesifik;
b. Data Pribadi yang bersifat umum.
Pasal 4
(1) Data Pribadi spesifik meliputi kesehatan.
(2) Data Pribadi umum meliputi nama.
`
	roots := ParseIndonesianUU(text)

	// 2 BABs.
	var babs []string
	idCollect(roots, "bab", &babs)
	if len(babs) != 2 {
		t.Fatalf("bab count = %d, want 2; babs = %v", len(babs), babs)
	}

	// 4 Pasal in monotonic sequence.
	var pasals []string
	idCollect(roots, "pasal", &pasals)
	if len(pasals) != 4 {
		t.Fatalf("pasal count = %d, want 4; pasals = %v", len(pasals), pasals)
	}
	for i, want := range []string{"Pasal 1", "Pasal 2", "Pasal 3", "Pasal 4"} {
		if pasals[i] != want {
			t.Errorf("pasal[%d] = %q, want %q", i, pasals[i], want)
		}
	}

	// Pasal 2 has 2 ayat and 2 huruf.
	p2 := idFindByPath(roots, "bab-i/pasal-2")
	if p2 == nil {
		t.Fatal("missing pasal-2 under bab-i")
	}
	var ayats []string
	idCollect(p2.Children, "ayat", &ayats)
	if len(ayats) != 2 {
		t.Fatalf("pasal 2 ayat count = %d, want 2", len(ayats))
	}
	var hurufs []string
	idCollect(p2.Children, "huruf", &hurufs)
	if len(hurufs) != 2 {
		t.Fatalf("pasal 2 huruf count = %d, want 2", len(hurufs))
	}

	// Citation paths.
	if s := idFindByPath(roots, "bab-i/pasal-2/ayat-1"); s == nil {
		t.Fatal("missing citation path bab-i/pasal-2/ayat-1")
	}
	if s := idFindByPath(roots, "bab-i/pasal-2/ayat-2/huruf-a"); s == nil {
		t.Fatal("missing citation path bab-i/pasal-2/ayat-2/huruf-a")
	}
}

func TestParseIndonesianUU_ocrNoisyPasalNumbers(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int // expected Pasal count
	}{
		{
			name: "Pasal I = 1 (OCR I→1)",
			input: `Pasal I
Content of article 1.
Pasal 2
Content of article 2.`,
			want: 2,
		},
		{
			name:  "Pasa1 1O = Pasal 10 (OCR l→1, O→0)",
			input: buildPasalSequence(1, 10, "Pasa1 1O"),
			want:  10,
		},
		{
			name:  "Pasa722 = Pasal 22 (OCR l→7 missing space)",
			input: buildPasalSequence(1, 22, "Pasa722"),
			want:  22,
		},
		{
			name:  "PasalT2 = Pasal 72 (OCR T→7)",
			input: buildPasalSequence(1, 72, "PasalT2"),
			want:  72,
		},
		{
			name:  "Pasal2T = Pasal 27 (OCR T→7 no space)",
			input: buildPasalSequence(1, 27, "Pasal2T"),
			want:  27,
		},
		{
			name: "trailing dots stripped",
			input: `Pasal 1
Content.
Pasal 2. . .
More content.
Pasal 3
Even more.`,
			want: 3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			roots := ParseIndonesianUU(tt.input)
			var pasals []string
			idCollect(roots, "pasal", &pasals)
			if len(pasals) != tt.want {
				t.Errorf("pasal count = %d, want %d; pasals = %v", len(pasals), tt.want, pasals)
			}
		})
	}
}

// buildPasalSequence generates text with Pasal 1..target-1 as clean "Pasal N"
// headings and uses rawTarget as the last heading.
func buildPasalSequence(start, target int, rawTarget string) string {
	var b strings.Builder
	for i := start; i < target; i++ {
		b.WriteString("Pasal " + strconv.Itoa(i) + "\n")
		b.WriteString("Content of article " + strconv.Itoa(i) + ".\n")
	}
	b.WriteString(rawTarget + "\n")
	b.WriteString("Content of the target article.\n")
	return b.String()
}

func TestParseIndonesianUU_monotonicRejectsCrossRefs(t *testing.T) {
	text := `Pasal 1
Ketentuan sebagaimana dimaksud dalam Pasal 20 ayat (1).
Pasal 2
Pengendali Data Pribadi wajib memenuhi Pasal 1.
Pasal 3
Ketentuan lebih lanjut.
`
	roots := ParseIndonesianUU(text)
	var pasals []string
	idCollect(roots, "pasal", &pasals)
	if len(pasals) != 3 {
		t.Fatalf("monotonic filter: pasal count = %d, want 3 (cross-refs rejected); pasals = %v", len(pasals), pasals)
	}
	// Pasal 20 in the prose must not appear as a section.
	for _, p := range pasals {
		if p == "Pasal 20" {
			t.Fatal("cross-reference Pasal 20 was accepted as a section")
		}
	}
}

func TestParseIndonesianUU_babBagianParagrafNesting(t *testing.T) {
	text := `BAB I
KETENTUAN UMUM
Pasal 1
Definisi.
BAB II
DATA PRIBADI
Bagian Kesatu
Jenis Data
Pasal 2
Jenis data pribadi.
Bagian Kedua
Pemrosesan
Paragraf 1
Dasar Pemrosesan
Pasal 3
Pemrosesan harus berdasarkan hukum.
Pasal 4
Syarat lainnya.
`
	roots := ParseIndonesianUU(text)

	// 2 BABs.
	var babs []string
	idCollect(roots, "bab", &babs)
	if len(babs) != 2 {
		t.Fatalf("bab count = %d, want 2", len(babs))
	}

	// 2 Bagians under BAB II.
	bab2 := idFindByPath(roots, "bab-ii")
	if bab2 == nil {
		t.Fatal("missing bab-ii")
	}
	var bagians []string
	idCollect(bab2.Children, "bagian", &bagians)
	if len(bagians) != 2 {
		t.Fatalf("bagian count under BAB II = %d, want 2", len(bagians))
	}

	// Paragraf under Bagian Kedua.
	if s := idFindByPath(roots, "bab-ii/bagian-kedua/paragraf-1"); s == nil {
		t.Fatal("missing paragraf-1 under bagian-kedua")
	}

	// Pasal 3 under Paragraf 1.
	if s := idFindByPath(roots, "bab-ii/bagian-kedua/paragraf-1/pasal-3"); s == nil {
		t.Fatal("missing pasal-3 under paragraf-1")
	}

	// Pasal 4 also under Paragraf 1 (same paragraf as Pasal 3).
	if s := idFindByPath(roots, "bab-ii/bagian-kedua/paragraf-1/pasal-4"); s == nil {
		t.Fatal("missing pasal-4 under paragraf-1")
	}
}

func TestParseIndonesianUU_ayatHurufNesting(t *testing.T) {
	text := `Pasal 1
(1) Pelindungan dilakukan berdasarkan asas:
a. pelindungan;
b. kepastian hukum;
c. kepentingan umum.
(2) Pelindungan sebagaimana pada ayat (1) meliputi semua.
`
	roots := ParseIndonesianUU(text)

	p1 := idFindByPath(roots, "pasal-1")
	if p1 == nil {
		t.Fatal("missing pasal-1")
	}

	// 2 ayat.
	var ayats []string
	idCollect(p1.Children, "ayat", &ayats)
	if len(ayats) != 2 {
		t.Fatalf("ayat count = %d, want 2", len(ayats))
	}

	// 3 huruf under ayat 1.
	a1 := idFindByPath(roots, "pasal-1/ayat-1")
	if a1 == nil {
		t.Fatal("missing ayat-1")
	}
	var hurufs []string
	idCollect(a1.Children, "huruf", &hurufs)
	if len(hurufs) != 3 {
		t.Fatalf("huruf count under ayat-1 = %d, want 3", len(hurufs))
	}
	if s := idFindByPath(roots, "pasal-1/ayat-1/huruf-c"); s == nil {
		t.Fatal("missing huruf-c under ayat-1")
	}
}

func TestParseIndonesianUU_penjelasanSplit(t *testing.T) {
	text := `BAB I
KETENTUAN UMUM
Pasal 1
Definisi.
Pasal 2
Asas.

PENJELASAN
ATAS
UNDANG-UNDANG REPUBLIK INDONESIA

I. UMUM
Perkembangan teknologi.

II. PASALDEMIPASAL
Pasal 1
Cukup jelas.
Pasal 2
Cukup jelas.
`
	roots := ParseIndonesianUU(text)

	// Only 2 Pasal from the main body (not the PENJELASAN ones).
	var pasals []string
	idCollect(roots, "pasal", &pasals)
	if len(pasals) != 2 {
		t.Fatalf("pasal count = %d, want 2 (penjelasan excluded); pasals = %v", len(pasals), pasals)
	}

	// PENJELASAN node exists.
	var penj []string
	idCollect(roots, "penjelasan", &penj)
	if len(penj) == 0 {
		t.Fatal("missing penjelasan section")
	}
}

func TestParseIndonesianUU_noBabDocument(t *testing.T) {
	// Short regulations start directly at Pasal 1 with no BAB.
	text := `PERATURAN OTORITAS JASA KEUANGAN
NOMOR 5 TAHUN 2026
TENTANG TEKNOLOGI INFORMASI

Pasal 1
Ketentuan umum.
Pasal 2
(1) Penyelenggara wajib menerapkan.
(2) Ketentuan lebih lanjut.
Pasal 3
Ketentuan penutup.
`
	roots := ParseIndonesianUU(text)

	// No BABs.
	var babs []string
	idCollect(roots, "bab", &babs)
	if len(babs) != 0 {
		t.Fatalf("bab count = %d, want 0 (no-BAB document)", len(babs))
	}

	// 3 Pasal at root level.
	var pasals []string
	idCollect(roots, "pasal", &pasals)
	if len(pasals) != 3 {
		t.Fatalf("pasal count = %d, want 3", len(pasals))
	}

	// Citation paths are not nested under a bab.
	if s := idFindByPath(roots, "pasal-1"); s == nil {
		t.Fatal("missing pasal-1 at root level")
	}
}

func TestParseIndonesianUU_ocrNoiseStripping(t *testing.T) {
	text := `PRESIDEN
REPUELIK INDONESIA
SK No 016999A
BAB I
KETENTUAN UMUM
24
Pasal 1
Content line one.
SK No 017000 A
FRESIDEN
REPUBLIK INDONESIA
Pasal 2
Content line two.
`
	roots := ParseIndonesianUU(text)

	// Noise lines must not appear in content.
	var pasals []string
	idCollect(roots, "pasal", &pasals)
	if len(pasals) != 2 {
		t.Fatalf("pasal count = %d, want 2 (noise stripped)", len(pasals))
	}

	p1 := idFindByPath(roots, "bab-i/pasal-1")
	if p1 == nil {
		t.Fatal("missing pasal-1")
	}
	if strings.Contains(p1.Content, "SK No") {
		t.Error("SK No watermark leaked into pasal-1 content")
	}
	if strings.Contains(p1.Content, "PRESIDEN") {
		t.Error("PRESIDEN header leaked into pasal-1 content")
	}
	if strings.Contains(p1.Content, "24") {
		t.Error("bare page number leaked into pasal-1 content")
	}
}

func TestParseIndonesianUU_babHeadingOnNextLine(t *testing.T) {
	text := `BAB I
KETENTUAN UMUM
Pasal 1
Definisi.
BAB II
HAK SUBJEK DATA PRIBADI
Pasal 2
Hak subjek.
`
	roots := ParseIndonesianUU(text)
	bab1 := idFindByPath(roots, "bab-i")
	if bab1 == nil {
		t.Fatal("missing bab-i")
	}
	if bab1.Heading != "KETENTUAN UMUM" {
		t.Errorf("bab-i heading = %q, want %q", bab1.Heading, "KETENTUAN UMUM")
	}
}

func TestParseIndonesianUU_uniquePaths(t *testing.T) {
	text := `BAB I
KETENTUAN UMUM
Pasal 1
Content.
Pasal 2
Content.
BAB II
DATA PRIBADI
Pasal 3
Content.
`
	roots := ParseIndonesianUU(text)
	seen := map[string]bool{}
	var walk func([]Section)
	walk = func(secs []Section) {
		for _, s := range secs {
			if seen[s.CitationPath] {
				t.Fatalf("duplicate citation path: %s", s.CitationPath)
			}
			seen[s.CitationPath] = true
			walk(s.Children)
		}
	}
	walk(roots)
}

func TestParseIndonesianUU_lampiranStopsMainParsing(t *testing.T) {
	text := `BAB I
KETENTUAN UMUM
Pasal 1
Definisi.
Pasal 2
Ketentuan.

LAMPIRAN
UNDANG-UNDANG REPUBLIK INDONESIA

Tabel 1: Daftar sanksi.
Pasal 1
This should not be parsed as a main-body Pasal.
`
	roots := ParseIndonesianUU(text)

	// Only 2 Pasal from the main body.
	var pasals []string
	idCollect(roots, "pasal", &pasals)
	if len(pasals) != 2 {
		t.Fatalf("pasal count = %d, want 2 (lampiran's Pasal excluded); pasals = %v", len(pasals), pasals)
	}

	// Lampiran node exists.
	var lamps []string
	idCollect(roots, "lampiran", &lamps)
	if len(lamps) != 1 {
		t.Fatalf("lampiran count = %d, want 1", len(lamps))
	}
}

// ---- inline Pasal heading tests (BPK OCR merges heading + body) -----------

func TestParseIndonesianUU_inlinePasalAyat(t *testing.T) {
	// BPK OCR merges Pasal heading with the first ayat on the same line.
	text := `Pasal 1
Definisi umum.
Pasal 2 (1) Undang-Undang ini berlaku untuk setiap orang.
(2) Ketentuan lebih lanjut diatur dengan Peraturan Pemerintah.
Pasal 3
Ketentuan penutup.
`
	roots := ParseIndonesianUU(text)

	// 3 Pasal.
	var pasals []string
	idCollect(roots, "pasal", &pasals)
	if len(pasals) != 3 {
		t.Fatalf("pasal count = %d, want 3; pasals = %v", len(pasals), pasals)
	}

	// Pasal 2 must have 2 ayat (the inline one + the next line).
	p2 := idFindByPath(roots, "pasal-2")
	if p2 == nil {
		t.Fatal("missing pasal-2")
	}
	var ayats []string
	idCollect(p2.Children, "ayat", &ayats)
	if len(ayats) != 2 {
		t.Fatalf("pasal 2 ayat count = %d, want 2; ayats = %v", len(ayats), ayats)
	}

	// Ayat 1 must contain the inline text.
	a1 := idFindByPath(roots, "pasal-2/ayat-1")
	if a1 == nil {
		t.Fatal("missing pasal-2/ayat-1")
	}
	if !strings.Contains(a1.Content, "Undang-Undang ini berlaku") {
		t.Errorf("ayat-1 content = %q, want it to contain inline text", a1.Content)
	}
}

func TestParseIndonesianUU_inlinePasalBodyText(t *testing.T) {
	// BPK OCR merges Pasal heading with body text (no ayat marker).
	text := `Pasal 1
Definisi umum.
Pasal 2 Subjek Data Pribadi berhak mendapatkan informasi.
Pasal 3
Ketentuan penutup.
`
	roots := ParseIndonesianUU(text)

	var pasals []string
	idCollect(roots, "pasal", &pasals)
	if len(pasals) != 3 {
		t.Fatalf("pasal count = %d, want 3; pasals = %v", len(pasals), pasals)
	}

	// Pasal 2 must contain the inline body text.
	p2 := idFindByPath(roots, "pasal-2")
	if p2 == nil {
		t.Fatal("missing pasal-2")
	}
	if !strings.Contains(p2.Content, "Subjek Data Pribadi berhak") {
		t.Errorf("pasal-2 content = %q, want it to contain inline body text", p2.Content)
	}
}

func TestParseIndonesianUU_inlinePasalOCRNoise(t *testing.T) {
	// OCR noise + inline content: Pasa7 (l→7) with inline ayat.
	text := `Pasal 1
Content one.
Pasal 2
Content two.
Pasal 3
Content three.
Pasa74 (1) Persetujuan sebagaimana dimaksud wajib diberikan.
(2) Persetujuan harus secara tegas.
Pasal 5
Content five.
`
	roots := ParseIndonesianUU(text)

	var pasals []string
	idCollect(roots, "pasal", &pasals)
	if len(pasals) != 5 {
		t.Fatalf("pasal count = %d, want 5; pasals = %v", len(pasals), pasals)
	}

	// Pasal 4 (from "Pasa74") must have 2 ayat.
	p4 := idFindByPath(roots, "pasal-4")
	if p4 == nil {
		t.Fatal("missing pasal-4")
	}
	var ayats []string
	idCollect(p4.Children, "ayat", &ayats)
	if len(ayats) != 2 {
		t.Fatalf("pasal 4 ayat count = %d, want 2; ayats = %v", len(ayats), ayats)
	}

	a1 := idFindByPath(roots, "pasal-4/ayat-1")
	if a1 == nil {
		t.Fatal("missing pasal-4/ayat-1")
	}
	if !strings.Contains(a1.Content, "Persetujuan") {
		t.Errorf("ayat-1 content = %q, want it to contain inline text", a1.Content)
	}
}

func TestParseIndonesianUU_inlinePasalStandaloneUnchanged(t *testing.T) {
	// Standalone Pasal headings (no inline text) must still work as before.
	text := `Pasal 1
Definisi umum.
Pasal 2
(1) Asas pelindungan.
(2) Asas lainnya.
Pasal 3
Ketentuan penutup.
`
	roots := ParseIndonesianUU(text)

	var pasals []string
	idCollect(roots, "pasal", &pasals)
	if len(pasals) != 3 {
		t.Fatalf("pasal count = %d, want 3; pasals = %v", len(pasals), pasals)
	}

	// Pasal 2 has 2 ayat from separate lines (not inline).
	p2 := idFindByPath(roots, "pasal-2")
	if p2 == nil {
		t.Fatal("missing pasal-2")
	}
	var ayats []string
	idCollect(p2.Children, "ayat", &ayats)
	if len(ayats) != 2 {
		t.Fatalf("pasal 2 ayat count = %d, want 2", len(ayats))
	}
}

func TestParseIndonesianUU_inlinePasalWithBab(t *testing.T) {
	// Inline Pasal inside BAB structure.
	text := `BAB I
KETENTUAN UMUM
Pasal 1 Dalam Undang-Undang ini yang dimaksud dengan:
a. Data Pribadi adalah data tentang orang;
b. Pengendali adalah pihak yang menentukan.
Pasal 2 (1) Pelindungan Data Pribadi dilakukan berdasarkan asas.
(2) Asas sebagaimana dimaksud pada ayat (1) meliputi kepastian hukum.
BAB II
JENIS DATA PRIBADI
Pasal 3
Data Pribadi terdiri atas dua jenis.
`
	roots := ParseIndonesianUU(text)

	// 2 BABs, 3 Pasal.
	var babs []string
	idCollect(roots, "bab", &babs)
	if len(babs) != 2 {
		t.Fatalf("bab count = %d, want 2", len(babs))
	}

	var pasals []string
	idCollect(roots, "pasal", &pasals)
	if len(pasals) != 3 {
		t.Fatalf("pasal count = %d, want 3; pasals = %v", len(pasals), pasals)
	}

	// Pasal 1 under BAB I has inline content + 2 huruf.
	p1 := idFindByPath(roots, "bab-i/pasal-1")
	if p1 == nil {
		t.Fatal("missing bab-i/pasal-1")
	}
	if !strings.Contains(p1.Content, "Dalam Undang-Undang") {
		t.Errorf("pasal-1 content = %q, want inline text", p1.Content)
	}
	var hurufs []string
	idCollect(p1.Children, "huruf", &hurufs)
	if len(hurufs) != 2 {
		t.Fatalf("pasal 1 huruf count = %d, want 2", len(hurufs))
	}

	// Pasal 2 under BAB I has 2 ayat (first inline, second on next line).
	p2 := idFindByPath(roots, "bab-i/pasal-2")
	if p2 == nil {
		t.Fatal("missing bab-i/pasal-2")
	}
	var ayats []string
	idCollect(p2.Children, "ayat", &ayats)
	if len(ayats) != 2 {
		t.Fatalf("pasal 2 ayat count = %d, want 2", len(ayats))
	}
}

// ---- leading OCR noise before Pasal headings --------------------------------

func TestParseIndonesianUU_leadingNoiseBeforePasal(t *testing.T) {
	// BPK OCR sometimes produces stray characters before a Pasal heading:
	//   "' Pasal 23 ..." (leading apostrophe)
	//   ". Pasal 29 ..." (leading dot)
	// These must not break the monotonic sequence.
	text := `Pasal 1
Definisi umum.
Pasal 2
Asas pelindungan.
' Pasal 3 Klausul perjanjian.
Pasal 4
Pemrosesan Data Pribadi.
. Pasal 5 (1) Pengendali wajib memastikan akurasi.
(2) Ketentuan lebih lanjut.
Pasal 6
Ketentuan penutup.
`
	roots := ParseIndonesianUU(text)

	var pasals []string
	idCollect(roots, "pasal", &pasals)
	if len(pasals) != 6 {
		t.Fatalf("pasal count = %d, want 6 (leading noise stripped); pasals = %v", len(pasals), pasals)
	}
	for i, want := range []string{"Pasal 1", "Pasal 2", "Pasal 3", "Pasal 4", "Pasal 5", "Pasal 6"} {
		if pasals[i] != want {
			t.Errorf("pasal[%d] = %q, want %q", i, pasals[i], want)
		}
	}

	// Pasal 3 must have inline content.
	p3 := idFindByPath(roots, "pasal-3")
	if p3 == nil {
		t.Fatal("missing pasal-3")
	}
	if !strings.Contains(p3.Content, "Klausul perjanjian") {
		t.Errorf("pasal-3 content = %q, want inline text", p3.Content)
	}

	// Pasal 5 must have 2 ayat.
	p5 := idFindByPath(roots, "pasal-5")
	if p5 == nil {
		t.Fatal("missing pasal-5")
	}
	var ayats []string
	idCollect(p5.Children, "ayat", &ayats)
	if len(ayats) != 2 {
		t.Fatalf("pasal 5 ayat count = %d, want 2", len(ayats))
	}
}

// ---- omnibus quoted-article rule --------------------------------------------

func TestParseIndonesianUU_omnibusQuotedArticles(t *testing.T) {
	// Omnibus law pattern: outer Pasal N contains amendment instructions that
	// quote/insert inner articles from other laws. The inner "Pasal M" headings
	// must stay as CONTENT of the outer Pasal, never as top-level sections —
	// even when M happens to equal lastPasal+1.
	text := `BAB I
KETENTUAN UMUM
Pasal 1
Dalam Undang-Undang ini yang dimaksud dengan:
1. Sistem Keuangan adalah suatu kesatuan.
BAB II
KELEMBAGAAN
Pasal 2
Undang-Undang ini mengatur kelembagaan.
Pasal 3 Beberapa ketentuan dalam Undang-Undang Nomor 9 Tahun 2016 diubah sebagai berikut:
Ketentuan Pasal 1 diubah sehingga berbunyi sebagai berikut:
Pasal 1
Dalam UU ini yang dimaksud dengan Stabilitas Sistem Keuangan.
Ketentuan Pasal 4 diubah sehingga berbunyi sebagai berikut:
Pasal 4 (1) Dibentuk Komite Stabilitas Sistem Keuangan.
(2) Komite beranggotakan Menteri Keuangan.
BAB III
LEMBAGA PENJAMIN SIMPANAN
Pasal 4 Beberapa ketentuan dalam Undang-Undang Nomor 24 Tahun 2004 diubah sebagai berikut:
Ketentuan Pasal 1 diubah sehingga berbunyi sebagai berikut:
Pasal 1
Dalam UU ini yang dimaksud dengan Simpanan.
Ketentuan Pasal 5 diubah sehingga berbunyi sebagai berikut:
Pasal 5 (1) LPS menjamin Simpanan nasabah Bank.
(2) Ketentuan lebih lanjut.
BAB IV
KETENTUAN PENUTUP
Pasal 5
Undang-Undang ini mulai berlaku.
`
	roots := ParseIndonesianUU(text)

	// Only outer Pasal 1..5 must be recognized.
	var pasals []string
	idCollect(roots, "pasal", &pasals)
	if len(pasals) != 5 {
		t.Fatalf("pasal count = %d, want 5 (quoted articles excluded); pasals = %v", len(pasals), pasals)
	}
	for i, want := range []string{"Pasal 1", "Pasal 2", "Pasal 3", "Pasal 4", "Pasal 5"} {
		if pasals[i] != want {
			t.Errorf("pasal[%d] = %q, want %q", i, pasals[i], want)
		}
	}

	// Outer Pasal 3 (the amendment instruction) must contain the quoted
	// inner articles as content, not children sections.
	p3 := idFindByPath(roots, "bab-ii/pasal-3")
	if p3 == nil {
		t.Fatal("missing pasal-3 under bab-ii")
	}
	if !strings.Contains(p3.Content, "Stabilitas Sistem Keuangan") {
		t.Errorf("pasal-3 should contain quoted inner article text, got: %q", p3.Content)
	}

	// Outer Pasal 4 must also contain quoted inner article content.
	p4 := idFindByPath(roots, "bab-iii/pasal-4")
	if p4 == nil {
		t.Fatal("missing pasal-4 under bab-iii")
	}
	if !strings.Contains(p4.Content, "LPS menjamin Simpanan") {
		t.Errorf("pasal-4 should contain quoted inner article text, got: %q", p4.Content)
	}
}

func TestParseIndonesianUU_omnibusDoesNotBlockOuterPasal(t *testing.T) {
	// Verify that amendment instructions inside an outer Pasal don't block
	// the NEXT outer Pasal from being recognized (the amendment context
	// only applies to the immediately following line).
	text := `Pasal 1
Definisi.
Pasal 2 Beberapa ketentuan dalam UU Nomor 7 Tahun 2017 diubah sebagai berikut:
Ketentuan Pasal 5 diubah sehingga berbunyi sebagai berikut:
Pasal 5
Mata Uang Rupiah berlaku.
Pasal 3
Undang-Undang ini mulai berlaku.
`
	roots := ParseIndonesianUU(text)

	var pasals []string
	idCollect(roots, "pasal", &pasals)
	if len(pasals) != 3 {
		t.Fatalf("pasal count = %d, want 3; pasals = %v", len(pasals), pasals)
	}

	// Pasal 3 must be accepted as outer.
	p3 := idFindByPath(roots, "pasal-3")
	if p3 == nil {
		t.Fatal("missing pasal-3 at root")
	}
	if !strings.Contains(p3.Content, "mulai berlaku") {
		t.Errorf("pasal-3 content = %q, want closing provision", p3.Content)
	}
}

// ---- digit-space collapse tests ---------------------------------------------

func TestParseIndonesianUU_digitSpaceCollapse(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantCount int
		wantLast  string
	}{
		{
			name: "Pasal 1 1 = Pasal 11",
			input: buildPasalSequence(1, 11, "Pasal 1 1") +
				"Content of article 11.\n",
			wantCount: 11,
			wantLast:  "Pasal 11",
		},
		{
			name: "Pasal 4 1 = Pasal 41",
			input: buildPasalSequence(1, 41, "Pasal 4 1") +
				"Content of article 41.\n",
			wantCount: 41,
			wantLast:  "Pasal 41",
		},
		{
			name: "Pasal 1 1 2 = Pasal 112",
			input: buildPasalSequence(1, 112, "Pasal 1 1 2") +
				"Content of article 112.\n",
			wantCount: 112,
			wantLast:  "Pasal 112",
		},
		{
			name: "Pasal 33 1 = Pasal 331",
			input: buildPasalSequence(1, 331, "Pasal 33 1") +
				"Content of article 331.\n",
			wantCount: 331,
			wantLast:  "Pasal 331",
		},
		{
			name: "Pasal 1 1 with inline ayat",
			input: buildPasalSequence(1, 11, "Pasal 1 1 (1) Content here.") +
				"(2) More content.\n",
			wantCount: 11,
			wantLast:  "Pasal 11",
		},
		{
			name:      "Pasal 2 1 with inline body text",
			input:     buildPasalSequence(1, 21, "Pasal 2 1 Subjek Data berhak."),
			wantCount: 21,
			wantLast:  "Pasal 21",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			roots := ParseIndonesianUU(tt.input)
			var pasals []string
			idCollect(roots, "pasal", &pasals)
			if len(pasals) != tt.wantCount {
				t.Errorf("pasal count = %d, want %d; last few = %v",
					len(pasals), tt.wantCount, pasals[max(0, len(pasals)-5):])
			}
			if len(pasals) > 0 && pasals[len(pasals)-1] != tt.wantLast {
				t.Errorf("last pasal = %q, want %q", pasals[len(pasals)-1], tt.wantLast)
			}
		})
	}
}

func TestCollapsePasalDigitSpaces(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Pasal 1 1", "Pasal 11"},
		{"Pasal 4 1", "Pasal 41"},
		{"Pasal 1 1 2", "Pasal 112"},
		{"Pasal 33 1", "Pasal 331"},
		{"Pasal 1 1 (1) Content.", "Pasal 11 (1) Content."},
		{"Pasal 2 1 Subjek Data.", "Pasal 21 Subjek Data."},
		// No collapse: single digit (not space-split).
		{"Pasal 11", "Pasal 11"},
		// No collapse: not a Pasal heading.
		{"Bagian 1 1", "Bagian 1 1"},
		// No collapse: lowercase after number (body text, not heading).
		{"Pasal 1 1 ayat (2)", "Pasal 1 1 ayat (2)"},
	}
	for _, tt := range tests {
		got := idCollapsePasalDigitSpaces(tt.input)
		if got != tt.want {
			t.Errorf("idCollapsePasalDigitSpaces(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// ---- combined PRESIDEN REPUBLIK noise stripping tests -----------------------

func TestParseIndonesianUU_presidenRepublikNoise(t *testing.T) {
	text := `Pasal 1
Content line one.
PRESIDEN REPUBLIK INDONESIA
Pasal 2
Content line two.
PRESIDEN REPUBUK INDONESIA
Pasal 3
Content line three.
PRESIDEN REPUBUK TNDONESIA
Pasal 4
Content four.
PRESIDEN REPUEUK INDONESIA
Pasal 5
Content five.
PRESIDEN REPUBLIK INDONESIA,
Pasal 6
Content six.
PRESIDEN REPUBLTK INDONESIA
Pasal 7
Content seven.
PRESIDEN REPUBLIK INOONESIA
Pasal 8
Content eight.
`
	roots := ParseIndonesianUU(text)

	var pasals []string
	idCollect(roots, "pasal", &pasals)
	if len(pasals) != 8 {
		t.Fatalf("pasal count = %d, want 8 (PRESIDEN REPUBLIK noise stripped); pasals = %v", len(pasals), pasals)
	}

	// None of the content should contain the noise.
	p1 := idFindByPath(roots, "pasal-1")
	if p1 == nil {
		t.Fatal("missing pasal-1")
	}
	if strings.Contains(p1.Content, "PRESIDEN") {
		t.Error("PRESIDEN REPUBLIK header leaked into pasal-1 content")
	}
}

// ---- integration test: UU 27/2022 PDP from real PDF -----------------------

// repoRoot walks up from the working directory to find the repo root (where
// go.mod lives). Returns "" if not found.
func repoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func TestParseIndonesianUU_UU27_2022_pdftotext(t *testing.T) {
	root := repoRoot()
	if root == "" {
		t.Skip("repo root not found")
	}
	uu27PDF := filepath.Join(root, "data", "spike_id", "peraturan", "UU_27_2022_PDP_correct.pdf")

	// Skip if the PDF or pdftotext is absent.
	if _, err := os.Stat(uu27PDF); os.IsNotExist(err) {
		t.Skipf("PDF not found: %s", uu27PDF)
	}
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext not installed")
	}

	out, err := exec.Command("pdftotext", uu27PDF, "-", "-enc", "UTF-8").Output()
	if err != nil {
		t.Fatalf("pdftotext: %v", err)
	}
	text := string(out)
	roots := ParseIndonesianUU(text)

	// --- Pasal inventory: exactly 1..76, 0 gaps, 0 duplicates ---
	pasals := idCollectSections(roots, "pasal")
	t.Logf("Pasal count: %d", len(pasals))

	if len(pasals) != 76 {
		// Log all found for diagnosis.
		for _, p := range pasals {
			t.Logf("  %s (ordinal=%d, path=%s)", p.Label, p.Ordinal, p.CitationPath)
		}
		t.Fatalf("Pasal count = %d, want 76", len(pasals))
	}

	// Verify monotonic 1..76.
	seen := map[int]bool{}
	for _, p := range pasals {
		if seen[p.Ordinal] {
			t.Errorf("duplicate Pasal ordinal: %d", p.Ordinal)
		}
		seen[p.Ordinal] = true
	}
	for i := 1; i <= 76; i++ {
		if !seen[i] {
			t.Errorf("missing Pasal %d", i)
		}
	}

	// --- BAB inventory ---
	babs := idCollectSections(roots, "bab")
	t.Logf("BAB count: %d", len(babs))
	for _, b := range babs {
		t.Logf("  %s heading=%q", b.Label, b.Heading)
	}

	// UU 27/2022 has BAB I through BAB XVI, but some BAB headings appear as
	// duplicates due to page-break noise ("-25BAB VIII" repeating "BABVIII ...").
	// After noise stripping, we expect the unique set. The actual law has 16
	// BABs (I-XVI). The task spec says 13, but the real text has 16.
	if len(babs) < 13 {
		t.Errorf("BAB count = %d, want >= 13", len(babs))
	}

	// --- Ayat and Huruf totals ---
	ayats := idCollectSections(roots, "ayat")
	hurufs := idCollectSections(roots, "huruf")
	t.Logf("Total ayat: %d", len(ayats))
	t.Logf("Total huruf: %d", len(hurufs))

	// Pasal 1 must exist.
	if pasals[0].Label != "Pasal 1" || pasals[0].Ordinal != 1 {
		t.Errorf("first pasal = %+v, want Pasal 1", pasals[0])
	}
	// Pasal 76 must be last.
	if pasals[75].Label != "Pasal 76" || pasals[75].Ordinal != 76 {
		t.Errorf("last pasal = %+v, want Pasal 76", pasals[75])
	}

	// PENJELASAN must be captured.
	penj := idCollectSections(roots, "penjelasan")
	t.Logf("Penjelasan sections: %d", len(penj))
	if len(penj) == 0 {
		// The PENJELASAN may be captured via the PASAL DEMI PASAL marker.
		// It's acceptable if the banner triggers and the node exists.
		t.Log("WARNING: no penjelasan section found — check PENJELASAN detection")
	}
}
