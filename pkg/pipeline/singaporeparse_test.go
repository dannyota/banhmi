package pipeline

import (
	"strings"
	"testing"

	"danny.vn/banhmi/pkg/base/jurisdiction"
)

// A compact Singapore Act that exercises the SG parser: long title + enacting
// clause (must be skipped), Arabic-numeral Parts, sections with em-dash
// separators, inline + standalone subsections and paragraphs, a Division, and
// a Schedule whose own numbering must NOT be read as sections.
const sgTestAct = `BANKING ACT 1970

(CHAPTER 19)

An Act to license and regulate the business of banking.

BE IT ENACTED BY THE PRESIDENT WITH THE ADVICE AND CONSENT OF THE PARLIAMENT OF SINGAPORE, AS FOLLOWS:

PART 1 — PRELIMINARY

1.—(1) This Act may be cited as the Banking Act 1970.
(2) This Act comes into operation on 1 January 1971.

2.—(1) In this Act, unless the context otherwise requires—
(a) "bank" means any company;
(b) "banking business" means the business of receiving money.

PART 2 — LICENSING OF BANKS

Division 1 — Grant and Revocation of Licences

3.—(1) No person shall carry on banking business unless the person holds a valid licence.
(2) A licence shall be subject to conditions.

4. The Authority may revoke a licence if the bank fails to comply with this Act.

PART 3 — RESERVE FUNDS AND DIVIDENDS

5.—(1) Every bank shall maintain a reserve fund.
(a) the first condition;
(b) the second condition, namely—
(i) the first way; and
(ii) the second way.
(2) The reserve fund requirement.

THE SCHEDULE

1. This is a schedule item, not a section.
2. Another schedule item.`

func TestParseSingaporeAct_structure(t *testing.T) {
	roots := ParseSingaporeAct(sgTestAct)

	// Parts: three Arabic-numeral parts.
	var parts []string
	collect(roots, "part", &parts)
	if len(parts) != 3 || parts[0] != "Part 1" || parts[1] != "Part 2" || parts[2] != "Part 3" {
		t.Fatalf("parts = %v, want [Part 1, Part 2, Part 3]", parts)
	}

	// Part headings from the em-dash title.
	p1 := findByPath(roots, "part-1")
	if p1 == nil || p1.Heading != "PRELIMINARY" {
		t.Fatalf("part-1 heading = %q, want PRELIMINARY", p1.Heading)
	}

	// Sections: monotonic run 1..5.
	var secs []string
	collect(roots, "section", &secs)
	if len(secs) != 5 {
		t.Fatalf("sections = %v, want 5 sections", secs)
	}
	for i, want := range []string{"Section 1", "Section 2", "Section 3", "Section 4", "Section 5"} {
		if secs[i] != want {
			t.Fatalf("section[%d] = %q, want %q", i, secs[i], want)
		}
	}

	// Schedule captured, its own numbering not parsed as sections.
	var scheds []string
	collect(roots, "schedule", &scheds)
	if len(scheds) != 1 {
		t.Fatalf("schedules = %v, want one", scheds)
	}

	// Division node under Part 2.
	var divs []string
	collect(roots, "division", &divs)
	if len(divs) != 1 || divs[0] != "Division 1" {
		t.Fatalf("divisions = %v, want [Division 1]", divs)
	}
	div := findByPath(roots, "part-2/division-1")
	if div == nil {
		t.Fatal("missing part-2/division-1")
	}
	if div.Heading != "Grant and Revocation of Licences" {
		t.Fatalf("division heading = %q, want Grant and Revocation of Licences", div.Heading)
	}
}

func TestParseSingaporeAct_nesting(t *testing.T) {
	roots := ParseSingaporeAct(sgTestAct)

	// Section 1 under Part 1.
	if findByPath(roots, "part-1/section-1") == nil {
		t.Fatal("missing part-1/section-1")
	}

	// Section 1 has two subsections: inline (1) and standalone (2).
	s1 := findByPath(roots, "part-1/section-1")
	var subs []string
	collect(s1.Children, "subsection", &subs)
	if len(subs) != 2 || subs[0] != "(1)" || subs[1] != "(2)" {
		t.Fatalf("section 1 subsections = %v, want [(1) (2)]", subs)
	}

	// Section 2 → subsection (1) → paragraphs (a), (b).
	para := findByPath(roots, "part-1/section-2/subsection-1/paragraph-a")
	if para == nil || para.Kind != "paragraph" {
		t.Fatal("missing section-2 paragraph (a)")
	}

	// Section 3 nested under the Division.
	if findByPath(roots, "part-2/division-1/section-3") == nil {
		t.Fatal("section 3 not under division-1")
	}

	// Section 4 is a simple section (no subsection).
	s4 := findByPath(roots, "part-2/division-1/section-4")
	if s4 == nil {
		t.Fatal("missing section 4")
	}
	if !strings.Contains(s4.Content, "revoke a licence") {
		t.Fatalf("section 4 content = %q, missing expected text", s4.Content)
	}
}

