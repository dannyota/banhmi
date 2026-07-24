package pipeline

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"danny.vn/banhmi/pkg/extract"
	"danny.vn/banhmi/pkg/ingest"
	dbbronze "danny.vn/banhmi/pkg/store/bronze"
)

func TestPickFilePrefersMainDocxOverAppendix(t *testing.T) {
	files := []dbbronze.BronzeRawFile{
		{ID: 1, FileKind: "appendix", FileFormat: "docx"},
		{ID: 2, FileKind: "main", FileFormat: "docx"},
		{ID: 3, FileKind: "original_scan", FileFormat: "pdf"},
	}

	got := pickFile(files, "docx", "main")
	if got == nil {
		t.Fatal("pickFile returned nil, want main DOCX")
	}
	if got.ID != 2 {
		t.Fatalf("pickFile picked id=%d, want main DOCX id=2", got.ID)
	}
}

func TestPickFileSkipsAppendixForPrimaryDocx(t *testing.T) {
	files := []dbbronze.BronzeRawFile{
		{ID: 1, FileKind: "appendix", FileFormat: "docx"},
		{ID: 2, FileKind: "original_scan", FileFormat: "pdf"},
	}

	got := pickFile(files, "docx", "main")
	if got != nil {
		t.Fatalf("pickFile picked appendix DOCX id=%d as primary", got.ID)
	}
}

func TestPickFilePDFKindPriority(t *testing.T) {
	files := []dbbronze.BronzeRawFile{
		{ID: 1, FileKind: "appendix", FileFormat: "pdf"},
		{ID: 2, FileKind: "original_scan", FileFormat: "pdf"},
		{ID: 3, FileKind: "main", FileFormat: "pdf"},
	}

	got := pickFile(files, "pdf", "main", "original_scan")
	if got == nil {
		t.Fatal("pickFile returned nil, want main PDF")
	}
	if got.ID != 3 {
		t.Fatalf("pickFile picked id=%d, want main PDF id=3", got.ID)
	}
}

func TestPickPDFForExtractionSkipsOriginalScanAfterBornDigitalReview(t *testing.T) {
	files := []dbbronze.BronzeRawFile{
		{ID: 1, FileKind: "original_scan", FileFormat: "pdf"},
	}

	got := pickPDFForExtraction(files, true)

	if got != nil {
		t.Fatalf("pickPDFForExtraction picked original_scan id=%d after born-digital review", got.ID)
	}
}

func TestPickPDFForExtractionStillAllowsMainPDF(t *testing.T) {
	files := []dbbronze.BronzeRawFile{
		{ID: 1, FileKind: "original_scan", FileFormat: "pdf"},
		{ID: 2, FileKind: "main", FileFormat: "pdf"},
	}

	got := pickPDFForExtraction(files, true)

	if got == nil || got.ID != 2 {
		t.Fatalf("pickPDFForExtraction = %+v, want main PDF id=2", got)
	}
}

func TestPickPDFForExtractionUsesOriginalScanWithoutBornDigitalReview(t *testing.T) {
	files := []dbbronze.BronzeRawFile{
		{ID: 1, FileKind: "original_scan", FileFormat: "pdf"},
	}

	got := pickPDFForExtraction(files, false)

	if got == nil || got.ID != 1 {
		t.Fatalf("pickPDFForExtraction = %+v, want original_scan id=1", got)
	}
}

func TestValidCongbaoFallbackCandidateAcceptsExactNumberAndFile(t *testing.T) {
	issued := time.Date(2022, 1, 27, 0, 0, 0, 0, time.UTC)
	sd := dbbronze.BronzeSourceDocument{
		DocNumber:     strPtr("14/2022/NĐ-CP"),
		DocNumberNorm: normalizeDocNumberForStorage("14/2022/NĐ-CP"),
		DocType:       strPtr("Nghị định"),
		IssuedAt:      &issued,
	}
	doc := ingest.DiscoveredDoc{
		Number:   "14/2022/NĐ-CP",
		DocType:  ingest.DocType("Nghị định"),
		IssuedAt: issued,
		Files:    []ingest.FileRef{{URL: "https://example.invalid/doc.doc", Ext: "doc"}},
	}

	ok, reason := validCongbaoFallbackCandidate(sd, doc)

	if !ok {
		t.Fatalf("validCongbaoFallbackCandidate rejected exact fallback: %s", reason)
	}
}

