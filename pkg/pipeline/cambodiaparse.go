package pipeline

import (
	"regexp"
	"strconv"
	"strings"
)

// ParseCambodianAct parses Cambodian English-language legal text into a
// []Section tree. The provision hierarchy mirrors the SG/MY pattern:
//
//	Chapter > Article > Sub-article > Item   (+ Annex/Schedule)
//
// Cambodian Acts use "Article N" as the primary provision unit (equivalent to
// Section in MY/SG). NBC Prakas sometimes use "Clause" instead of "Article";
// the parser treats both as the section-level chunk unit (kind "section").
//
// It is a deterministic line-by-line state machine — no AI — and reuses the
// shared myBuild tree, uniqueSeg, and helpers from the MY parser.
func ParseCambodianAct(text string) []Section {
	lines := khBodyLines(text)
	p := &khParser{root: &myBuild{level: -1}}
	p.stack = []*myBuild{p.root}
	for _, line := range lines {
		p.consume(line)
	}
	return p.root.toSections()
}

// ---- patterns (anchored at line start) --------------------------------------

var (
	// Chapter headers: "CHAPTER I", "CHAPTER 1", "Chapter I — Title",
	// "Chapter 1. Title". Captures: [1]=number (Roman or Arabic),
	// [2]=optional title after separator.
	khChapterRe = regexp.MustCompile(`(?i)^CHAPTER\s+([IVXLC\d]+)\s*(?:[.—–\-:]\s*(.*))?$`)

	// Article markers: "Article 1", "Article 1.", "Article 1:", "Article 1.—",
	// with optional title on the same line.
	// Also matches "Clause" for NBC Prakas.
	// Captures: [1]=number, [2]=rest of line after separator.
	khArticleRe = regexp.MustCompile(`(?i)^(?:Article|Clause)\s+(\d+[A-Z]*)\s*[.:]?\s*(?:[—–\-]\s*)?(.*)$`)

	// Sub-article: "(1) text", "(2) text" — same as MY/SG subsection.
	khSubArticleRe = regexp.MustCompile(`^\((\d{1,3}[A-Z]?)\)\s+(.*)$`)

	// Lettered item: "(a) text", "(b) text".
	khItemRe = regexp.MustCompile(`^\(([a-z]{1,3})\)\s+(.*)$`)

	// Annex / Schedule markers (stop section parsing).
	khAnnexRe = regexp.MustCompile(`(?i)^(?:ANNEX|SCHEDULE|APPENDIX)\b`)

	// Page noise: bare page numbers, repeated act titles in ALL-CAPS.
	khPageNumRe = regexp.MustCompile(`^\d+$`)

	// Enacting clause / long title — used to find body start.
	khEnactingRe = regexp.MustCompile(`(?i)^(?:be it enacted|enacted by|promulgated by|adopted by the)`)
)

