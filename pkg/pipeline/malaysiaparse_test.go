package pipeline

import "testing"

// A compact Act that exercises the proven recipe: a front "Arrangement of
// Sections" TOC (must be skipped), the enacting clause, two Parts, sections with
// inline + standalone subsections and paragraphs, page-header noise, and a
// Schedule whose own 1./2. numbering must NOT be read as sections.
const myTestAct = `LAWS OF MALAYSIA

ARRANGEMENT OF SECTIONS

PART I
1. Short title
2. Interpretation
PART II
3. Powers

ENACTED by the Parliament of Malaysia as follows:

PART I
PRELIMINARY
Short title and commencement
1. (1) This Act may be cited as the Test Act 2026.
(2) This Act comes into operation on a date appointed by the Minister.
Interpretation
2. (1) In this Act, unless the context otherwise requires—
(a) the first defined term;
(b) the second defined term.
24
Laws of Malaysia
ACT 999
PART II
REGULATORY OBJECTIVES AND POWERS
3. The Bank shall regulate the matters set out in this Act.

FIRST SCHEDULE
1. This is a schedule paragraph, not section one.
2. This is another schedule paragraph.`

func collect(secs []Section, kind string, out *[]string) {
	for _, s := range secs {
		if s.Kind == kind {
			*out = append(*out, s.Label)
		}
		collect(s.Children, kind, out)
	}
}

func findByPath(secs []Section, path string) *Section {
	for i := range secs {
		if secs[i].CitationPath == path {
			return &secs[i]
		}
		if got := findByPath(secs[i].Children, path); got != nil {
			return got
		}
	}
	return nil
}

func TestParseMalaysianAct_structure(t *testing.T) {
	roots := ParseMalaysianAct(myTestAct)

	// Top level: Part I, Part II, and the Schedule (TOC before ENACTED is skipped).
	var parts []string
	collect(roots, "part", &parts)
	if len(parts) != 2 || parts[0] != "Part I" || parts[1] != "Part II" {
		t.Fatalf("parts = %v, want [Part I, Part II]", parts)
	}

	// Sections form the monotonic run 1..3 — the Schedule's 1./2. are NOT sections.
	var secs []string
	collect(roots, "section", &secs)
	if len(secs) != 3 || secs[0] != "Section 1" || secs[1] != "Section 2" || secs[2] != "Section 3" {
		t.Fatalf("sections = %v, want [Section 1, Section 2, Section 3]", secs)
	}

	// Schedule is captured as its own node.
	var scheds []string
	collect(roots, "schedule", &scheds)
	if len(scheds) != 1 {
		t.Fatalf("schedules = %v, want one", scheds)
	}

	// Nesting + citation paths.
	if s := findByPath(roots, "part-i/section-1"); s == nil {
		t.Fatal("missing part-i/section-1")
	}
	if s := findByPath(roots, "part-ii/section-3"); s == nil {
		t.Fatal("section 3 not under Part II (path part-ii/section-3)")
	}
	// Section 1 has two subsections (inline (1) + standalone (2)).
	s1 := findByPath(roots, "part-i/section-1")
	var subs []string
	collect(s1.Children, "subsection", &subs)
	if len(subs) != 2 || subs[0] != "(1)" || subs[1] != "(2)" {
		t.Fatalf("section 1 subsections = %v, want [(1) (2)]", subs)
	}
	// Section 2 → subsection (1) → paragraphs (a),(b).
	para := findByPath(roots, "part-i/section-2/subsection-1/paragraph-a")
	if para == nil || para.Kind != "paragraph" {
		t.Fatal("missing paragraph at part-i/section-2/subsection-1/paragraph-a")
	}
}