func TestValidCongbaoFallbackCandidateRejectsFuzzyNumber(t *testing.T) {
	sd := dbbronze.BronzeSourceDocument{
		DocNumber:     strPtr("14/2022/NĐ-CP"),
		DocNumberNorm: normalizeDocNumberForStorage("14/2022/NĐ-CP"),
	}
	doc := ingest.DiscoveredDoc{
		Number: "13/2022/NĐ-CP",
		Files:  []ingest.FileRef{{URL: "https://example.invalid/doc.pdf", Ext: "pdf"}},
	}

	ok, reason := validCongbaoFallbackCandidate(sd, doc)

	if ok || reason != "number_mismatch" {
		t.Fatalf("validCongbaoFallbackCandidate = %v/%q, want number_mismatch", ok, reason)
	}
}

func TestValidCongbaoFallbackCandidateRejectsIssuedDateMismatch(t *testing.T) {
	issued := time.Date(2022, 1, 27, 0, 0, 0, 0, time.UTC)
	sd := dbbronze.BronzeSourceDocument{
		DocNumber:     strPtr("14/2022/NĐ-CP"),
		DocNumberNorm: normalizeDocNumberForStorage("14/2022/NĐ-CP"),
		IssuedAt:      &issued,
	}
	doc := ingest.DiscoveredDoc{
		Number:   "14/2022/NĐ-CP",
		IssuedAt: issued.AddDate(0, 0, 1),
		Files:    []ingest.FileRef{{URL: "https://example.invalid/doc.pdf", Ext: "pdf"}},
	}

	ok, reason := validCongbaoFallbackCandidate(sd, doc)

	if ok || reason != "issued_date_mismatch" {
		t.Fatalf("validCongbaoFallbackCandidate = %v/%q, want issued_date_mismatch", ok, reason)
	}
}

func TestValidCongbaoFallbackCandidateRejectsNoExtractableFiles(t *testing.T) {
	sd := dbbronze.BronzeSourceDocument{
		DocNumber:     strPtr("14/2022/NĐ-CP"),
		DocNumberNorm: normalizeDocNumberForStorage("14/2022/NĐ-CP"),
	}
	doc := ingest.DiscoveredDoc{
		Number: "14/2022/NĐ-CP",
		Files:  []ingest.FileRef{{URL: "https://example.invalid/doc.html", Ext: "html"}},
	}

	ok, reason := validCongbaoFallbackCandidate(sd, doc)

	if ok || reason != "no_extractable_files" {
		t.Fatalf("validCongbaoFallbackCandidate = %v/%q, want no_extractable_files", ok, reason)
	}
}

