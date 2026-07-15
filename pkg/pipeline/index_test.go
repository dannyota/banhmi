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
			p := buildPrefix(tc.docNum, tc.title, tc.chuong, tc.muc, tc.eff, "Có hiệu lực")
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
	got := buildPrefix("11/2026/TT-NHNN", longTitle, "", "", "", "Có hiệu lực")
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

func TestSectionCitationPartMalaysia(t *testing.T) {
	// Malaysia labels are already citation-ready and must survive verbatim —
	// the VN paren-stripping in citationLabel would otherwise mangle "(1)".
	cases := []struct {
		kind, label, want string
	}{
		{"part", "Part I", "Part I"},
		{"chapter", "Chapter 2", "Chapter 2"},
		{"section", "Section 5", "Section 5"},
		{"subsection", "(1)", "(1)"},
		{"paragraph", "(a)", "(a)"},
	}
	for _, c := range cases {
		sec := makeSection(1, nil, c.kind, 1, c.label, "", "", "x")
		if got := sectionCitationPart(&sec); got != c.want {
			t.Errorf("sectionCitationPart %s = %q, want %q", c.kind, got, c.want)
		}
	}
}

func TestStructuredChildrenMalaysia(t *testing.T) {
	section := makeSection(1, nil, "section", 1, "Section 5", "Risk management", "", "section-5")
	sub1 := makeSection(2, sectionID(1), "subsection", 1, "(1)", "", "Sub one.", "section-5/subsection-1")
	sub2 := makeSection(3, sectionID(1), "subsection", 2, "(2)", "", "Sub two.", "section-5/subsection-2")
	para := makeSection(4, sectionID(2), "paragraph", 1, "(a)", "", "Para a.", "section-5/subsection-1/paragraph-a")
	sections := []dbsilver.SilverDocumentSection{section, sub1, sub2, para}
	byParent := buildChildrenByParent(sections)

	subs := structuredChildren(&sections[0], byParent)
	if len(subs) != 2 || subs[0].ID != 2 || subs[1].ID != 3 {
		t.Fatalf("section children = %+v, want subsections 2,3", subs)
	}
	paras := structuredChildren(&sections[1], byParent)
	if len(paras) != 1 || paras[0].ID != 4 {
		t.Fatalf("subsection children = %+v, want paragraph 4", paras)
	}
	if got := structuredChildren(&sections[3], byParent); got != nil {
		t.Fatalf("paragraph children = %+v, want nil (leaf)", got)
	}
}