func TestParseSingaporeAct_subparagraphs(t *testing.T) {
	roots := ParseSingaporeAct(sgTestAct)

	// Section 5: (i),(ii) are subparagraphs under (b), not siblings.
	if findByPath(roots, "part-3/section-5/subsection-1/paragraph-b/subparagraph-i") == nil {
		t.Fatal("roman (i) should nest under paragraph (b)")
	}
	if findByPath(roots, "part-3/section-5/subsection-1/paragraph-b/subparagraph-ii") == nil {
		t.Fatal("roman (ii) should nest under paragraph (b)")
	}
}

func TestParseSingaporeAct_fullTextFallback(t *testing.T) {
	roots, stats, _ := parseNormalizeSections(jurisdiction.ParserSGAct, "The Authority may issue guidelines.")
	if len(roots) != 1 || roots[0].Kind != "section" || roots[0].CitationPath != "fulltext" {
		t.Fatalf("fallback roots = %+v, want one full-text section", roots)
	}
	if stats.Total == 0 {
		t.Fatal("fallback produced zero sections")
	}
}

func TestParseSingaporeAct_uniquePaths(t *testing.T) {
	roots := ParseSingaporeAct(sgTestAct)
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

// ---- MAS Notice / Guideline tests -------------------------------------------

// A compact MAS Notice that exercises the MAS paragraph parser: plain integer
// paragraphs, section headings, (a)/(b) alphabetic items, (i)/(ii) roman sub-items.
const masTestNotice = `MAS Notice No.: FSM-N05

Notice to banks in Singapore
Financial Services and Markets Act 2022

Issue Date: 09 May 2024

NOTICE ON TECHNOLOGY RISK MANAGEMENT

Introduction

1 This Notice is issued pursuant to section 29(1) of the Financial Services and Markets Act 2022.

Definitions

2 For the purpose of this Notice—

"critical system" means a system, the failure of which will cause significant disruption.

3 Except where defined in this Notice, the expressions used have the same meanings as in the Act.

Technology Risk Management

4 A Bank must put in place a framework and process to identify critical systems.

5 A Bank must make all reasonable effort to maintain high availability for critical systems.

6 A Bank must establish a recovery time objective of not more than 4 hours.

7 A Bank must notify the Authority as soon as possible, but not later than 1 hour, upon the discovery of a relevant incident.

8 A Bank must submit a root cause and impact analysis report containing—
(a) an executive summary of the relevant incident;
(b) an analysis of the root cause which triggered the relevant incident;
(c) a description of the impact on the Bank's—
(i) compliance with laws and regulations;
(ii) operations; and
(iii) service to its customers; and
(d) a description of the remedial measures taken.

9 A Bank must implement IT controls to protect customer information.

Effective Date

10 This Notice shall take effect on 10 May 2024.`

func TestParseMASNotice_structure(t *testing.T) {
	roots := ParseSingaporeAct(masTestNotice)

	// Should produce heading sections for "Introduction", "Definitions", etc.
	var headings []string
	collect(roots, "heading", &headings)
	if len(headings) < 3 {
		t.Fatalf("headings = %v, want at least 3 (Introduction, Definitions, Technology Risk Management, ...)", headings)
	}

	// Should produce paragraph-level sections.
	var paras []string
	collect(roots, "section", &paras)
	if len(paras) < 8 {
		t.Fatalf("paragraphs = %v, want at least 8", paras)
	}
	if paras[0] != "Paragraph 1" {
		t.Fatalf("first paragraph = %q, want Paragraph 1", paras[0])
	}
}

func TestParseMASNotice_nesting(t *testing.T) {
	roots := ParseSingaporeAct(masTestNotice)

	// Paragraph 8 should have alphabetic paragraphs (a), (b), (c), (d).
	var found *Section
	var walk func([]Section)
	walk = func(secs []Section) {
		for i := range secs {
			if secs[i].Label == "Paragraph 8" {
				found = &secs[i]
				return
			}
			walk(secs[i].Children)
		}
	}
	walk(roots)
	if found == nil {
		t.Fatal("missing Paragraph 8")
	}

	var alphs []string
	collect(found.Children, "paragraph", &alphs)
	if len(alphs) < 4 {
		t.Fatalf("paragraph 8 alphabetic items = %v, want (a)(b)(c)(d)", alphs)
	}

	// (c) should have roman sub-paragraphs (i), (ii), (iii).
	paraC := findByLabel(found.Children, "(c)")
	if paraC == nil {
		t.Fatal("missing paragraph (c) under Paragraph 8")
	}
	var romans []string
	collect(paraC.Children, "paragraph", &romans)
	if len(romans) < 2 {
		t.Fatalf("paragraph (c) roman items = %v, want (i)(ii)(iii)", romans)
	}
}

func TestParseMASNotice_noFulltextFallback(t *testing.T) {
	roots := ParseSingaporeAct(masTestNotice)
	// Should NOT fall through to fulltext.
	if len(roots) == 1 && roots[0].CitationPath == "fulltext" {
		t.Fatal("MAS Notice fell through to fulltext fallback")
	}
}

func TestParseMASNotice_uniquePaths(t *testing.T) {
	roots := ParseSingaporeAct(masTestNotice)
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

// Test dot-notation paragraphs (e.g. "1.1 text", "4.2 text").
const masTestDotNotation = `Guideline No: SFA 04-G10
Issue Date: 03 September 2020

GUIDELINES TO MAS NOTICE SFA 04-N16 ON EXECUTION OF CUSTOMERS' ORDERS

1 Purpose

1.1 These Guidelines are issued by the Monetary Authority of Singapore pursuant to Section 321.

1.2 These Guidelines provide guidance on the requirements relating to Best Execution.

2 Overarching Requirement

2.1 A capital markets intermediary shall establish and implement Best Execution policies.

3 Order Placement and Execution Policy

3.1 A capital markets intermediary should establish Best Execution policies covering all products.

3.2 The Best Execution policies must be approved by the Board of Directors.

3.3 Best Execution should apply when executing customers' orders on an execution venue.`

func TestParseMASNotice_dotNotation(t *testing.T) {
	roots := ParseSingaporeAct(masTestDotNotation)

	// Should not fall through to fulltext.
	if len(roots) == 1 && roots[0].CitationPath == "fulltext" {
		t.Fatal("dot-notation MAS Guideline fell through to fulltext fallback")
	}

	// Should produce paragraph sections for 1, 2, 3.
	var paras []string
	collect(roots, "section", &paras)
	if len(paras) < 3 {
		t.Fatalf("paragraphs = %v, want at least 3", paras)
	}
	if paras[0] != "Paragraph 1" {
		t.Fatalf("first paragraph = %q, want Paragraph 1", paras[0])
	}

	// Paragraph 3 should contain sub-paragraph content (3.1, 3.2, 3.3).
	var p3 *Section
	var walk func([]Section)
	walk = func(secs []Section) {
		for i := range secs {
			if secs[i].Label == "Paragraph 3" {
				p3 = &secs[i]
				return
			}
			walk(secs[i].Children)
		}
	}
	walk(roots)
	if p3 == nil {
		t.Fatal("missing Paragraph 3")
	}
	if !strings.Contains(p3.Content, "3.1") {
		t.Fatalf("Paragraph 3 content missing sub-paragraph 3.1: %q", p3.Content)
	}
}

// Test dot-terminated integer paragraphs: "1. text", "2. text".
const masTestDotTerminated = `MAS Notice No.: PSN08
Notice to licensees
Payment Services Act 2019

NOTICE ON DISCLOSURES AND COMMUNICATIONS

Introduction

1. This Notice is issued pursuant to section 102(1) of the Payment Services Act 2019.

Definitions

2. For the purpose of this Notice—

"delayed service" means a service where the transaction is not completed immediately.

3. The expressions used shall have the same meanings as in the Act.

Disclosures by Standard Payment Institutions

4. A standard payment institution must provide the following statement.

5. Where a major payment institution carries on a business of providing services,
the institution must disclose to customers.

6. Where a major payment institution issues e-money, it must provide a statement.`

func TestParseMASNotice_dotTerminated(t *testing.T) {
	roots := ParseSingaporeAct(masTestDotTerminated)

	if len(roots) == 1 && roots[0].CitationPath == "fulltext" {
		t.Fatal("dot-terminated MAS Notice fell through to fulltext fallback")
	}

	var paras []string
	collect(roots, "section", &paras)
	if len(paras) < 5 {
		t.Fatalf("paragraphs = %v, want at least 5", paras)
	}
	if paras[0] != "Paragraph 1" {
		t.Fatalf("first paragraph = %q, want Paragraph 1", paras[0])
	}
}

// Test Roman-numeral section headings: "I. Introduction", "II. Definitions".
const masTestRomanHeadings = `MAS Notice No.: FSM-N04

NOTICE ON CYBER HYGIENE

I. Introduction

1.1 This Notice is issued pursuant to section 29(1) of the Act.

II. Definitions

2.1 For the purpose of this Notice—

"administrative account" means any user account with full privileges.

III. Administrative Account Security

3.1 A relevant entity must implement multi-factor authentication.

3.2 A relevant entity must ensure that administrative accounts are disabled after a period of inactivity.`

func TestParseMASNotice_romanHeadings(t *testing.T) {
	roots := ParseSingaporeAct(masTestRomanHeadings)

	if len(roots) == 1 && roots[0].CitationPath == "fulltext" {
		t.Fatal("Roman-heading MAS Notice fell through to fulltext fallback")
	}

	// Should produce heading sections with Roman numeral labels.
	var headings []string
	collect(roots, "heading", &headings)
	if len(headings) < 3 {
		t.Fatalf("headings = %v, want at least 3", headings)
	}
}

// Ensure Acts are still correctly detected and parsed (not misidentified as MAS Notices).
func TestParseSingaporeAct_notMisidentifiedAsMAS(t *testing.T) {
	roots := ParseSingaporeAct(sgTestAct)

	// Acts produce Parts and Sections, not headings.
	var parts []string
	collect(roots, "part", &parts)
	if len(parts) == 0 {
		t.Fatal("Act was misidentified as MAS Notice — no Parts found")
	}

	var headings []string
	collect(roots, "heading", &headings)
	if len(headings) > 0 {
		t.Fatal("Act produced heading nodes — likely misidentified as MAS Notice")
	}
}

// findByLabel returns the first section with the given label (depth-1 search).
func findByLabel(secs []Section, label string) *Section {
	for i := range secs {
		if secs[i].Label == label {
			return &secs[i]
		}
	}
	return nil
}

// Inline Schedule references in body text (e.g. "the First Schedule;" in a
// definition) must NOT trigger the schedule gate that stops section parsing.
// Regression: PSA2019/SFA2001/CA2018/BA1970 lost most sections because mixed-
// case references like "the First Schedule;" matched the old case-insensitive
// sgScheduleRe, setting inSched=true permanently.
func TestParseSingaporeAct_inlineScheduleRef(t *testing.T) {
	act := `An Act to regulate payment services.

BE IT ENACTED AS FOLLOWS:

PART 1

1.—(1) This Act may be cited as the Payment Services Act 2019.

2.—(1) In this Act, unless the context otherwise requires—
"account issuance service" has the meaning given by Part 3 of
the First Schedule;
"e-money" has the meaning given by Part 3 of
the First Schedule;

3.—(1) Subject to subsection (2), this Act does not apply to any public authority.

4.—(1) The Authority may appoint any person to exercise any of its powers.

FIRST SCHEDULE

1. This is a schedule item.`

	roots := ParseSingaporeAct(act)
	var secs []string
	collect(roots, "section", &secs)
	if len(secs) != 4 {
		t.Fatalf("sections = %v, want 4 (inline Schedule refs must not stop parsing)", secs)
	}
	// The real FIRST SCHEDULE heading (all caps) should still be captured.
	var scheds []string
	collect(roots, "schedule", &scheds)
	if len(scheds) != 1 {
		t.Fatalf("schedules = %v, want 1", scheds)
	}
}

// SSO may emit "Section 2. — Interpretation" with an explicit prefix.
func TestParseSingaporeAct_sectionPrefixForm(t *testing.T) {
	act := `An Act to regulate.

BE IT ENACTED AS FOLLOWS:

PART 1

Section 1. — Short title
This Act may be cited as the Test Act 2026.

Section 2. — Interpretation
In this Act, "bank" means a licensed entity.`

	roots := ParseSingaporeAct(act)
	var secs []string
	collect(roots, "section", &secs)
	if len(secs) != 2 || secs[0] != "Section 1" || secs[1] != "Section 2" {
		t.Fatalf("sections = %v, want [Section 1, Section 2]", secs)
	}
}
