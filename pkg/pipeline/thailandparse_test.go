package pipeline

import (
	"testing"
)

// ---- helpers (reuse collect/findByPath from malaysiaparse_test.go) ---------

// thCollect walks a section tree and collects labels for nodes of the given kind.
func thCollect(secs []Section, kind string, out *[]string) {
	for _, s := range secs {
		if s.Kind == kind {
			*out = append(*out, s.Label)
		}
		thCollect(s.Children, kind, out)
	}
}

func thFindByPath(secs []Section, path string) *Section {
	for i := range secs {
		if secs[i].CitationPath == path {
			return &secs[i]
		}
		if got := thFindByPath(secs[i].Children, path); got != nil {
			return got
		}
	}
	return nil
}

// ---- Thai numeral conversion ----------------------------------------------

func TestThaiToArabic(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"๐", "0"},
		{"๑๒๓", "123"},
		{"๔๕๖๗๘๙", "456789"},
		{"abc", "abc"},
		{"มาตรา ๕", "มาตรา 5"},
		{"(๑๐)", "(10)"},
		{"42", "42"},
	}
	for _, tt := range tests {
		got := thaiToArabic(tt.in)
		if got != tt.want {
			t.Errorf("thaiToArabic(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestThaiParseInt(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"๑", 1},
		{"๑๐", 10},
		{"42", 42},
		{"๐", 0},
		{"abc", -1},
	}
	for _, tt := range tests {
		got := thaiParseInt(tt.in)
		if got != tt.want {
			t.Errorf("thaiParseInt(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

// ---- Act parser -----------------------------------------------------------

const thTestAct = `หมวด ๑ บททั่วไป

มาตรา ๑ พระราชบัญญัตินี้เรียกว่า
มาตรา ๒ พระราชบัญญัตินี้ให้ใช้บังคับ
มาตรา ๓ ในพระราชบัญญัตินี้

หมวด ๒ การประกอบธุรกิจ

ส่วนที่ ๑ การขออนุญาต

มาตรา ๔ ผู้ใดประสงค์จะประกอบธุรกิจ
(๑) ยื่นคำขออนุญาต
(๒) แนบเอกสารหลักฐาน
มาตรา ๕ ผู้ขออนุญาตต้อง

ส่วนที่ ๒ การกำกับดูแล

มาตรา ๖ ธนาคารแห่งประเทศไทยมีอำนาจ
`

func TestParseThaiAct_structure(t *testing.T) {
	roots := ParseThaiAct(thTestAct)

	// Two chapters.
	var chapters []string
	thCollect(roots, "chapter", &chapters)
	if len(chapters) != 2 {
		t.Fatalf("chapters = %v, want 2", chapters)
	}
	if chapters[0] != "หมวด 1" || chapters[1] != "หมวด 2" {
		t.Fatalf("chapter labels = %v, want [หมวด 1, หมวด 2]", chapters)
	}

	// Chapter headings.
	ch1 := thFindByPath(roots, "chapter-1")
	if ch1 == nil {
		t.Fatal("missing chapter-1")
	}
	if ch1.Heading != "บททั่วไป" {
		t.Fatalf("chapter-1 heading = %q, want บททั่วไป", ch1.Heading)
	}

	// 6 sections (มาตรา 1..6).
	var secs []string
	thCollect(roots, "section", &secs)
	if len(secs) != 6 {
		t.Fatalf("sections = %v, want 6", secs)
	}
	for i := 1; i <= 6; i++ {
		want := "มาตรา " + thaiToArabic(string(rune('๐'+i)))
		// The label uses Arabic digits.
		if secs[i-1] != want {
			t.Errorf("section[%d] = %q, want %q", i-1, secs[i-1], want)
		}
	}

	// Two parts (ส่วนที่).
	var parts []string
	thCollect(roots, "part", &parts)
	if len(parts) != 2 {
		t.Fatalf("parts = %v, want 2", parts)
	}

	// มาตรา 4 has numbered items (๑) and (๒).
	s4 := thFindByPath(roots, "chapter-2/part-1/section-4")
	if s4 == nil {
		t.Fatal("missing section-4")
	}
	var items []string
	thCollect(s4.Children, "paragraph", &items)
	if len(items) != 2 {
		t.Fatalf("section 4 items = %v, want 2", items)
	}
	if items[0] != "(1)" || items[1] != "(2)" {
		t.Fatalf("item labels = %v, want [(1) (2)]", items)
	}
}

func TestParseThaiAct_arabicNumerals(t *testing.T) {
	text := `หมวด 1 บททั่วไป

มาตรา 1 ข้อความ
มาตรา 2 ข้อความ
`
	roots := ParseThaiAct(text)

	var secs []string
	thCollect(roots, "section", &secs)
	if len(secs) != 2 {
		t.Fatalf("sections = %v, want 2", secs)
	}
}

func TestParseThaiAct_amendmentSuffix(t *testing.T) {
	text := `มาตรา ๑ ข้อความ
มาตรา ๒ ข้อความ
มาตรา ๓ ข้อความ
มาตรา ๔ ข้อความ
มาตรา ๕ ข้อความ
มาตรา ๕ ทวิ เพิ่มเติมโดย
มาตรา ๕ ตรี เพิ่มเติมโดย
มาตรา ๖ ข้อความ
`
	roots := ParseThaiAct(text)

	var secs []string
	thCollect(roots, "section", &secs)
	if len(secs) != 8 {
		t.Fatalf("sections count = %d, want 8; labels = %v", len(secs), secs)
	}
	// Check the amendment suffixes are preserved in labels.
	if secs[5] != "มาตรา 5 ทวิ" {
		t.Errorf("section[5] = %q, want 'มาตรา 5 ทวิ'", secs[5])
	}
	if secs[6] != "มาตรา 5 ตรี" {
		t.Errorf("section[6] = %q, want 'มาตรา 5 ตรี'", secs[6])
	}

	// Citation paths for amendment suffixes.
	s5bis := thFindByPath(roots, "section-5/1")
	if s5bis == nil {
		t.Fatal("missing section-5/1 (ทวิ)")
	}
	s5ter := thFindByPath(roots, "section-5/2")
	if s5ter == nil {
		t.Fatal("missing section-5/2 (ตรี)")
	}
}

// ---- BOT notification parser ----------------------------------------------

const thTestBOT = `ประกาศธนาคารแห่งประเทศไทย

ข้อ ๑ ประกาศนี้ใช้บังคับ
ข้อ ๒ ในประกาศนี้
(๑) คำว่า "สถาบันการเงิน"
(๒) คำว่า "ระบบ"
ข้อ ๓ สถาบันการเงินต้อง
`

func TestParseThaiAct_BOTNotification(t *testing.T) {
	roots := ParseThaiAct(thTestBOT)

	// 3 clauses (ข้อ).
	var secs []string
	thCollect(roots, "section", &secs)
	if len(secs) != 3 {
		t.Fatalf("clauses = %v, want 3", secs)
	}
	if secs[0] != "ข้อ 1" || secs[1] != "ข้อ 2" || secs[2] != "ข้อ 3" {
		t.Fatalf("clause labels = %v", secs)
	}

	// ข้อ 2 has two numbered items.
	s2 := thFindByPath(roots, "section-2")
	if s2 == nil {
		t.Fatal("missing section-2")
	}
	var items []string
	thCollect(s2.Children, "paragraph", &items)
	if len(items) != 2 {
		t.Fatalf("clause 2 items = %v, want 2", items)
	}
}

func TestParseThaiAct_BOTArabicNumerals(t *testing.T) {
	text := `ข้อ 1 ข้อแรก
ข้อ 2 ข้อสอง
ข้อ 3 ข้อสาม
`
	roots := ParseThaiAct(text)

	var secs []string
	thCollect(roots, "section", &secs)
	if len(secs) != 3 {
		t.Fatalf("clauses = %v, want 3", secs)
	}
}

// ---- full-text fallback ---------------------------------------------------

func TestParseThaiAct_fullTextFallback(t *testing.T) {
	// Text with no มาตรา or ข้อ markers → empty result (caller adds fallback).
	text := `สถาบันการเงินต้องดำเนินการ
ตามหลักเกณฑ์ที่กำหนด`
	roots := ParseThaiAct(text)
	if len(roots) != 0 {
		t.Fatalf("expected empty roots for unstructured text, got %d", len(roots))
	}
}

// ---- page noise stripping -------------------------------------------------

func TestThaiPageNoise(t *testing.T) {
	text := `๓
มาตรา ๑ ข้อความ
42
มาตรา ๒ ข้อความ
`
	roots := ParseThaiAct(text)

	var secs []string
	thCollect(roots, "section", &secs)
	if len(secs) != 2 {
		t.Fatalf("sections = %v, want 2 (page numbers should be stripped)", secs)
	}
}

// ---- transitional provisions and notes ------------------------------------

func TestParseThaiAct_transitionalAndNotes(t *testing.T) {
	text := `มาตรา ๑ ข้อความ
มาตรา ๒ ข้อความ

บทเฉพาะกาล

มาตรา ๓ บทเฉพาะกาลนี้

หมายเหตุ :- เหตุผลในการประกาศใช้
`
	roots := ParseThaiAct(text)

	// 2 sections before transitional, 1 in transitional, and notes heading.
	var secs []string
	thCollect(roots, "section", &secs)
	if len(secs) != 3 {
		t.Fatalf("sections = %v, want 3", secs)
	}

	// Transitional is a chapter node.
	var chapters []string
	thCollect(roots, "chapter", &chapters)
	if len(chapters) != 1 || chapters[0] != "บทเฉพาะกาล" {
		t.Fatalf("chapters = %v, want [บทเฉพาะกาล]", chapters)
	}

	// Notes heading exists.
	var headings []string
	thCollect(roots, "heading", &headings)
	if len(headings) != 1 {
		t.Fatalf("headings = %v, want 1 (หมายเหตุ)", headings)
	}
}

// ---- isBOTNotification detection ------------------------------------------

func TestIsBOTNotification(t *testing.T) {
	actLines := thBodyLines(thTestAct)
	if isBOTNotification(actLines) {
		t.Fatal("Act text should not be detected as BOT notification")
	}

	botLines := thBodyLines(thTestBOT)
	if !isBOTNotification(botLines) {
		t.Fatal("BOT text should be detected as BOT notification")
	}
}
