package pipeline

import (
	"regexp"
	"strconv"
	"strings"
)

// ParseMalaysianAct parses the text of a Malaysian Act (as extracted from a
// born-digital AGC "Laws of Malaysia" PDF) into the same []Section tree the VN
// parser produces, but with the Malaysian provision hierarchy:
//
//	Part > Chapter/Division > Section > Subsection > Paragraph   (+ Schedule)
//
// It is a deterministic line-by-line state machine — no AI — and is the MY twin
// of ParseSections. The recipe was proven on FSA 2013 (17/17 Parts, 281/281
// sections, 0 gaps): strip page noise, cut the front "Arrangement of Sections"
// at the enacting clause, accept a Section number only in monotonic sequence
// (so the Schedules' own 1./2./3. renumbering and inline cross-references do not
// masquerade as sections), and stop section parsing at the first Schedule.
//
// Structure, numbering, nesting, and citation paths are reliable. Section
// marginal-note TITLES are not recovered here — pdfminer/MarkItDown text drops
// the margin geometry, so high-fidelity titles need a separate layout-aware
// (pdfplumber x-coordinate) pass; Heading is left empty until then.
func ParseMalaysianAct(text string) []Section {
	p := &myParser{root: &myBuild{level: -1}}
	p.stack = []*myBuild{p.root}
	for _, line := range myBodyLines(text) {
		p.consume(line)
	}
	return p.root.toSections()
}

// ---- node builder (heap-allocated nodes → stable pointers; converted to value
// []Section at the end, avoiding slice-growth pointer invalidation) -----------

type myBuild struct {
	sec      Section
	level    int
	children []*myBuild
}

func (b *myBuild) toSections() []Section {
	if len(b.children) == 0 {
		return nil
	}
	out := make([]Section, len(b.children))
	for i, c := range b.children {
		s := c.sec
		s.Content = strings.TrimSpace(s.Content)
		s.Children = c.toSections()
		out[i] = s
	}
	return out
}

// ---- patterns (anchored at line start) --------------------------------------

var (
	// Patterns are case-insensitive where born-digital AGC PDFs render headings in
	// small caps that pdfminer/MarkItDown flattens to mixed case (e.g. "enActeD by").
	myPageNoiseRe = regexp.MustCompile(`(?i)^(laws of malaysia|act\s+\d+[a-z]?)$`)
	myEnactingRe  = regexp.MustCompile(`(?i)enacted by`)
	myPartRe      = regexp.MustCompile(`(?i)^PART\s+([IVXLC]+)$`)
	myChapterRe   = regexp.MustCompile(`(?i)^(?:Division|Chapter)\s+(\d+)$`)
	mySectionRe   = regexp.MustCompile(`^(\d+[A-Z]*)\.(?:\s+(.*))?$`)
	// Subsection numbers are 1–3 digits (+ optional letter, e.g. 2A); a 4-digit
	// parenthetical is a year cross-reference, not a subsection label.
	mySubsecRe   = regexp.MustCompile(`^\((\d{1,3}[A-Z]?)\)\s+(.*)$`)
	myParaRe     = regexp.MustCompile(`^\(([a-z]{1,3})\)\s+(.*)$`)
	myScheduleRe = regexp.MustCompile(`(?i)^(?:(?:FIRST|SECOND|THIRD|FOURTH|FIFTH|SIXTH|SEVENTH|EIGHTH|NINTH|TENTH|ELEVENTH|TWELFTH)\s+SCHEDULE|SCHEDULE\s+\d+)\b`)
)

const (
	myLevelPart = iota
	myLevelChapter
	myLevelSection
	myLevelSubsection
	myLevelParagraph
	myLevelSubparagraph
)

// myBodyLines strips per-page noise and cuts the front "Arrangement of Sections"
// table of contents at the enacting clause, returning the body's non-empty lines.
func myBodyLines(text string) []string {
	text = strings.ReplaceAll(text, " ", " ")
	text = strings.ReplaceAll(text, " ", " ")
	raw := strings.Split(text, "\n")
	start := 0
	for i, ln := range raw {
		if myEnactingRe.MatchString(ln) {
			start = i + 1
			break
		}
	}
	var out []string
	for _, ln := range raw[start:] {
		t := strings.TrimSpace(ln)
		if t == "" || isMYPageNoise(t) {
			continue
		}
		out = append(out, t)
	}
	return out
}

func isMYPageNoise(t string) bool {
	if myPageNoiseRe.MatchString(t) {
		return true
	}
	if _, err := strconv.Atoi(t); err == nil { // bare page number
		return true
	}
	return false
}

// ---- state machine ----------------------------------------------------------

type myParser struct {
	root     *myBuild
	stack    []*myBuild
	lastSec  int    // highest Section number accepted (sections are a 1..N run)
	lastPara string // last alphabetic paragraph label, to disambiguate roman (i)/(v)/(x)
	inSched  bool   // once a Schedule starts, stop section parsing
}

