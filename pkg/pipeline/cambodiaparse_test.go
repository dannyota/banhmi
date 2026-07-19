package pipeline

import (
	"strings"
	"testing"
)

// A compact Cambodian Act that exercises the KH parser: enacting clause
// (skipped), Chapters (Roman + Arabic), Articles with optional period,
// sub-articles (1)/(2), lettered items (a)/(b), roman sub-items (i)/(ii),
// and an Annex whose own numbering must NOT be read as articles.
const khTestAct = `LAW ON BANKING AND FINANCIAL INSTITUTIONS

Adopted by the National Assembly on 18 November 1999.

BE IT ENACTED BY THE KING OF THE KINGDOM OF CAMBODIA

CHAPTER I — GENERAL PROVISIONS

Article 1. This law governs the licensing, organization, and operation of banks and financial institutions in Cambodia.

Article 2. (1) In this law, unless the context otherwise requires—
(a) "bank" means a legal entity licensed to carry on banking business;
(b) "financial institution" means an entity licensed to carry on financial services, including—
(i) deposit taking; and
(ii) lending activities.
(2) The National Bank of Cambodia shall supervise all licensed banks.

CHAPTER II — LICENSING

Article 3. No person shall carry on banking business without a licence granted by the National Bank of Cambodia.

Article 4. The National Bank may revoke a licence if the bank fails to comply with this law.

CHAPTER 3 — CAPITAL REQUIREMENTS

Article 5. (1) Every bank shall maintain minimum capital as prescribed.
(a) the initial capital requirement;
(b) the ongoing capital adequacy ratio.
(2) The National Bank shall set capital adequacy standards.

ANNEX

1. This is an annex item, not an article.
2. Another annex item.`

func TestParseCambodianAct_structure(t *testing.T) {
	roots := ParseCambodianAct(khTestAct)

	// Chapters: three (I, II, 3).
	var chapters []string
	collect(roots, "chapter", &chapters)
	if len(chapters) != 3 {
		t.Fatalf("chapters = %v, want 3", chapters)
	}
	if chapters[0] != "Chapter I" {
		t.Fatalf("chapter[0] = %q, want Chapter I", chapters[0])
	}
	if chapters[2] != "Chapter 3" {
		t.Fatalf("chapter[2] = %q, want Chapter 3", chapters[2])
	}

	// Chapter headings.
	ch1 := findByPath(roots, "chapter-i")
	if ch1 == nil || ch1.Heading != "GENERAL PROVISIONS" {
		var h string
		if ch1 != nil {
			h = ch1.Heading
		}
		t.Fatalf("chapter-i heading = %q, want GENERAL PROVISIONS", h)
	}

	// Articles: monotonic 1..5.
	var arts []string
	collect(roots, "section", &arts)
	if len(arts) != 5 {
		t.Fatalf("articles = %v, want 5", arts)
	}
	for i, want := range []string{"Article 1", "Article 2", "Article 3", "Article 4", "Article 5"} {
		if arts[i] != want {
			t.Fatalf("article[%d] = %q, want %q", i, arts[i], want)
		}
	}

	// Annex captured; its numbering not parsed as articles.
	var annexes []string
	collect(roots, "schedule", &annexes)
	if len(annexes) != 1 {
		t.Fatalf("annexes = %v, want one", annexes)
	}
}

func TestParseCambodianAct_nesting(t *testing.T) {
	roots := ParseCambodianAct(khTestAct)

	// Article 1 under Chapter I.
	if findByPath(roots, "chapter-i/section-1") == nil {
		t.Fatal("missing chapter-i/section-1")
	}

	// Article 2 → sub-article (1) → items (a), (b).
	sub1 := findByPath(roots, "chapter-i/section-2/subsection-1")
	if sub1 == nil {
		t.Fatal("missing article-2 sub-article (1)")
	}
	para := findByPath(roots, "chapter-i/section-2/subsection-1/paragraph-a")
	if para == nil || para.Kind != "paragraph" {
		t.Fatal("missing article-2 paragraph (a)")
	}

	// Sub-article (2) is a sibling of (1).
	sub2 := findByPath(roots, "chapter-i/section-2/subsection-2")
	if sub2 == nil {
		t.Fatal("missing article-2 sub-article (2)")
	}

	// Article 3 under Chapter II.
	if findByPath(roots, "chapter-ii/section-3") == nil {
		t.Fatal("missing chapter-ii/section-3")
	}

	// Article 4 content check.
	a4 := findByPath(roots, "chapter-ii/section-4")
	if a4 == nil {
		t.Fatal("missing article 4")
	}
	if !strings.Contains(a4.Content, "revoke a licence") {
		t.Fatalf("article 4 content = %q, missing expected text", a4.Content)
	}
}

func TestParseCambodianAct_subItems(t *testing.T) {
	roots := ParseCambodianAct(khTestAct)

	// Article 2: (i) and (ii) are sub-items under (b), not siblings.
	if findByPath(roots, "chapter-i/section-2/subsection-1/paragraph-b/subparagraph-i") == nil {
		t.Fatal("roman (i) should nest under paragraph (b)")
	}
	if findByPath(roots, "chapter-i/section-2/subsection-1/paragraph-b/subparagraph-ii") == nil {
		t.Fatal("roman (ii) should nest under paragraph (b)")
	}
}

func TestParseCambodianAct_fullTextFallback(t *testing.T) {
	// Text with no Article markers falls through to the caller's fallback.
	roots := ParseCambodianAct("The National Bank may issue guidelines on prudential standards.")
	if len(roots) != 0 {
		t.Fatalf("expected empty roots for text without Article markers, got %d", len(roots))
	}
}

func TestParseCambodianAct_uniquePaths(t *testing.T) {
	roots := ParseCambodianAct(khTestAct)
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

// Clause-based documents (NBC Prakas).
const khTestPrakas = `PRAKAS ON LICENSING CONDITIONS FOR BANKS

Adopted by the Board of the National Bank of Cambodia.

Clause 1. This Prakas sets out the licensing conditions.

Clause 2. (1) An applicant must submit:
(a) a business plan;
(b) proof of capital.
(2) The NBC shall review the application within 90 days.

Clause 3. The NBC may impose additional conditions.`

func TestParseCambodianAct_clauseFormat(t *testing.T) {
	roots := ParseCambodianAct(khTestPrakas)

	var arts []string
	collect(roots, "section", &arts)
	if len(arts) != 3 {
		t.Fatalf("clauses = %v, want 3", arts)
	}
	// Clause markers produce "Article N" labels.
	if arts[0] != "Article 1" {
		t.Fatalf("clause[0] = %q, want Article 1", arts[0])
	}

	// Nesting: Clause 2 → (1) → (a), (b).
	if findByPath(roots, "section-2/subsection-1/paragraph-a") == nil {
		t.Fatal("missing clause-2 paragraph (a)")
	}
}