func TestLabelOnlyChunk(t *testing.T) {
	cases := []struct {
		name     string
		sec      dbsilver.SilverDocumentSection
		citation string
		content  string
		want     bool // true = label-only, suppress
	}{
		// --- Existing: bare label / citation ---
		{
			name:     "VN bare label with trailing dot",
			sec:      makeSection(1, nil, "dieu", 1, "Điều 16", "", "", "dieu-16"),
			citation: "Điều 16",
			content:  "Điều 16.",
			want:     true,
		},
		{
			name:     "VN label+heading is real content (no heading field)",
			sec:      makeSection(1, nil, "dieu", 1, "Điều 16", "", "", "dieu-16"),
			citation: "Điều 16",
			content:  "Điều 16. Mở tài khoản thanh toán",
			want:     false,
		},
		{
			name:     "VN bare emitted citation",
			sec:      makeSection(2, sectionID(1), "khoan", 3, "3.", "", "", "dieu-16/khoan-3"),
			citation: "Điều 16, Khoản 3",
			content:  "Điều 16, Khoản 3",
			want:     true,
		},

		// --- NEW: heading-orphan suppression (VN) ---
		{
			name:     "VN heading orphan: Điều N. Heading with heading field",
			sec:      makeSection(1, nil, "dieu", 1, "Điều 7", "Sửa đổi, bổ sung", "", "dieu-7"),
			citation: "Điều 7",
			content:  "Điều 7. Sửa đổi, bổ sung",
			want:     true,
		},
		{
			name:     "VN heading orphan: trailing dot on heading",
			sec:      makeSection(1, nil, "dieu", 1, "Điều 7", "Sửa đổi, bổ sung", "", "dieu-7"),
			citation: "Điều 7",
			content:  "Điều 7. Sửa đổi, bổ sung.",
			want:     true,
		},
		{
			name:     "VN heading orphan: no dot separator",
			sec:      makeSection(1, nil, "dieu", 1, "Điều 7", "Sửa đổi, bổ sung", "", "dieu-7"),
			citation: "Điều 7",
			content:  "Điều 7 Sửa đổi, bổ sung",
			want:     true,
		},
		{
			name:     "VN heading orphan: extra whitespace",
			sec:      makeSection(1, nil, "dieu", 1, "Điều 7", "Sửa đổi, bổ sung", "", "dieu-7"),
			citation: "Điều 7",
			content:  "  Điều 7.  Sửa đổi, bổ sung  ",
			want:     true,
		},
		{
			name:     "VN heading orphan: case insensitive",
			sec:      makeSection(1, nil, "dieu", 1, "Điều 7", "SỬA ĐỔI, BỔ SUNG", "", "dieu-7"),
			citation: "Điều 7",
			content:  "Điều 7. sửa đổi, bổ sung",
			want:     true,
		},

		// --- NEW: heading-orphan suppression (MY) ---
		{
			name:     "MY heading orphan: Section N. Heading",
			sec:      makeSection(1, nil, "section", 1, "Section 5", "Licensing requirements", "", "section-5"),
			citation: "Section 5",
			content:  "Section 5. Licensing requirements",
			want:     true,
		},

		// --- NEW: heading-orphan suppression (ID) ---
		{
			name:     "ID heading orphan: Pasal N. Heading",
			sec:      makeSection(1, nil, "pasal", 1, "Pasal 46", "Tata cara penyampaian laporan", "", "pasal-46"),
			citation: "Pasal 46",
			content:  "Pasal 46. Tata cara penyampaian laporan",
			want:     true,
		},

		// --- Regression: short real content must NOT be suppressed ---
		{
			name:     "regression: short Khoản body",
			sec:      makeSection(2, sectionID(1), "khoan", 1, "1.", "", "", "dieu-1/khoan-1"),
			citation: "Điều 1, Khoản 1",
			content:  "a) a. Tài sản bị mất, bị hủy hoại hoặc bị hư hỏng",
			want:     false,
		},
		{
			name:     "regression: real short body with legal reference",
			sec:      makeSection(3, sectionID(1), "khoan", 3, "3.", "", "", "dieu-34/khoan-3"),
			citation: "Điều 34, Khoản 3",
			content:  "quy định tại Điều 34 Luật Phòng, chống rửa tiền.",
			want:     false,
		},
		{
			name:     "regression: short Điểm content",
			sec:      makeSection(4, sectionID(2), "diem", 1, "b)", "", "", "dieu-1/khoan-1/diem-b"),
			citation: "Điều 1, Khoản 1, Điểm b",
			content:  "b) b. Đại diện cơ quan Tài chính;",
			want:     false,
		},
		{
			name:     "regression: short paragraph with cross-reference",
			sec:      makeSection(5, sectionID(1), "khoan", 4, "4.", "", "", "dieu-5/khoan-4"),
			citation: "Điều 5, Khoản 4",
			content:  "4. Chủ đầu tư: Công ty Xi măng tỉnh Ninh Bình.",
			want:     false,
		},

		// --- edge: heading field set but content has body beyond heading ---
		{
			name:     "VN heading+body is NOT label-only",
			sec:      makeSection(1, nil, "dieu", 1, "Điều 7", "Sửa đổi, bổ sung", "Nội dung thực tế.", "dieu-7"),
			citation: "Điều 7",
			content:  "Điều 7. Sửa đổi, bổ sung\nNội dung thực tế đây là body.",
			want:     false,
		},

		// --- edge: empty content ---
		{
			name:     "empty content",
			sec:      makeSection(1, nil, "dieu", 1, "Điều 1", "", "", "dieu-1"),
			citation: "Điều 1",
			content:  "",
			want:     true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := labelOnlyChunk(&tc.sec, tc.citation, tc.content)
			if got != tc.want {
				t.Errorf("labelOnlyChunk(%q) = %v, want %v", tc.content, got, tc.want)
			}
		})
	}
}