// khBodyLines strips noise and locates the body start. Cambodian PDFs
// typically have a preamble before the first Chapter or Article.
func khBodyLines(text string) []string {
	text = strings.ReplaceAll(text, " ", " ") // NBSP
	text = strings.ReplaceAll(text, " ", " ") // en-space
	raw := strings.Split(text, "\n")

	// Find body start: after enacting clause, or start.
	start := 0
	for i, ln := range raw {
		if khEnactingRe.MatchString(strings.TrimSpace(ln)) {
			start = i + 1
			break
		}
	}

	var out []string
	for _, ln := range raw[start:] {
		t := strings.TrimSpace(ln)
		if t == "" {
			continue
		}
		if khPageNumRe.MatchString(t) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// ---- state machine ----------------------------------------------------------

type khParser struct {
	root     *myBuild
	stack    []*myBuild
	lastArt  int    // highest article number accepted (monotonic filter)
	lastItem string // last alphabetic item label (roman disambiguation)
	inAnnex  bool   // stop article parsing inside Annex/Schedule
}

func (p *khParser) consume(line string) {
	// Annex/Schedule gate.
	if khAnnexRe.MatchString(line) {
		p.inAnnex = true
		p.push("schedule", line, myLevelPart, slug(line))
		return
	}
	if p.inAnnex {
		p.appendContent(line)
		return
	}

	// Chapter header.
	if m := khChapterRe.FindStringSubmatch(line); m != nil {
		label := "Chapter " + strings.ToUpper(m[1])
		p.push("chapter", label, myLevelPart, "chapter-"+strings.ToLower(m[1]))
		if title := strings.TrimSpace(m[2]); title != "" {
			p.setHeading(title)
		}
		return
	}

	// Article / Clause.
	if m := khArticleRe.FindStringSubmatch(line); m != nil && p.acceptArticle(m[1]) {
		p.lastItem = ""
		p.push("section", "Article "+m[1], myLevelSection, "section-"+strings.ToLower(m[1]))
		if rest := strings.TrimSpace(m[2]); rest != "" {
			p.consumeInline(rest)
		}
		return
	}

	// Sub-article: "(1) text".
	if m := khSubArticleRe.FindStringSubmatch(line); m != nil && p.inArticle() {
		p.lastItem = ""
		p.push("subsection", "("+m[1]+")", myLevelSubsection, "subsection-"+strings.ToLower(m[1]))
		p.appendContent(m[2])
		return
	}

	// Lettered item: "(a) text".
	if m := khItemRe.FindStringSubmatch(line); m != nil && p.inArticle() {
		tok := m[1]
		if p.isSubItem(tok) {
			p.push("paragraph", "("+tok+")", myLevelSubparagraph, "subparagraph-"+tok)
		} else {
			p.push("paragraph", "("+tok+")", myLevelParagraph, "paragraph-"+tok)
			p.lastItem = tok
		}
		p.appendContent(m[2])
		return
	}

	p.appendContent(line)
}

func (p *khParser) consumeInline(rest string) {
	if m := khSubArticleRe.FindStringSubmatch(rest); m != nil {
		p.push("subsection", "("+m[1]+")", myLevelSubsection, "subsection-"+strings.ToLower(m[1]))
		p.appendContent(m[2])
		return
	}
	p.appendContent(rest)
}

func (p *khParser) acceptArticle(num string) bool {
	if p.inAnnex {
		return false
	}
	base := leadingInt(num)
	hasSuffix := base > 0 && len(strconv.Itoa(base)) < len(num)
	if base == p.lastArt+1 || (hasSuffix && base == p.lastArt) {
		p.lastArt = base
		return true
	}
	// Accept article 1 when nothing parsed yet.
	if p.lastArt == 0 && base == 1 {
		p.lastArt = 1
		return true
	}
	return false
}

func (p *khParser) inArticle() bool {
	for i := len(p.stack) - 1; i >= 0; i-- {
		if p.stack[i].sec.Kind == "section" {
			return true
		}
	}
	return false
}

func (p *khParser) isSubItem(tok string) bool {
	if !isRomanLower(tok) {
		return false
	}
	if len(tok) > 1 {
		return true
	}
	switch tok {
	case "i":
		return p.lastItem != "h"
	case "v":
		return p.lastItem != "u"
	case "x":
		return p.lastItem != "w"
	}
	return false
}

// push, appendContent, setHeading, helpers — delegate to the shared myBuild
// tree and reuse the MY level constants + uniqueSeg helper.

func (p *khParser) push(kind, label string, level int, seg string) {
	for len(p.stack) > 1 && p.stack[len(p.stack)-1].level >= level {
		p.stack = p.stack[:len(p.stack)-1]
	}
	parent := p.stack[len(p.stack)-1]
	ord := 0
	for _, c := range parent.children {
		if c.sec.Kind == kind {
			ord++
		}
	}
	seg = uniqueSeg(parent, seg)
	path := seg
	if parent.sec.CitationPath != "" {
		path = parent.sec.CitationPath + "/" + seg
	}
	node := &myBuild{level: level, sec: Section{Kind: kind, Ordinal: ord + 1, Label: label, CitationPath: path}}
	parent.children = append(parent.children, node)
	p.stack = append(p.stack, node)
}

func (p *khParser) appendContent(s string) {
	top := p.stack[len(p.stack)-1]
	if top == p.root {
		return
	}
	if top.sec.Content != "" {
		top.sec.Content += "\n"
	}
	top.sec.Content += strings.TrimSpace(s)
}

func (p *khParser) setHeading(h string) {
	top := p.stack[len(p.stack)-1]
	if top != p.root {
		top.sec.Heading = strings.TrimSpace(h)
	}
}
