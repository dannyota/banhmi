package pipeline

import (
	"strings"
	"testing"

	"danny.vn/banhmi/pkg/base/jurisdiction"
)

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

// Older AGC PDFs render the enacting clause in small caps that flatten to mixed
// case ("enActeD by"); the TOC must still be cut so the body sections parse and
// body subsections do not pile onto the last TOC section.
func TestParseMalaysianAct_smallCapsEnactingCutsTOC(t *testing.T) {
	act := `Laws of Malaysia
ARRANGEMENT OF SECTIONS
1. Short title
2. Powers
enActeD by the Parliament of Malaysia as follows:
1. (1) This Act may be cited as the Test Act.
(2) It binds the Government.
2. The powers clause.`
	roots := ParseMalaysianAct(act)

	s1 := findByPath(roots, "section-1")
	if s1 == nil {
		t.Fatal("no section-1 (TOC not cut?)")
	}
	var subs []string
	collect(s1.Children, "subsection", &subs)
	if len(subs) != 2 {
		t.Fatalf("section 1 subsections = %v, want [(1) (2)] — body parsed, TOC cut", subs)
	}
}

// Roman (i)/(ii) are subparagraphs nested under their alphabetic paragraph, not
// sibling paragraphs; a 4-digit year parenthetical is not a subsection.
func TestParseMalaysianAct_subparagraphsAndYearGuard(t *testing.T) {
	act := `enActeD by the Parliament as follows:
1. (1) A person may—
(a) do the first thing;
(b) do the second thing, namely—
(i) the first way; and
(ii) the second way.
(2) The second subsection.
(1965) A stray year reference, not a subsection.`
	roots := ParseMalaysianAct(act)

	// (i),(ii) nest under paragraph (b).
	if findByPath(roots, "section-1/subsection-1/paragraph-b/subparagraph-i") == nil {
		t.Fatal("roman (i) should nest under paragraph (b)")
	}
	if findByPath(roots, "section-1/subsection-1/paragraph-b/subparagraph-ii") == nil {
		t.Fatal("roman (ii) should nest under paragraph (b)")
	}
	// subsection (1) has exactly two direct paragraphs (a),(b) — romans are nested.
	sub := findByPath(roots, "section-1/subsection-1")
	direct := 0
	for _, c := range sub.Children {
		if c.Kind == "paragraph" {
			direct++
		}
	}
	if direct != 2 {
		t.Fatalf("subsection-1 direct paragraphs = %d, want 2 (romans nested under (b))", direct)
	}
	// Year guard: no subsection labelled (1965).
	var subs []string
	collect(roots, "subsection", &subs)
	for _, s := range subs {
		if s == "(1965)" {
			t.Fatal("(1965) was misparsed as a subsection")
		}
	}
}

// Binding MY text with no Part/Section structure still yields one chunkable
// section via the fallback (so it is not silently dropped from the index).
func TestParseNormalizeSections_myFullTextFallback(t *testing.T) {
	roots, stats, _ := parseNormalizeSections(jurisdiction.ParserMYAct, "Organisation Structure\n\nThe Commission consists of the following divisions and units.")
	if len(roots) != 1 || roots[0].Kind != "section" || roots[0].CitationPath != "fulltext" {
		t.Fatalf("fallback roots = %+v, want one full-text section", roots)
	}
	if stats.Total == 0 {
		t.Fatal("fallback produced zero sections")
	}
	// Empty text yields nothing (no junk section).
	if r := fullTextFallback("   ", "section"); r != nil {
		t.Fatalf("empty fallback = %+v, want nil", r)
	}
}