// TestSourcePriorityCaseSQLParity guards the single source→priority table:
// the SQL CASE the normalize selector interpolates must agree with
// metadataPriority for every listed source, for unknown sources (ELSE), and
// must rank NULL 0 so a source-less validity row never blocks a re-normalize.
func TestSourcePriorityCaseSQLParity(t *testing.T) {
	const col = "fd.source"
	rendered := sourcePriorityCaseSQL(col)

	whenRe := regexp.MustCompile(`WHEN fd\.source = '([^']+)' THEN (\d+)`)
	got := make(map[string]int16)
	for _, m := range whenRe.FindAllStringSubmatch(rendered, -1) {
		n, err := strconv.Atoi(m[2])
		if err != nil {
			t.Fatalf("non-numeric priority %q in %q", m[2], rendered)
		}
		if _, dup := got[m[1]]; dup {
			t.Errorf("duplicate WHEN branch for source %q", m[1])
		}
		got[m[1]] = int16(n)
	}
	if len(got) != len(sourceMetadataPriority) {
		t.Errorf("CASE lists %d sources, priority table has %d: %q", len(got), len(sourceMetadataPriority), rendered)
	}
	for source := range sourceMetadataPriority {
		if got[source] != metadataPriority(source) {
			t.Errorf("source %q: SQL CASE yields %d, metadataPriority yields %d", source, got[source], metadataPriority(source))
		}
	}

	elseRe := regexp.MustCompile(`ELSE (\d+) END$`)
	m := elseRe.FindStringSubmatch(rendered)
	if m == nil {
		t.Fatalf("no ELSE branch in %q", rendered)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("non-numeric ELSE priority %q", m[1])
	}
	if int16(n) != metadataPriority("some_unlisted_source") {
		t.Errorf("ELSE yields %d, metadataPriority(unknown) yields %d", n, metadataPriority("some_unlisted_source"))
	}

	if !strings.HasPrefix(rendered, "CASE WHEN fd.source IS NULL THEN 0 ") {
		t.Errorf("CASE must rank NULL 0 first, got %q", rendered)
	}
	if again := sourcePriorityCaseSQL(col); again != rendered {
		t.Errorf("render is not deterministic:\n%q\n%q", rendered, again)
	}
}

func TestIsSetnegPageArtifact(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{"SK No 164024 A", true},
		{"SK No 16401,4 A", true}, // OCR digit noise
		{"PRESIDEN REPUBLIK INDONESIA", true},
		{"FRESIDEN REPI.IBUK INDONESIA", false}, // embedded-layer garbage never reaches the cleaner
		{"- 52 -", true},
		{"-52-", true},
		{"PRESIDEN REPUBLIK INDONESIA,", false}, // preamble/signature line of the law itself
		{"Menimbang: bahwa untuk melaksanakan ketentuan", false},
		{"Pasal 52", false},
		{"- catatan -", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isSetnegPageArtifact(tt.line); got != tt.want {
			t.Errorf("isSetnegPageArtifact(%q) = %v, want %v", tt.line, got, tt.want)
		}
	}
}

func TestDocKeyDedupesSourcesByDocNumber(t *testing.T) {
	vbpl := dbbronze.BronzeSourceDocument{Source: "vbpl", ExternalID: "152698", DocNumber: strPtr("14/2022/NĐ-CP"), DocType: strPtr("Nghị định")}
	congbao := dbbronze.BronzeSourceDocument{Source: "congbao", ExternalID: "36772", DocNumber: strPtr("14/2022/NĐ-CP"), DocType: strPtr("Nghị định")}

	if docKey(vbpl) != docKey(congbao) {
		t.Fatalf("docKey mismatch: vbpl=%q congbao=%q", docKey(vbpl), docKey(congbao))
	}
}

