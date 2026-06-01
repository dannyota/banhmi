package pipeline

import (
	"strings"
	"testing"

	dbsilver "danny.vn/banhmi/pkg/store/silver"
)

// makeSection is a test helper that builds a SilverDocumentSection.
func makeSection(id int64, parentID *int64, kind string, ordinal int32, label, heading, content, citPath string) dbsilver.SilverDocumentSection {
	var lp, hp, cp *string
	if label != "" {
		lp = &label
	}
	if heading != "" {
		hp = &heading
	}
	if content != "" {
		cp = &content
	}
	return dbsilver.SilverDocumentSection{
		ID:           id,
		ParentID:     parentID,
		Kind:         kind,
		Ordinal:      ordinal,
		Label:        lp,
		Heading:      hp,
		Content:      cp,
		CitationPath: citPath,
	}
}

// sectionID is a pointer helper for test cases.
func sectionID(id int64) *int64 { return &id }

// TestBuildPrefix_components verifies that buildPrefix assembles all expected
// components and handles empty fields gracefully.
func TestBuildPrefix_components(t *testing.T) {
	cases := []struct {
		name, docNum, title, chuong, muc, eff, wantContains string
		wantMissing                                         string
	}{
		{
			name:         "full",
			docNum:       "11/2026/TT-NHNN",
			title:        "Thông tư 11",
			chuong:       "Chương I QUY ĐỊNH CHUNG",
			muc:          "",
			eff:          "01/01/2026",
			wantContains: "11/2026/TT-NHNN",
		},
		{
			name:         "no_doc_number",
			docNum:       "",
			title:        "Thông tư về cho vay",
			chuong:       "",
			muc:          "",
			eff:          "15/03/2025",
			wantContains: "Thông tư về cho vay",
		},
		{
			name:         "with_muc",
			docNum:       "01/QĐ",
			title:        "Quyết định",
			chuong:       "Chương II",
			muc:          "Mục 1 Quy định",
			eff:          "",
			wantContains: "Mục 1 Quy định",
			wantMissing:  "Có hiệu lực",
		},
		{
			name:         "empty",
			docNum:       "",
			title:        "",
			chuong:       "",
			muc:          "",
			eff:          "",
			wantContains: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := buildPrefix(tc.docNum, tc.title, tc.chuong, tc.muc, tc.eff)
			if tc.wantContains != "" && !strings.Contains(p, tc.wantContains) {
				t.Errorf("prefix %q missing %q", p, tc.wantContains)
			}
			if tc.wantMissing != "" && strings.Contains(p, tc.wantMissing) {
				t.Errorf("prefix %q should not contain %q", p, tc.wantMissing)
			}
		})
	}
}