func (p *myParser) consume(line string) {
	switch {
	case myScheduleRe.MatchString(line):
		p.inSched = true
		p.push("schedule", line, myLevelPart, slug(line))
		return
	case p.inSched:
		p.appendContent(line) // schedules: keep flat content, don't parse their numbering
		return
	}

	if m := myPartRe.FindStringSubmatch(line); m != nil {
		p.push("part", "Part "+m[1], myLevelPart, "part-"+strings.ToLower(m[1]))
		return
	}
	if m := myChapterRe.FindStringSubmatch(line); m != nil {
		p.push("chapter", "Chapter "+m[1], myLevelChapter, "chapter-"+m[1])
		return
	}
	if m := mySectionRe.FindStringSubmatch(line); m != nil && p.acceptSection(m[1]) {
		p.lastPara = ""
		p.push("section", "Section "+m[1], myLevelSection, "section-"+strings.ToLower(m[1]))
		if rest := strings.TrimSpace(m[2]); rest != "" {
			p.consumeInline(rest) // e.g. "7. (1) ..." → subsection (1) ...
		}
		return
	}
	if m := mySubsecRe.FindStringSubmatch(line); m != nil && p.inSection() {
		p.lastPara = ""
		p.push("subsection", "("+m[1]+")", myLevelSubsection, "subsection-"+strings.ToLower(m[1]))
		p.appendContent(m[2])
		return
	}
	if m := myParaRe.FindStringSubmatch(line); m != nil && p.inSection() {
		tok := m[1]
		// A roman (i)/(ii)/… is a subparagraph nested under its alphabetic
		// paragraph, not a sibling paragraph; alpha (a)/(b)/… is a paragraph.
		if p.isSubparagraph(tok) {
			p.push("paragraph", "("+tok+")", myLevelSubparagraph, "subparagraph-"+tok)
		} else {
			p.push("paragraph", "("+tok+")", myLevelParagraph, "paragraph-"+tok)
			p.lastPara = tok
		}
		p.appendContent(m[2])
		return
	}
	p.appendContent(line)
}

// isSubparagraph decides whether a lowercase parenthetical like (i) is a roman
// subparagraph rather than an alphabetic paragraph. Multi-letter romans (ii, iv, …)
// are unambiguous; the single ambiguous letters i/v/x are alphabetic paragraphs
// only when they continue the a,b,c… run (…h→(i), …u→(v), …w→(x)).
func (p *myParser) isSubparagraph(tok string) bool {
	if !isRomanLower(tok) {
		return false
	}
	if len(tok) > 1 {
		return true
	}
	switch tok {
	case "i":
		return p.lastPara != "h"
	case "v":
		return p.lastPara != "u"
	case "x":
		return p.lastPara != "w"
	}
	return false
}

// consumeInline handles the remainder after a section number on the same line.
func (p *myParser) consumeInline(rest string) {
	if m := mySubsecRe.FindStringSubmatch(rest); m != nil {
		p.push("subsection", "("+m[1]+")", myLevelSubsection, "subsection-"+strings.ToLower(m[1]))
		p.appendContent(m[2])
		return
	}
	p.appendContent(rest)
}

func (p *myParser) acceptSection(num string) bool {
	if p.inSched {
		return false
	}
	base := leadingInt(num)
	hasSuffix := base > 0 && len(strconv.Itoa(base)) < len(num)
	if base == p.lastSec+1 || (hasSuffix && base == p.lastSec) {
		p.lastSec = base
		return true
	}
	return false
}

// push pops the stack to the new node's parent (by level), appends the node, and
// makes it the open frame. CitationPath is the parent path plus this node's seg.
func (p *myParser) push(kind, label string, level int, seg string) {
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
	seg = uniqueSeg(parent, seg) // guarantee a unique path even if a label repeats
	path := seg
	if parent.sec.CitationPath != "" {
		path = parent.sec.CitationPath + "/" + seg
	}
	node := &myBuild{level: level, sec: Section{Kind: kind, Ordinal: ord + 1, Label: label, CitationPath: path}}
	parent.children = append(parent.children, node)
	p.stack = append(p.stack, node)
}

func (p *myParser) appendContent(s string) {
	top := p.stack[len(p.stack)-1]
	if top == p.root {
		return // preamble / stray text before the first heading
	}
	if top.sec.Content != "" {
		top.sec.Content += "\n"
	}
	top.sec.Content += strings.TrimSpace(s)
}

func (p *myParser) inSection() bool {
	for i := len(p.stack) - 1; i >= 0; i-- {
		if p.stack[i].sec.Kind == "section" {
			return true
		}
	}
	return false
}

// ---- helpers ----------------------------------------------------------------

func leadingInt(s string) int {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	n, _ := strconv.Atoi(s[:end])
	return n
}

var mySlugStripRe = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	return strings.Trim(mySlugStripRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

// romanLowerRe matches a lowercase roman numeral (i..xxxix), used to tell a roman
// subparagraph (i)/(ii)/… from an alphabetic paragraph (a)/(b)/….
var romanLowerRe = regexp.MustCompile(`^(?:x{0,3})(?:ix|iv|v?i{0,3})$`)

func isRomanLower(s string) bool {
	return s != "" && romanLowerRe.MatchString(s)
}

// uniqueSeg returns seg, or seg-2/seg-3/… when a sibling already uses it, so every
// child of parent has a distinct last path segment (hence a unique CitationPath).
func uniqueSeg(parent *myBuild, seg string) string {
	taken := func(s string) bool {
		for _, c := range parent.children {
			cs := c.sec.CitationPath
			if i := strings.LastIndex(cs, "/"); i >= 0 {
				cs = cs[i+1:]
			}
			if cs == s {
				return true
			}
		}
		return false
	}
	if !taken(seg) {
		return seg
	}
	for n := 2; ; n++ {
		if cand := seg + "-" + strconv.Itoa(n); !taken(cand) {
			return cand
		}
	}
}