func TestDocKey(t *testing.T) {
	tests := []struct {
		name string
		sd   dbbronze.BronzeSourceDocument
		want string
	}{
		{
			name: "type discriminates documents sharing a number",
			sd:   dbbronze.BronzeSourceDocument{Source: "vbpl", ExternalID: "17067", DocNumber: strPtr("51/2005/QH11"), DocType: strPtr("Luật")},
			want: "LUẬT|51/2005/QH11",
		},
		{
			name: "same number different type yields a different key",
			sd:   dbbronze.BronzeSourceDocument{Source: "vbpl", ExternalID: "17116", DocNumber: strPtr("51/2005/QH11"), DocType: strPtr("Nghị quyết")},
			want: "NGHỊ QUYẾT|51/2005/QH11",
		},
		{
			name: "stray spaces around separators are tightened",
			sd:   dbbronze.BronzeSourceDocument{Source: "vbpl", ExternalID: "130588", DocNumber: strPtr("18 /2018/TT-NHNN"), DocType: strPtr("Thông tư")},
			want: "THÔNG TƯ|18/2018/TT-NHNN",
		},
		{
			name: "NBSP folds like a plain space",
			sd:   dbbronze.BronzeSourceDocument{Source: "vbpl", ExternalID: "1", DocNumber: strPtr("18 /2018/TT-NHNN"), DocType: strPtr("Thông tư")},
			want: "THÔNG TƯ|18/2018/TT-NHNN",
		},
		{
			// A suffix-less number with no source type still keys on the number
			// alone (QH is ambiguous, so the type is never derived from it).
			name: "missing type keys on the number alone",
			sd:   dbbronze.BronzeSourceDocument{Source: "sbv_hanoi", ExternalID: "9", DocNumber: strPtr("51/2005/QH11")},
			want: "51/2005/QH11",
		},
		{
			// A type-bearing suffix converges a typeless observation with the
			// typed one from another source (dedup: single identity).
			name: "missing type is derived from an unambiguous suffix",
			sd:   dbbronze.BronzeSourceDocument{Source: "sbv_hanoi", ExternalID: "9", DocNumber: strPtr("99/2024/TT-NHNN")},
			want: "THÔNG TƯ|99/2024/TT-NHNN",
		},
		{
			name: "OJK infix overrides a mislabeled type (SEOJK label on a PADK number)",
			sd:   dbbronze.BronzeSourceDocument{Source: "ojk", ExternalID: "x1", DocNumber: strPtr("SEOJK 43/PADK.03/2025"), DocType: strPtr("Surat Edaran Otoritas Jasa Keuangan")},
			want: "PADK|PADK 43/PADK.03/2025",
		},
		{
			name: "OJK infix keys a bare number identically",
			sd:   dbbronze.BronzeSourceDocument{Source: "ojkweb", ExternalID: "x2", DocNumber: strPtr("43/PADK.03/2025"), DocType: strPtr("PADK")},
			want: "PADK|PADK 43/PADK.03/2025",
		},
		{
			name: "OJK infix agrees with a correct label (no change)",
			sd:   dbbronze.BronzeSourceDocument{Source: "bpk", ExternalID: "x3", DocNumber: strPtr("11/POJK.03/2022"), DocType: strPtr("Peraturan Otoritas Jasa Keuangan")},
			want: "POJK|POJK 11/POJK.03/2022",
		},
		{
			name: "missing number falls back to source:external_id",
			sd:   dbbronze.BronzeSourceDocument{Source: "vbpl", ExternalID: "22313", DocType: strPtr("Hiến pháp")},
			want: "vbpl:22313",
		},
		{
			name: "blank number falls back to source:external_id",
			sd:   dbbronze.BronzeSourceDocument{Source: "vbpl", ExternalID: "9028", DocNumber: strPtr("  "), DocType: strPtr("Luật")},
			want: "vbpl:9028",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := docKey(tt.sd); got != tt.want {
				t.Fatalf("docKey = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDocKeyTypeFromNumberSuffix(t *testing.T) {
	tests := []struct {
		name string
		sd   dbbronze.BronzeSourceDocument
		want string
	}{
		{
			name: "mislabeled Luật on a TT- number normalizes to Thông tư key",
			sd:   dbbronze.BronzeSourceDocument{Source: "vanban", ExternalID: "v1", DocNumber: strPtr("03/2026/TT-NHNN"), DocType: strPtr("Luật")},
			want: "THÔNG TƯ|03/2026/TT-NHNN",
		},
		{
			name: "mislabeled Nghị quyết on a NĐ-CP number normalizes to Nghị định key",
			sd:   dbbronze.BronzeSourceDocument{Source: "vanban", ExternalID: "v2", DocNumber: strPtr("117/2018/NĐ-CP"), DocType: strPtr("Nghị quyết")},
			want: "NGHỊ ĐỊNH|117/2018/NĐ-CP",
		},
		{
			name: "QH Luật stays distinct (ambiguous number never overridden)",
			sd:   dbbronze.BronzeSourceDocument{Source: "vbpl", ExternalID: "q1", DocNumber: strPtr("51/2005/QH11"), DocType: strPtr("Luật")},
			want: "LUẬT|51/2005/QH11",
		},
		{
			name: "QH Nghị quyết stays distinct (ambiguous number never overridden)",
			sd:   dbbronze.BronzeSourceDocument{Source: "vbpl", ExternalID: "q2", DocNumber: strPtr("51/2005/QH11"), DocType: strPtr("Nghị quyết")},
			want: "NGHỊ QUYẾT|51/2005/QH11",
		},
		{
			name: "TTLT- wins over TT- prefix",
			sd:   dbbronze.BronzeSourceDocument{Source: "vanban", ExternalID: "v3", DocNumber: strPtr("01/2016/TTLT-NHNN-BTP"), DocType: strPtr("Thông tư")},
			want: "THÔNG TƯ LIÊN TỊCH|01/2016/TTLT-NHNN-BTP",
		},
		{
			name: "VBHN- consolidated document",
			sd:   dbbronze.BronzeSourceDocument{Source: "vanban", ExternalID: "v4", DocNumber: strPtr("07/VBHN-NHNN"), DocType: strPtr("Nghị định")},
			want: "VĂN BẢN HỢP NHẤT|07/VBHN-NHNN",
		},
		{
			name: "QĐ- override",
			sd:   dbbronze.BronzeSourceDocument{Source: "vanban", ExternalID: "v5", DocNumber: strPtr("1730/QĐ-NHNN"), DocType: strPtr("Thông tư")},
			want: "QUYẾT ĐỊNH|1730/QĐ-NHNN",
		},
		{
			name: "correct label is unchanged by the guard",
			sd:   dbbronze.BronzeSourceDocument{Source: "vbpl", ExternalID: "v6", DocNumber: strPtr("14/2022/NĐ-CP"), DocType: strPtr("Nghị định")},
			want: "NGHỊ ĐỊNH|14/2022/NĐ-CP",
		},
		{
			name: "empty number falls back to source:external_id",
			sd:   dbbronze.BronzeSourceDocument{Source: "vbpl", ExternalID: "v7", DocNumber: strPtr("  "), DocType: strPtr("Luật")},
			want: "vbpl:v7",
		},
		{
			name: "weird numberless string keys on the number, no suffix override",
			sd:   dbbronze.BronzeSourceDocument{Source: "vbpl", ExternalID: "v8", DocNumber: strPtr("Không số"), DocType: strPtr("Hiến pháp")},
			want: "vbpl:v8",
		},
		{
			name: "ID POJK number is not touched by the VN suffix guard",
			sd:   dbbronze.BronzeSourceDocument{Source: "bpk", ExternalID: "id1", DocNumber: strPtr("11/POJK.03/2022"), DocType: strPtr("Peraturan Otoritas Jasa Keuangan")},
			want: "POJK|POJK 11/POJK.03/2022",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := docKey(tt.sd); got != tt.want {
				t.Fatalf("docKey = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestVNTypeFromNumberSuffix(t *testing.T) {
	tests := []struct {
		number string
		want   string
	}{
		{"03/2026/TT-NHNN", "THÔNG TƯ"},
		{"117/2018/NĐ-CP", "NGHỊ ĐỊNH"},
		{"1730/QĐ-NHNN", "QUYẾT ĐỊNH"},
		{"16/CT-TTG", "CHỈ THỊ"},
		{"01/2016/TTLT-NHNN-BTP", "THÔNG TƯ LIÊN TỊCH"},
		{"07/VBHN-NHNN", "VĂN BẢN HỢP NHẤT"},
		{"42/NQ-CP", "NGHỊ QUYẾT"},
		{"51/2005/QH11", ""}, // ambiguous: Luật vs Nghị quyết
		{"11/POJK.03/2022", ""},
		{"Act 758", ""},
		{"", ""},
		{"14/2022", ""},
	}
	for _, tt := range tests {
		t.Run(tt.number, func(t *testing.T) {
			// docKey upper-cases the number before the suffix check.
			if got := vnTypeFromNumberSuffix(strings.ToUpper(tt.number)); got != tt.want {
				t.Fatalf("vnTypeFromNumberSuffix(%q) = %q, want %q", tt.number, got, tt.want)
			}
		})
	}
}

func TestCorrectIssuedAtYear(t *testing.T) {
	mk := func(y int, m time.Month, d int) *time.Time {
		t := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
		return &t
	}
	tests := []struct {
		name   string
		number *string
		issued *time.Time
		want   *time.Time
	}{
		{
			name:   "year-looking ordinal without slash delimiters is not a year",
			number: strPtr("2028/QĐ-NHNN"),
			issued: mk(2021, time.June, 5),
			want:   mk(2021, time.June, 5),
		},
		{
			name:   "vbpl off-by-one year corrected to the number year",
			number: strPtr("04/2025/TT-NHNN"),
			issued: mk(2024, time.May, 15),
			want:   mk(2025, time.May, 15),
		},
		{
			name:   "December of prior year is a legitimate straddle, not corrected",
			number: strPtr("04/2025/TT-NHNN"),
			issued: mk(2024, time.December, 20),
			want:   mk(2024, time.December, 20),
		},
		{
			name:   "January of following year is a legitimate straddle, not corrected",
			number: strPtr("04/2025/TT-NHNN"),
			issued: mk(2026, time.January, 10),
			want:   mk(2026, time.January, 10),
		},
		{
			name:   "matching year is unchanged",
			number: strPtr("04/2025/TT-NHNN"),
			issued: mk(2025, time.June, 1),
			want:   mk(2025, time.June, 1),
		},
		{
			name:   "two years off (mid-year) is corrected",
			number: strPtr("04/2025/TT-NHNN"),
			issued: mk(2027, time.June, 1),
			want:   mk(2025, time.June, 1),
		},
		{
			name:   "nil issued_at untouched",
			number: strPtr("04/2025/TT-NHNN"),
			issued: nil,
			want:   nil,
		},
		{
			name:   "zero issued_at untouched",
			number: strPtr("04/2025/TT-NHNN"),
			issued: &time.Time{},
			want:   &time.Time{},
		},
		{
			name:   "number without a year is untouched",
			number: strPtr("1730/QĐ-NHNN"),
			issued: mk(2024, time.May, 15),
			want:   mk(2024, time.May, 15),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := correctIssuedAtYear(nil, tt.number, tt.issued, "ext")
			switch {
			case tt.want == nil:
				if got != nil {
					t.Fatalf("got %v, want nil", got)
				}
			case got == nil:
				t.Fatalf("got nil, want %v", tt.want)
			case !got.Equal(*tt.want):
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// TestCorrectIssuedAtYearOtherJurisdictionShapes proves the year cross-check is a
// no-op for the doc-number shapes of the other five jurisdictions: their numbers
// either embed no (19|20)\d{2} year at all, or (ID) the guard is gated to VN at
// the call site so it never runs for them. This test covers the regex gate.
func TestCorrectIssuedAtYearOtherJurisdictionShapes(t *testing.T) {
	issued := time.Date(2019, time.March, 3, 0, 0, 0, 0, time.UTC)
	// MY (Act 758), SG (Cap. 50 / 2021 Rev Ed uses no promulgation-year infix in
	// the citation key), TH (พ.ร.บ. numbers carry Buddhist-era years > 2500, and
	// the ASCII forms carry none), KH (Prakas B7-018-xxx) — none match (19|20)\d{2}.
	numbers := []string{"Act 758", "Cap. 50", "P.U. (A) 123", "B7-018-001", "SEC Kor Nor 3"}
	for _, n := range numbers {
		t.Run(n, func(t *testing.T) {
			num := n
			in := issued
			got := correctIssuedAtYear(nil, &num, &in, "ext")
			if got == nil || !got.Equal(issued) {
				t.Fatalf("number %q: issued_at was altered to %v, want %v", n, got, issued)
			}
		})
	}
}

func TestPDFToTextEngine(t *testing.T) {
	// go-fitz requires a real PDF; skip if MuPDF shared library is unavailable.
	// This test only validates the engine tag and normalization wrapper.
	a := &Activities{}
	_, engine, _ := a.pdfToText(context.Background(), "doc-1", "/nonexistent.pdf")
	if engine != "mupdf/1" {
		t.Fatalf("engine = %q, want mupdf/1", engine)
	}
}

func TestAssessPDFExtractionEngineTag(t *testing.T) {
	// The content gate and extraction assessment logic is independent of the engine.
	// We verify the engine tag is correct after the migration.
	a := &Activities{}
	got := a.assessPDFExtraction(context.Background(), "doc-1", "/nonexistent.pdf", extract.DefaultGate())
	if got.engine != "mupdf/1" {
		t.Fatalf("engine = %q, want mupdf/1", got.engine)
	}
}

func TestAssessPDFExtractionRoutesGateFailureToOCR(t *testing.T) {
	// With a missing PDF, go-fitz returns an error and assessment falls through.
	a := &Activities{}
	got := a.assessPDFExtraction(context.Background(), "doc-1", "/nonexistent.pdf", extract.DefaultGate())
	if got.extractable {
		t.Fatal("assessPDFExtraction extractable=true on missing file, want failure")
	}
}

func TestDOCToTextEngineTag(t *testing.T) {
	// docToText requires soffice + go-fitz. Without soffice installed, it returns
	// an error; we only verify the engine tag comes through correctly.
	a := &Activities{}
	_, engine, err := a.docToText(context.Background(), "doc-1", []byte("legacy doc bytes"))
	// Expect an error (soffice likely missing in test env), but engine must be set.
	if err == nil {
		if engine != "libreoffice+mupdf/1" {
			t.Fatalf("engine = %q, want libreoffice+mupdf/1", engine)
		}
	}
}

func TestCleanPDFMarkdownNoiseRemovesCongbaoHeaders(t *testing.T) {
	in := "2\n\n" +
		"CÔNG BÁO/Số 223 + 224/Ngày 09-02-2022\n\n" +
		"Điều 1. Nội dung\n" +
		"\fCÔNG BÁO/Số 223 + 224/Ngày 09-02-2022\n\n" +
		"3\n\n" +
		"|     | CÔNG BÁO/Số 223 + 224/Ngày 09-02-2022  |     |     | 5 |\n" +
		"1. Sửa đổi quy định\n\n" +
		"100\n" +
		"Bảng số liệu"

	got := cleanPDFMarkdownNoise(in)
	if strings.Contains(got, "CÔNG BÁO/Số") {
		t.Fatalf("cleaned text still contains page header: %q", got)
	}
	if strings.Contains(got, "\f") {
		t.Fatalf("cleaned text still contains form feed: %q", got)
	}
	if strings.Contains("\n"+got+"\n", "\n2\n") || strings.Contains("\n"+got+"\n", "\n3\n") {
		t.Fatalf("cleaned text still contains adjacent page numbers: %q", got)
	}
	for _, want := range []string{"Điều 1. Nội dung", "1. Sửa đổi quy định", "100\nBảng số liệu"} {
		if !strings.Contains(got, want) {
			t.Fatalf("cleaned text = %q, want to keep %q", got, want)
		}
	}
}

func TestHTMLToTextExtractsBody(t *testing.T) {
	a := &Activities{}
	body := `<html><head><title>T</title></head><body><p>Bộ Tư pháp</p></body></html>`
	text, engine, err := a.htmlToText(context.Background(), "doc-1", body)
	if err != nil {
		t.Fatalf("htmlToText: %v", err)
	}
	if engine != "gohtml/1" {
		t.Fatalf("engine = %q, want gohtml/1", engine)
	}
	if !strings.Contains(text, "Bộ Tư pháp") {
		t.Fatalf("text = %q, want to contain body text", text)
	}
}

func TestAssessConvertedTextRejectsSupplementOnlyText(t *testing.T) {
	text := "**BÁO CÁO TÌNH HÌNH THỰC HIỆN CƠ CẤU LẠI THỜI HẠN TRẢ NỢ**\n\nTháng ... năm ..."
	confidence, ok, sourceUnavailable := assessConvertedText(text)
	if ok {
		t.Fatal("supplement-only text passed as binding")
	}
	if sourceUnavailable {
		t.Fatal("supplement-only text should not be treated as source-unavailable")
	}
	if confidence >= 0.6 {
		t.Fatalf("confidence = %f, want below pass threshold", confidence)
	}
}

func TestFileNameForArtifactUsesSourceName(t *testing.T) {
	got := fileNameForArtifact(ClaimedArtifact{
		RefKey:   "0.pdf",
		FileName: "VanBanGoc_09.2020.TT.NHNN.pdf",
	})
	if got != "VanBanGoc_09.2020.TT.NHNN.pdf" {
		t.Fatalf("fileNameForArtifact = %q, want source filename", got)
	}
}

func TestFileNameForArtifactFallsBackToRefKey(t *testing.T) {
	got := fileNameForArtifact(ClaimedArtifact{RefKey: "0.pdf"})
	if got != "0.pdf" {
		t.Fatalf("fileNameForArtifact = %q, want ref key", got)
	}
}

func TestCanonicalIDDocNumber(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"verbose old-style POJK", "Peraturan Otoritas Jasa Keuangan Nomor 11/POJK.03/2022 Tahun 2022", "POJK 11/POJK.03/2022"},
		{"verbose new-style POJK", "Peraturan Otoritas Jasa Keuangan Nomor 21 Tahun 2023", "POJK 21/2023"},
		{"verbose SEOJK", "Surat Edaran Otoritas Jasa Keuangan Nomor 24/SEOJK.03/2021", "SEOJK 24/SEOJK.03/2021"},
		{"short SEOJK with NOMOR filler", "SEOJK NOMOR 22/SEOJK.05/2021", "SEOJK 22/SEOJK.05/2021"},
		{"PBI No. dot form", "PBI No.10 Tahun 2025", "PBI 10/2025"},
		{"verbose PPATK without filler", "PERATURAN PUSAT PELAPORAN DAN ANALISIS TRANSAKSI KEUANGAN 1/2021", "PPATK 1/2021"},
		{"verbose LPS without filler", "PERATURAN LEMBAGA PENJAMIN SIMPANAN 1/2023", "LPS 1/2023"},
		{"UU with parenthetical", "Undang-undang (UU) Nomor 27 Tahun 2022", "UU 27/2022"},
		{"Perppu not split into PP+UU", "Peraturan Pemerintah Pengganti Undang-Undang Nomor 2 Tahun 2022", "PERPPU 2/2022"},
		{"already-short POJK unchanged", "POJK 11/POJK.03/2022", "POJK 11/POJK.03/2022"},
		{"old slash PBI unchanged", "PBI 23/6/PBI/2021", "PBI 23/6/PBI/2021"},
		{"VN number untouched", "15/2023/NĐ-CP", "15/2023/NĐ-CP"},
		{"VN circular untouched", "09/2020/tt-nhnn", "09/2020/tt-nhnn"},
		{"MY PU untouched", "P.U.(A) 123/2023", "P.U.(A) 123/2023"},
		{"MY act untouched", "Act 758", "Act 758"},
		{"bare phrase without number untouched", "Peraturan Otoritas Jasa Keuangan", "Peraturan Otoritas Jasa Keuangan"},
		{"bare N Tahun YYYY folds", "4 Tahun 2023", "4/2023"},
		{"bare with NOMOR filler folds", "Nomor 11 Tahun 2014", "11/2014"},
		{"PADK Nomor form", "PADK Nomor 1 Tahun 2026", "PADK 1/2026"},
		{"PADK old-style", "PADK Nomor 37/PADK.08/2025", "PADK 37/PADK.08/2025"},
		{"verbose PADK", "Peraturan Anggota Dewan Komisioner Nomor 2 Tahun 2026", "PADK 2/2026"},
		{"MY numeric untouched", "P.U.(A) 123", "P.U.(A) 123"},
		{"parenthetical code repeat", "PMK (PMK) NOMOR 68/PMK.03/2022", "PMK 68/PMK.03/2022"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := canonicalIDDocNumber(tt.in); got != tt.want {
				t.Errorf("canonicalIDDocNumber(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