// uniqueSeg guarantees distinct citation paths even if a source repeats a label.
func TestParseMalaysianAct_uniquePaths(t *testing.T) {
	roots := ParseMalaysianAct(myTestAct)
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

// Older AGC reprint PDFs (Acts 758/759/701/710) merge the side-column marginal
// note onto the section-number line: "Short title and commencement  1. (1) This
// Act may be cited…". The pre-split in myBodyLines must separate the note from
// the section number so the line-anchored section regex and the monotonic
// filter see every section. Two verified shapes: (a) marginal note + section
// number at line start, (b) trailing body text + marginal note + NEXT section
// number all on one line.
func TestParseMalaysianAct_marginalNoteMergedSections(t *testing.T) {
	act := `ENACTED by the Parliament of Malaysia as follows:
PART I
PRELIMINARY
Short title and commencement  1. (1) This Act may be cited as the Test Act 2013.
(2) This Act comes into operation on a date to be appointed by the Minister.
Interpretation  2. (1) In this Act, unless the context otherwise requires—
(a) the first defined term;
shall, on conviction, be liable to a fine.   Effect of revocation  3. (1) Where—
(a) a licence is revoked.`
	roots := ParseMalaysianAct(act)

	var secs []string
	collect(roots, "section", &secs)
	if len(secs) != 3 || secs[0] != "Section 1" || secs[1] != "Section 2" || secs[2] != "Section 3" {
		t.Fatalf("sections = %v, want [Section 1, Section 2, Section 3]", secs)
	}
	// Section 1: inline subsection (1) + standalone (2).
	s1 := findByPath(roots, "part-i/section-1")
	if s1 == nil {
		t.Fatal("missing part-i/section-1")
	}
	var subs []string
	collect(s1.Children, "subsection", &subs)
	if len(subs) != 2 || subs[0] != "(1)" || subs[1] != "(2)" {
		t.Fatalf("section 1 subsections = %v, want [(1) (2)]", subs)
	}
	// Shape (b): the trailing body text before the marginal note stays with the
	// previous provision, and section 3 opens cleanly with subsection (1).
	if findByPath(roots, "part-i/section-3/subsection-1") == nil {
		t.Fatal("missing part-i/section-3/subsection-1 (merged trailing-text line not split)")
	}
	p2a := findByPath(roots, "part-i/section-2/subsection-1/paragraph-a")
	if p2a == nil {
		t.Fatal("missing section-2 paragraph (a)")
	}
	if !strings.Contains(p2a.Content, "liable to a fine") {
		t.Fatalf("trailing body text lost: paragraph (a) content = %q", p2a.Content)
	}
}

// A clean Act line (single spaces only) must pass through the marginal-note
// pre-split untouched — previously-working Acts stay byte-identical.
func TestMYBodyLines_noSplitOnCleanLines(t *testing.T) {
	got := myBodyLines("ENACTED by Parliament:\n1. Short title. This Act contains ref to section 12. It works.\n")
	if len(got) != 1 || got[0] != "1. Short title. This Act contains ref to section 12. It works." {
		t.Fatalf("clean line was altered: %q", got)
	}
}

// A compact BNM Policy Document in the shape verified on real corpus markdown
// (pd-rmit-nov25 / the e-money PD / the AML/CFT PD): title block + dot-leader
// TOC (cut), lettered Parts, numbered chapter headings (some merged mid-line
// after 2+ spaces), S/G paragraphs (S 9.1 opens its chapter when the heading
// was lost), a monotonic footnote that must NOT steal a chapter number, and an
// appendix ("APPENDICES Appendix 1 …" banner form) whose numbered list must
// not become sections.
const myTestPD = `Risk Management in Test (RMiT)
Issued on: 28 November 2025                                    BNM/RH/PD 028-98
TABLE OF CONTENTS
PART A OVERVIEW ................................................ 2
1          Introduction ........................................ 3
2          Applicability ....................................... 3
PART B POLICY REQUIREMENTS ..................................... 8
8         Governance .......................................... 8
APPENDICES ..................................................... 41
Appendix 1      Storage of Sensitive Data ..................... 41
PART A OVERVIEW
1          Introduction  1.1 With the prevalent use of technology, institutions must invest in controls.
1.2 This policy document sets out requirements.  2          Applicability  2.1  Subject to paragraph 2.2, this policy document is applicable to all institutions.
3 For ease of reference, an institution is defined as a person licensed under the Act with substantial market presence in Malaysia.
Test Institution                       4 of 80
PART B POLICY REQUIREMENTS
3          Governance  Responsibilities of the Board
S 3.1 The board must establish and approve the technology risk appetite.  S 3.2 In discharging its oversight, the board must obtain updates.
G 3.3 The board may participate in awareness programmes.
S 4.1 A financial institution must ensure the TRMF is an integral part of its ERM.
APPENDICES Appendix 1 Storage of Sensitive Data
1. deploying the latest industry-tested encryption;
2. implementing authorised access control.`

func TestParseMalaysianAct_bnmPolicyDocument(t *testing.T) {
	roots := ParseMalaysianAct(myTestPD)

	var parts, secs, scheds []string
	collect(roots, "part", &parts)
	collect(roots, "section", &secs)
	collect(roots, "schedule", &scheds)

	if len(parts) != 2 || parts[0] != "Part A" || parts[1] != "Part B" {
		t.Fatalf("parts = %v, want [Part A, Part B]", parts)
	}
	// Chapters 1, 2 (heading merged mid-line), 3, and 4 (rescued by S 4.1).
	// The footnote "3 For ease of reference…." must not steal chapter 3.
	want := []string{"Paragraph 1", "Paragraph 2", "Paragraph 3", "Paragraph 4"}
	if len(secs) != len(want) {
		t.Fatalf("sections = %v, want %v", secs, want)
	}
	for i := range want {
		if secs[i] != want[i] {
			t.Fatalf("sections = %v, want %v", secs, want)
		}
	}
	if len(scheds) != 1 || scheds[0] != "Appendix 1" {
		t.Fatalf("schedules = %v, want [Appendix 1]", scheds)
	}

	// Chapter 1 heading recovered; its sub-paragraphs stay flat content.
	s1 := findByPath(roots, "part-a/section-1")
	if s1 == nil || s1.Heading != "Introduction" {
		t.Fatalf("part-a/section-1 = %+v, want heading Introduction", s1)
	}
	if !strings.Contains(s1.Content, "1.1 With the prevalent use") || !strings.Contains(s1.Content, "1.2 This policy document") {
		t.Fatalf("chapter 1 content lost sub-paragraphs: %q", s1.Content)
	}
	// Chapter 2's heading was merged mid-line after 2+ spaces and must be split out.
	s2 := findByPath(roots, "part-a/section-2")
	if s2 == nil || !strings.Contains(s2.Content, "2.1  Subject to paragraph 2.2") {
		t.Fatalf("merged chapter 2 not recovered: %+v", s2)
	}
	// The footnote did not become a section: chapter 3 is the real Governance
	// chapter under Part B, with its S/G paragraphs as content.
	s3 := findByPath(roots, "part-b/section-3")
	if s3 == nil {
		t.Fatal("missing part-b/section-3")
	}
	if !strings.Contains(s3.Content, "S 3.1 The board must establish") || !strings.Contains(s3.Content, "G 3.3 The board may participate") {
		t.Fatalf("chapter 3 S/G content lost: %q", s3.Content)
	}
	// Chapter 4 has no heading line at all — rescued by its first S paragraph.
	s4 := findByPath(roots, "part-b/section-4")
	if s4 == nil || !strings.Contains(s4.Content, "S 4.1 A financial institution") {
		t.Fatalf("S/G-rescued chapter 4 missing: %+v", s4)
	}
	// The appendix keeps its numbered list as flat content, not sections.
	app := findByPath(roots, "appendix-1")
	if app == nil || len(app.Children) != 0 {
		t.Fatalf("appendix-1 = %+v, want flat schedule node", app)
	}
	if !strings.Contains(app.Content, "1. deploying the latest") {
		t.Fatalf("appendix content lost: %q", app.Content)
	}
	// Page furniture is dropped.
	if strings.Contains(s2.Content, "4 of 80") {
		t.Fatal("page counter leaked into content")
	}
}

// An Act must never be routed to the PD parser: the S/G detector requires S/G
// paragraph markers, which Acts do not contain.
func TestParseMalaysianAct_actNotDetectedAsPD(t *testing.T) {
	roots := ParseMalaysianAct(myTestAct)
	var secs []string
	collect(roots, "section", &secs)
	if len(secs) != 3 || secs[0] != "Section 1" {
		t.Fatalf("Act misrouted: sections = %v", secs)
	}
}

// Compact BNM PD with stranded S/G markers (go-fitz extracts the margin "S"/"G"
// tag onto its own line, separated from the paragraph number). Derived from the
// real PD+Statistical+Reporting_v2 document. The stranded markers must trigger
// PD detection — Acts never have bare single-letter "S"/"G" body lines.
const myTestPDStrandedSG = `Issued on: 5 Feb 2018 BNM/RH/PD 031-12
Guidelines on Statistical Reporting for Money Services Business
TABLE OF CONTENTS
PART A OVERVIEW ............. 1
1 Introduction .............. 1
PART B REQUIREMENTS ......... 3
7 Types of reports .......... 3
PART A OVERVIEW
1.1 This policy document outlines the requirements for statistical reporting.
2.1 This policy document is applicable to all licensees.
3.1 This policy document is issued pursuant to section 34 of the Act.
4.1 This policy document comes into effect on 5 February 2018.
5.1 The terms and expressions used in this policy document shall have the same meanings.
5.2 For the purpose of this policy document:
6.1 This policy document supersedes the previous version.
7 Types of statistical reports and submission requirements
PART B REQUIREMENTS FOR STATISTICAL REPORTING AND SUBMISSION
S 7.1 A licensee shall submit relevant statistical reports.
S 7.2 Types of Statistical Reports and Submission Timelines.

S

7.3 All statistical reports shall be submitted to the Bank.

S

7.4 A licensee shall also submit a hardcopy.

G

7.6 The maintenance of records may be in hardcopies or electronic forms.

S
S
8.1 Submission after the specified deadlines shall be considered late.
8.2 Failure in submitting is deemed to be non-submission.
9.1 A licensee shall ensure that all data is true, correct and complete.`

func TestParseMalaysianAct_bnmPDStrandedSGMarkers(t *testing.T) {
	roots := ParseMalaysianAct(myTestPDStrandedSG)

	var parts, secs []string
	collect(roots, "part", &parts)
	collect(roots, "section", &secs)

	if len(parts) != 2 || parts[0] != "Part A" || parts[1] != "Part B" {
		t.Fatalf("parts = %v, want [Part A, Part B]", parts)
	}
	// Chapters opened by their N.N sub-paragraphs: 1 (from 1.1), 2 (from 2.1),
	// …, 7 (from chapter heading or S 7.1), 8 (from 8.1), 9 (from 9.1).
	if len(secs) < 7 {
		t.Fatalf("sections = %v (%d), want >=7 (chapters detected)", secs, len(secs))
	}
	if secs[0] != "Paragraph 1" {
		t.Fatalf("first section = %q, want Paragraph 1", secs[0])
	}

	// The stranded S/G markers must not appear as section content corruption.
	s1 := findByPath(roots, "part-a/section-1")
	if s1 == nil {
		t.Fatal("missing part-a/section-1")
	}
	if strings.Contains(s1.Content, "\nS\n") {
		t.Fatal("stranded S marker leaked into section content as raw line")
	}
}

// Verify that the existing RMiT-style PD (inline S/G markers) still parses
// identically after the stranded-SG detection extension.
func TestParseMalaysianAct_bnmPDInlineSGUnchanged(t *testing.T) {
	roots := ParseMalaysianAct(myTestPD)

	var parts, secs, scheds []string
	collect(roots, "part", &parts)
	collect(roots, "section", &secs)
	collect(roots, "schedule", &scheds)

	// Must be byte-identical to TestParseMalaysianAct_bnmPolicyDocument.
	wantParts := []string{"Part A", "Part B"}
	wantSecs := []string{"Paragraph 1", "Paragraph 2", "Paragraph 3", "Paragraph 4"}
	wantScheds := []string{"Appendix 1"}

	if len(parts) != len(wantParts) {
		t.Fatalf("parts = %v, want %v", parts, wantParts)
	}
	for i := range wantParts {
		if parts[i] != wantParts[i] {
			t.Fatalf("parts = %v, want %v", parts, wantParts)
		}
	}
	if len(secs) != len(wantSecs) {
		t.Fatalf("sections = %v, want %v", secs, wantSecs)
	}
	for i := range wantSecs {
		if secs[i] != wantSecs[i] {
			t.Fatalf("sections = %v, want %v", secs, wantSecs)
		}
	}
	if len(scheds) != len(wantScheds) {
		t.Fatalf("schedules = %v, want %v", scheds, wantScheds)
	}

	// Content spot-check: chapter 3 S/G content preserved.
	s3 := findByPath(roots, "part-b/section-3")
	if s3 == nil || !strings.Contains(s3.Content, "S 3.1 The board must establish") {
		t.Fatalf("RMiT PD chapter 3 content changed: %+v", s3)
	}
}

// BNM letter/circular with bare numbered paragraphs (derived from the real
// MSSB Online doc): bare N. paragraphs starting from 2 (1 is the opening
// preamble), plus an Appendix. The Act parser rejects these because the
// monotonic filter requires start-at-1, and no ENACTED clause is present.
const myTestBNMLetter = `BANK NEGARA MALAYSIA
CENTRAL BANK OF MALAYSIA
5 Julai 2018
Tuan/Puan,
Money Services Business Online Application and Tracking System (MSBS)
As part of the continuous efforts of Bank Negara Malaysia (the Bank) to promote
operational efficiency, licensees are now able to submit regulatory applications
through the MSBS with effect from 1 August 2018.
2.
Other functions and benefits of the MSBS are as follows:
(a) Maintains up to date information on the profile of licensees.
(b) Real time status tracking of applications by licensees.
3.
With the deployment of the MSBS, licensees are required to submit all regulatory
applications stated in paragraph 1 through the MSBS.
4. Licensees are hereby notified that applications under paragraph 1 will only be
accepted by the Bank through MSBS with effect from 1 August 2018.
5.
For any further queries, please contact the following officers.
Sekian.
Yang benar,
(Jessica Chew Cheng Lian)
Timbalan Gabenor
Appendix
Process Flow for Access to the Money Services Business Online Application and
Tracking System (MSBS) through Fl@KijangNet Regulatory Services
STEP 1
Licensee's Administrators to login to Fl@Kijangnet.`

func TestParseMalaysianAct_bnmLetterDoc(t *testing.T) {
	roots := ParseMalaysianAct(myTestBNMLetter)

	if len(roots) == 1 && roots[0].CitationPath == "fulltext" {
		t.Fatal("BNM letter fell through to fulltext fallback")
	}

	var secs, scheds []string
	collect(roots, "section", &secs)
	collect(roots, "schedule", &scheds)

	// Paragraphs 2–5 (paragraph 1 is the unnumbered preamble before "2.").
	if len(secs) != 4 {
		t.Fatalf("sections = %v, want 4 (Paragraphs 2–5)", secs)
	}
	if secs[0] != "Paragraph 2" || secs[3] != "Paragraph 5" {
		t.Fatalf("sections = %v, want [Paragraph 2, Paragraph 3, Paragraph 4, Paragraph 5]", secs)
	}

	// Appendix captured as a schedule node.
	if len(scheds) != 1 || scheds[0] != "Appendix 1" {
		t.Fatalf("schedules = %v, want [Appendix 1]", scheds)
	}

	// Preamble text before paragraph 2 is orphaned (dropped by appendContent
	// to the root, which is discarded). The letter body starts at paragraph 2.
	s2 := findByPath(roots, "section-2")
	if s2 == nil {
		t.Fatal("missing section-2")
	}
	if !strings.Contains(s2.Content, "Other functions") {
		t.Fatalf("paragraph 2 content = %q, want to contain 'Other functions'", s2.Content)
	}
}

// A short BNM letter with exactly 3 bare numbered paragraphs (derived from the
// real BMC for DFI doc). The threshold of 3 is met, so it should parse.
const myTestBNMShortLetter = `BANK NEGARA MALAYSIA
Tuan,
Regulatory Treatment of BNM Mudarabah Certificate (BMC) for DFI
The BMC is an Islamic monetary instrument issued by Bank Negara Malaysia.
2. With immediate effect, the Bank hereby specifies the following regulatory treatment
for capital adequacy requirements.
3. Furthermore, the BMC is recognized as marketable securities issued by the Bank.
4.
The specification in this letter shall form an integral part of the Capital Framework
until further notice.
Sekian.`

func TestParseMalaysianAct_bnmShortLetter(t *testing.T) {
	roots := ParseMalaysianAct(myTestBNMShortLetter)

	if len(roots) == 1 && roots[0].CitationPath == "fulltext" {
		t.Fatal("short BNM letter fell through to fulltext fallback")
	}

	var secs []string
	collect(roots, "section", &secs)
	if len(secs) != 3 {
		t.Fatalf("sections = %v, want 3 (Paragraphs 2–4)", secs)
	}
	if secs[0] != "Paragraph 2" || secs[2] != "Paragraph 4" {
		t.Fatalf("sections = %v, want [Paragraph 2, Paragraph 3, Paragraph 4]", secs)
	}

	s4 := findByPath(roots, "section-4")
	if s4 == nil || !strings.Contains(s4.Content, "The specification in this letter") {
		t.Fatalf("paragraph 4 content wrong: %+v", s4)
	}
}

// Very short BNM doc with no numbered structure (derived from the real CCBM
// doc) should return nil so the caller falls back to fulltext. CCBM is a
// 1,010-char announcement with no Parts, no S/G, no numbered paragraphs.
const myTestBNMNoStructure = `Title
Commencement of Commercial Banking Business of China Construction Bank (Malaysia)
Berhad
Issuance Date
27 January 2017
Summary
We wish to inform that China Construction Bank (Malaysia) Berhad has been granted
a commercial banking licence by the Finance Minister of Malaysia.
Name and Address of Main Office
: China Construction Bank (Malaysia) Berhad
Wisma Selangor Dredging Berhad 142A, Jalan Ampang 50450 Kuala Lumpur`

func TestParseMalaysianAct_noStructureReturnsNil(t *testing.T) {
	roots := ParseMalaysianAct(myTestBNMNoStructure)
	if len(roots) != 0 {
		t.Fatalf("expected nil/empty for structureless doc, got %d sections", len(roots))
	}
}

// Table-driven: compact shapes exercising all detection paths.
func TestParseMalaysianAct_detectionPaths(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		wantKind string // "pd", "act", "letter", "none"
		minSecs  int
	}{
		{
			name:     "inline S/G → PD",
			text:     myTestPD,
			wantKind: "pd",
			minSecs:  4,
		},
		{
			name:     "stranded S/G → PD",
			text:     myTestPDStrandedSG,
			wantKind: "pd",
			minSecs:  7,
		},
		{
			name:     "Act with ENACTED + sections",
			text:     myTestAct,
			wantKind: "act",
			minSecs:  3,
		},
		{
			name:     "BNM letter with bare N.",
			text:     myTestBNMLetter,
			wantKind: "letter",
			minSecs:  4,
		},
		{
			name:     "short BNM letter (3 paras)",
			text:     myTestBNMShortLetter,
			wantKind: "letter",
			minSecs:  3,
		},
		{
			name:     "no structure → nil",
			text:     myTestBNMNoStructure,
			wantKind: "none",
			minSecs:  0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			roots := ParseMalaysianAct(tc.text)
			var secs []string
			collect(roots, "section", &secs)

			switch tc.wantKind {
			case "pd":
				// PD sections are labelled "Paragraph N".
				if len(secs) < tc.minSecs {
					t.Fatalf("sections = %d, want >= %d", len(secs), tc.minSecs)
				}
				if !strings.HasPrefix(secs[0], "Paragraph ") {
					t.Fatalf("first section = %q, want Paragraph prefix (PD path)", secs[0])
				}
			case "act":
				if len(secs) < tc.minSecs {
					t.Fatalf("sections = %d, want >= %d", len(secs), tc.minSecs)
				}
				if !strings.HasPrefix(secs[0], "Section ") {
					t.Fatalf("first section = %q, want Section prefix (Act path)", secs[0])
				}
			case "letter":
				if len(secs) < tc.minSecs {
					t.Fatalf("sections = %d, want >= %d", len(secs), tc.minSecs)
				}
				if !strings.HasPrefix(secs[0], "Paragraph ") {
					t.Fatalf("first section = %q, want Paragraph prefix (letter path)", secs[0])
				}
			case "none":
				if len(roots) != 0 {
					t.Fatalf("expected nil/empty, got %d roots", len(roots))
				}
			}
		})
	}
}