func TestBuildPrefixCapsLongFields(t *testing.T) {
	longTitle := strings.Repeat("Quy định rất dài ", 40)
	got := buildPrefix("11/2026/TT-NHNN", longTitle, "", "", "")
	if len([]rune(got)) > len([]rune("11/2026/TT-NHNN: "))+maxPrefixFieldRunes {
		t.Fatalf("prefix length = %d, want capped field: %q", len([]rune(got)), got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("prefix = %q, want ellipsis", got)
	}
}

// TestLabelStr verifies the label helper falls back to citation_path.
func TestLabelStr(t *testing.T) {
	sec := makeSection(1, nil, "dieu", 1, "Điều 7", "", "", "dieu-7")
	if got := labelStr(&sec); got != "Điều 7" {
		t.Errorf("labelStr = %q, want %q", got, "Điều 7")
	}

	// Fallback: no label → use last citation_path segment.
	sec2 := makeSection(2, nil, "dieu", 2, "", "", "", "chuong-I/dieu-2")
	if got := labelStr(&sec2); got != "dieu-2" {
		t.Errorf("labelStr fallback = %q, want %q", got, "dieu-2")
	}
}

// TestRoughTokenCount_estimates verifies the estimator is monotone for
// increasing text lengths.
func TestRoughTokenCount_estimates(t *testing.T) {
	texts := []string{
		"",
		"a",
		"Điều 7",
		"Điều 7 Phạm vi điều chỉnh của thông tư này áp dụng với các tổ chức tín dụng.",
		strings.Repeat("Ngân hàng nhà nước Việt Nam ", 20),
	}
	prev := -1
	for _, txt := range texts {
		tc := roughTokenCount(txt)
		if tc < 0 {
			t.Errorf("roughTokenCount(%q) = %d < 0", txt, tc)
		}
		if tc < prev {
			t.Errorf("roughTokenCount not monotone: %d < %d for %q", tc, prev, txt)
		}
		prev = tc
	}
}

// TestKhoanContent verifies that khoanContent includes the label and body.
func TestKhoanContent(t *testing.T) {
	k := makeSection(10, sectionID(1), "khoan", 1, "1.", "", "Nội dung khoản một.", "dieu-1/khoan-1")
	got := khoanContent(&k)
	if !strings.Contains(got, "1.") {
		t.Errorf("khoanContent %q missing label", got)
	}
	if !strings.Contains(got, "Nội dung khoản một.") {
		t.Errorf("khoanContent %q missing body", got)
	}
}

func TestSectionTreeContentIncludesChildren(t *testing.T) {
	dieu := makeSection(1, nil, "dieu", 1, "Điều 1", "Phạm vi điều chỉnh", "Nội dung mở đầu.", "dieu-1")
	khoan := makeSection(2, sectionID(1), "khoan", 1, "1.", "", "Nội dung khoản.", "dieu-1/khoan-1")
	diem := makeSection(3, sectionID(2), "diem", 1, "a)", "", "Nội dung điểm.", "dieu-1/khoan-1/diem-a")
	sections := []dbsilver.SilverDocumentSection{dieu, khoan, diem}

	got := sectionTreeContent(&sections[0], buildChildrenByParent(sections))
	for _, want := range []string{"Điều 1. Phạm vi điều chỉnh", "1. Nội dung khoản.", "a) Nội dung điểm."} {
		if !strings.Contains(got, want) {
			t.Fatalf("sectionTreeContent missing %q in:\n%s", want, got)
		}
	}
}

func TestSplitLongChunkContent(t *testing.T) {
	content := strings.Join([]string{
		strings.Repeat("Nội dung pháp lý ", 80),
		strings.Repeat("Quy định xử phạt ", 80),
		strings.Repeat("Thẩm quyền áp dụng ", 80),
	}, "\n")

	parts := splitLongChunkContent(content, 80)
	if len(parts) < 3 {
		t.Fatalf("parts = %d, want split content", len(parts))
	}
	for i, part := range parts {
		if strings.TrimSpace(part) == "" {
			t.Fatalf("part %d is empty", i)
		}
		if got := roughTokenCount(part); got > 80 {
			t.Fatalf("part %d tokens = %d, want <= 80", i, got)
		}
	}
}

func TestChunkRecordBatchesCapsBatchSize(t *testing.T) {
	chunks := make([]chunkRecord, 65)
	for i := range chunks {
		chunks[i] = chunkRecord{id: int64(i + 1)}
	}

	got := chunkRecordBatches(chunks, 32)
	if len(got) != 3 {
		t.Fatalf("batches = %d, want 3", len(got))
	}
	if len(got[0]) != 32 || len(got[1]) != 32 || len(got[2]) != 1 {
		t.Fatalf("batch sizes = %d/%d/%d, want 32/32/1", len(got[0]), len(got[1]), len(got[2]))
	}
	if got[2][0].id != 65 {
		t.Fatalf("last chunk id = %d, want 65", got[2][0].id)
	}
}

func TestFallbackChunkSectionsPrefersLegacyKhoan(t *testing.T) {
	chuong := makeSection(1, nil, "chuong", 1, "I.", "VẬN DỤNG CÁC TIÊU CHUẨN", "", "chuong-I")
	muc := makeSection(2, sectionID(1), "muc", 1, "A.", "Tổ chức thực hiện", "", "chuong-I/muc-A")
	khoan1 := makeSection(3, sectionID(2), "khoan", 1, "1.", "", "Nội dung một.", "chuong-I/muc-A/khoan-1")
	khoan2 := makeSection(4, sectionID(2), "khoan", 2, "2.", "", "Nội dung hai.", "chuong-I/muc-A/khoan-2")
	sections := []dbsilver.SilverDocumentSection{chuong, muc, khoan1, khoan2}

	got := fallbackChunkSections(sections, buildChildrenByParent(sections))
	if len(got) != 2 {
		t.Fatalf("fallback chunks = %d, want 2", len(got))
	}
	if got[0].ID != 3 || got[1].ID != 4 {
		t.Fatalf("fallback chunk ids = %d/%d, want 3/4", got[0].ID, got[1].ID)
	}
}

func TestSectionCitationIncludesAncestors(t *testing.T) {
	chuong := makeSection(1, nil, "chuong", 1, "I.", "VẬN DỤNG CÁC TIÊU CHUẨN", "", "chuong-I")
	muc := makeSection(2, sectionID(1), "muc", 1, "A.", "Tổ chức thực hiện", "", "chuong-I/muc-A")
	khoan := makeSection(3, sectionID(2), "khoan", 1, "1.", "", "Nội dung.", "chuong-I/muc-A/khoan-1")
	sections := []dbsilver.SilverDocumentSection{chuong, muc, khoan}
	byID := map[int64]*dbsilver.SilverDocumentSection{}
	for i := range sections {
		byID[sections[i].ID] = &sections[i]
	}

	got := sectionCitation(&sections[2], byID)
	want := "Chương I, Mục A, Khoản 1"
	if got != want {
		t.Fatalf("sectionCitation = %q, want %q", got, want)
	}
}

func TestSectionCitationPartIsConcise(t *testing.T) {
	dieu := makeSection(1, nil, "dieu", 1, "Điều 16", "Mở tài khoản thanh toán bằng phương tiện điện tử", "", "dieu-16")
	if got := sectionCitationPart(&dieu); got != "Điều 16" {
		t.Fatalf("sectionCitationPart dieu = %q, want Điều 16", got)
	}

	khoan := makeSection(2, sectionID(1), "khoan", 3, "3.", "Không dùng trong citation", "", "dieu-16/khoan-3")
	if got := sectionCitationPart(&khoan); got != "Khoản 3" {
		t.Fatalf("sectionCitationPart khoan = %q, want Khoản 3", got)
	}
}

func TestLabelOnlyChunk(t *testing.T) {
	dieu := makeSection(1, nil, "dieu", 1, "Điều 16", "", "", "dieu-16")
	if !labelOnlyChunk(&dieu, "Điều 16", "Điều 16.") {
		t.Fatal("labelOnlyChunk = false, want true for bare Điều label")
	}
	if labelOnlyChunk(&dieu, "Điều 16", "Điều 16. Mở tài khoản thanh toán") {
		t.Fatal("labelOnlyChunk = true, want false when a heading/content is present")
	}

	khoan := makeSection(2, sectionID(1), "khoan", 3, "3.", "", "", "dieu-16/khoan-3")
	if !labelOnlyChunk(&khoan, "Điều 16, Khoản 3", "Điều 16, Khoản 3") {
		t.Fatal("labelOnlyChunk = false, want true for bare emitted citation")
	}
}