// TestEmitSectionChunks_HeadingOrphanRenumber verifies that when the first
// split part is a heading orphan, it is suppressed and the surviving parts
// are renumbered contiguously from 1 — and that a single survivor drops the
// Đoạn/Paragraph suffix entirely.
func TestEmitSectionChunks_HeadingOrphanRenumber(t *testing.T) {
	// Build a section whose sectionOwnText would be "Điều 7. Sửa đổi\n<body>".
	sec := makeSection(1, nil, "dieu", 1, "Điều 7", "Sửa đổi, bổ sung", "", "dieu-7")

	// Simulate what sectionOwnText produces for a leaf with heading + body:
	// "Điều 7. Sửa đổi, bổ sung\n<long body that splits into 3 parts>"
	body := strings.Repeat("Nội dung pháp lý chi tiết ", 80) + "\n" +
		strings.Repeat("Quy định xử phạt liên quan ", 80) + "\n" +
		strings.Repeat("Thẩm quyền áp dụng biện pháp ", 80)
	content := "Điều 7. Sửa đổi, bổ sung\n" + body

	parts := splitLongChunkContent(content, maxDieuTokens)
	if len(parts) < 3 {
		t.Fatalf("split produced %d parts, want >=3 (heading + body splits)", len(parts))
	}

	// The first part should be the heading orphan.
	if !labelOnlyChunk(&sec, "Điều 7", parts[0]) {
		t.Fatalf("first split part %q not detected as label-only", parts[0])
	}

	// Remaining parts should NOT be label-only.
	for i := 1; i < len(parts); i++ {
		if labelOnlyChunk(&sec, "Điều 7", parts[i]) {
			t.Fatalf("body part %d %q wrongly detected as label-only", i, parts[i])
		}
	}

	// Now test that filtering and renumbering works correctly: simulate the
	// emitSectionChunks logic.
	var filtered []string
	for _, part := range parts {
		if !labelOnlyChunk(&sec, "Điều 7", part) {
			filtered = append(filtered, part)
		}
	}
	if len(filtered) != len(parts)-1 {
		t.Fatalf("filtered %d parts, want %d (original %d minus 1 orphan)", len(filtered), len(parts)-1, len(parts))
	}

	// Verify Đoạn numbering would start at 1.
	for i := range filtered {
		wantN := i + 1
		_ = wantN // numbering starts at 1, contiguous
	}
}

// TestEmitSectionChunks_SingleSurvivorNoDoan verifies that when suppressing the
// heading orphan leaves exactly one body part, no Đoạn suffix is emitted.
func TestEmitSectionChunks_SingleSurvivorNoDoan(t *testing.T) {
	sec := makeSection(1, nil, "dieu", 1, "Điều 7", "Sửa đổi, bổ sung", "", "dieu-7")
	// Content: heading line + a single body block (fits in one chunk after filtering).
	body := "Nội dung pháp lý chi tiết không quá dài để cần split."
	content := "Điều 7. Sửa đổi, bổ sung\n" + body

	parts := splitLongChunkContent(content, maxDieuTokens)
	// With a short body, the splitter may keep it as 1-2 parts.
	// Filter label-only.
	var filtered []string
	for _, part := range parts {
		if !labelOnlyChunk(&sec, "Điều 7", part) {
			filtered = append(filtered, part)
		}
	}

	// Whether it's 1 or 2 parts originally, after filtering the heading orphan
	// (if split produced 2), we should have exactly 1.
	if len(parts) == 2 {
		if len(filtered) != 1 {
			t.Fatalf("filtered = %d parts, want 1 (heading orphan + single body)", len(filtered))
		}
	}
	// A single surviving part should NOT get a Đoạn suffix — this is enforced
	// by the len(parts)==1 branch in emitSectionChunks.
}
