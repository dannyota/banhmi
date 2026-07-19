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
//
// BNM Policy Documents are detected by their S/G standard/guidance paragraph
// markers (defined in every PD's Interpretation chapter) and parsed by the PD
// state machine instead: lettered Parts (PART A/B/…) > numbered chapters as
// sections ("8 Governance …" cited by BNM as "paragraph 8") with the S/G
// paragraphs as their content, plus Appendix nodes (the PD twin of Schedules).
func ParseMalaysianAct(text string) []Section {
	lines := myBodyLines(text)
	if isBNMPolicyDoc(lines) {
		return parseBNMPolicyDoc(lines)
	}
	p := &myParser{root: &myBuild{level: -1}}
	p.stack = []*myBuild{p.root}
	for _, line := range lines {
		p.consume(line)
	}
	if roots := p.root.toSections(); len(roots) > 0 {
		return roots
	}
	// Neither PD nor Act structure — try BNM letter/circular format
	// (bare numbered paragraphs without S/G markers or ENACTED clause).
	return parseBNMLetterDoc(lines)
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

// myMarginalSplitRe finds a section-number token ("13. " / "2A. ") preceded by
// 2+ whitespace. Older AGC reprint PDFs (e.g. Acts 758/759/701/710) come out of
// go-fitz with the side-column marginal note merged onto the section-number line
// — "Short title and commencement  1. (1) This Act may be cited…" — which the
// line-anchored mySectionRe cannot see; the monotonic acceptSection filter then
// rejects every later section. Splitting the line at these boundaries restores
// one-heading-per-line without touching Acts whose lines are already clean.
var myMarginalSplitRe = regexp.MustCompile(`\s{2,}(\d+[A-Z]*\.\s)`)

// myBodyLines strips per-page noise and cuts the front "Arrangement of Sections"
// table of contents at the enacting clause, returning the body's non-empty lines.
// Lines carrying a merged marginal note + section number are split first.
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
		for _, part := range splitAtRe(ln, myMarginalSplitRe) {
			t := strings.TrimSpace(part)
			if t == "" || isMYPageNoise(t) {
				continue
			}
			out = append(out, t)
		}
	}
	return out
}

// splitAtRe splits line before each match of re's first capture group, so every
// captured token starts its own piece. Lines without a match pass through whole.
func splitAtRe(line string, re *regexp.Regexp) []string {
	locs := re.FindAllStringSubmatchIndex(line, -1)
	if len(locs) == 0 {
		return []string{line}
	}
	var parts []string
	prev := 0
	for _, loc := range locs {
		if at := loc[2]; at > prev { // loc[2] = start of capture group 1
			parts = append(parts, line[prev:at])
			prev = at
		}
	}
	parts = append(parts, line[prev:])
	return parts
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
	curSec   string // full label of the open section (PD chapters, e.g. "14A")
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

// acceptSection admits a section/chapter number only in monotonic sequence:
// the next integer (lastSec+1) or a letter-suffixed insert at the same base
// (…14 → 14A → 14B → 15). This keeps Schedule renumbering and inline
// cross-references from masquerading as sections.
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

// setHeading records the heading text of the just-pushed node.
func (p *myParser) setHeading(h string) {
	top := p.stack[len(p.stack)-1]
	if top != p.root {
		top.sec.Heading = strings.TrimSpace(h)
	}
}

func (p *myParser) inSection() bool {
	for i := len(p.stack) - 1; i >= 0; i-- {
		if p.stack[i].sec.Kind == "section" {
			return true
		}
	}
	return false
}

// ---- BNM policy-document state machine ---------------------------------------

// BNM PD shapes, verified on real corpus markdown (pd-rmit-nov25, the AML/CFT
// PD, the e-money PD): lettered Parts ("PART A OVERVIEW"), numbered chapter
// headings without a dot ("8 Governance arrangements", "14A CDD: Banking …"),
// S/G standard/guidance paragraphs ("S 8.1 …", "G 9.22 …", "S 14A.12 …"), and
// trailing appendices ("Appendix 1 …" / "APPENDIX 8a …"). go-fitz merges many
// headings mid-line after 2+ spaces, so PD lines are re-split before parsing.
var (
	// Case-sensitive: body Parts are "PART A …"; appendix-internal sub-headings
	// ("Part A: Network Security") are mixed-case and must stay content.
	myPDPartRe    = regexp.MustCompile(`^PART\s+([A-Z])\b`)
	myPDSGRe      = regexp.MustCompile(`^[SG]\s+(\d{1,2}[A-Z]?)\.\d`)
	myPDChapterRe = regexp.MustCompile(`^(\d{1,2}[A-Z]?)\s+([A-Z(].*)$`)
	myPDSubParaRe = regexp.MustCompile(`^(\d{1,2}[A-Z]?)\.\d+\b`)
	// The first appendix can carry the section banner on the same line:
	// "APPENDICES Appendix 1 Storage and Transportation …" (pd-rmit-nov25).
	myPDAppendixRe = regexp.MustCompile(`(?i)^(?:APPENDICES\s+)?APPENDIX\s+(\d{1,2}[a-z]?)\b`)
	// Page furniture: "Issued on: …" footers and bare "N of M" page counters.
	myPDNoiseRe = regexp.MustCompile(`(?i)^(issued on:|\d+\s+of\s+\d+$)`)
	// Re-split merged PD lines before an S/G marker ("S 8.1 ") or a numbered
	// token ("8 Governance", "2.1 Subject to …") that follows 2+ spaces.
	myPDSplitRe = regexp.MustCompile(`\s{2,}([SG]\s+\d{1,2}[A-Z]?\.\d|\d{1,2}[A-Z]?(?:\.\d+)*\s+\S)`)
)

// isBNMPolicyDoc reports whether the lines are a BNM Policy Document.
//
// Primary signal: 3+ inline S/G standard/guidance paragraph markers
// ("S 7.1 …", "G 9.22 …") — every PD defines them; Acts have none.
//
// Secondary signal (stranded markers): go-fitz sometimes extracts the
// margin-column "S"/"G" tag onto its own line, separated from the paragraph
// number. A standalone "S" or "G" line is counted as an S/G marker because
// Acts never contain bare single-letter "S"/"G" body lines.
func isBNMPolicyDoc(lines []string) bool {
	n := 0
	for _, ln := range lines {
		for _, piece := range splitAtRe(ln, myPDSplitRe) {
			t := strings.TrimSpace(piece)
			if myPDSGRe.MatchString(t) || t == "S" || t == "G" {
				if n++; n >= 3 {
					return true
				}
			}
		}
	}
	return false
}

// isPDTOCLine reports dot-leader table-of-contents lines ("Introduction ...... 3").
func isPDTOCLine(line string) bool {
	return strings.Contains(line, "....")
}

// isPDHeadingShaped tells a chapter heading ("29 Cybersecurity management")
// from a page-bottom footnote that shares the numbered shape and, being
// numbered sequentially through the document, can pass the monotonic filter
// ("29 Diversity in technology may include the use of …."). Headings are short
// title lines; footnotes are full sentences — long and/or period-terminated.
func isPDHeadingShaped(line string) bool {
	return len(line) <= 120 && !strings.HasSuffix(line, ".")
}

// parseBNMPolicyDoc parses a BNM Policy Document into:
//
//	Part (A/B/…) > section per numbered chapter (BNM cites them as "paragraph N")
//	with the chapter's S/G paragraphs and headings as flat content,
//	plus one schedule node per Appendix (flat content, like Act Schedules).
//
// Chapters are accepted monotonically (1, 2, … 14, 14A, … 15) — the same guard
// the Act parser uses — so numbered lists and cross-references stay content.
// A chapter whose heading line was merged beyond recovery is still opened by
// its first S/G paragraph ("S 15.1 …" opens chapter 15), heading left empty.
func parseBNMPolicyDoc(lines []string) []Section {
	p := &myParser{root: &myBuild{level: -1}}
	p.stack = []*myBuild{p.root}

	// Cut the front matter (title block, applicability list, TOC): the body
	// starts at the first PART heading that is not a dot-leader TOC entry.
	start := 0
	for i, ln := range lines {
		if myPDPartRe.MatchString(ln) && !isPDTOCLine(ln) {
			start = i
			break
		}
	}

	inAppendix := false
	lastApp := 0
	for _, raw := range lines[start:] {
		if myPDNoiseRe.MatchString(raw) {
			continue // drop footer lines before splitting (their dates look like headings)
		}
		for _, piece := range splitAtRe(raw, myPDSplitRe) {
			line := strings.TrimSpace(piece)
			if line == "" || isPDTOCLine(line) || myPDNoiseRe.MatchString(line) {
				continue
			}

			if m := myPDAppendixRe.FindStringSubmatch(line); m != nil {
				// The transition INTO the appendices (lastApp == 0) must look
				// like a heading — a mid-body "Appendix 1 …" cross-reference at
				// a line start would otherwise swallow the rest of the body.
				if base, ok := acceptPDAppendix(m[1], lastApp); ok && (lastApp > 0 || isPDHeadingShaped(line)) {
					lastApp = base
					inAppendix = true
					p.push("schedule", "Appendix "+strings.ToUpper(m[1]), myLevelPart, "appendix-"+strings.ToLower(m[1]))
					p.setHeading(strings.TrimSpace(line[len(m[0]):]))
					continue
				}
			}
			if inAppendix {
				p.appendContent(line) // appendices: flat content, don't parse their numbering
				continue
			}

			if m := myPDPartRe.FindStringSubmatch(line); m != nil {
				p.push("part", "Part "+m[1], myLevelPart, "part-"+strings.ToLower(m[1]))
				p.setHeading(strings.TrimSpace(line[len(m[0]):]))
				continue
			}
			if m := myPDChapterRe.FindStringSubmatch(line); m != nil && isPDHeadingShaped(line) && p.acceptSection(m[1]) {
				p.curSec = m[1]
				p.push("section", "Paragraph "+m[1], myLevelSection, "section-"+strings.ToLower(m[1]))
				p.setHeading(m[2])
				continue
			}
			// A chapter whose heading line was merged beyond recovery is still
			// opened by its own numbering: an S/G paragraph ("S 15.1 …") or a
			// bare sub-paragraph number ("15.1 …" — go-fitz sometimes strands
			// the S/G marker on the previous line) attests its chapter.
			if m := myPDSGRe.FindStringSubmatch(line); m != nil {
				p.openPDChapter(m[1])
				p.appendContent(line)
				continue
			}
			if m := myPDSubParaRe.FindStringSubmatch(line); m != nil {
				p.openPDChapter(m[1])
				p.appendContent(line)
				continue
			}
			p.appendContent(line)
		}
	}
	return p.root.toSections()
}

// openPDChapter opens the section for chapter num when it is not the one
// already open and passes the monotonic filter; content-attested chapters
// carry no heading (their heading line was merged beyond recovery).
func (p *myParser) openPDChapter(num string) {
	if num != p.curSec && p.acceptSection(num) {
		p.curSec = num
		p.push("section", "Paragraph "+num, myLevelSection, "section-"+strings.ToLower(num))
	}
}

// acceptPDAppendix admits appendix numbers monotonically with letter-suffix
// inserts (1, 2, …, 4, 4a, 4b, 5 …), mirroring acceptSection, so mid-sentence
// "Appendix N" cross-references at a line start cannot open a stray node.
func acceptPDAppendix(num string, last int) (int, bool) {
	base := leadingInt(num)
	hasSuffix := base > 0 && len(strconv.Itoa(base)) < len(num)
	if base == last+1 || (hasSuffix && base == last) {
		return base, true
	}
	return 0, false
}

// ---- BNM letter/circular state machine --------------------------------------

// BNM letters and circulars use bare numbered paragraphs ("2. With immediate
// effect…", "3. Furthermore…") without PART headings, S/G markers, or an
// enacting clause. The Act parser produces nothing (no ENACTED clause → no TOC
// cut, and paragraphs starting at 2 fail the start-at-1 monotonic filter); the
// PD parser is not triggered (no S/G markers). This parser is the last resort
// before fulltext fallback.

var (
	// Matches "2." or "4. The specification…" at line start — a bare numbered
	// paragraph in BNM letter format. The number may be followed by nothing
	// (text on the next line) or by inline text.
	myLetterParaRe = regexp.MustCompile(`^(\d{1,2})\.(?:\s+(.*))?$`)
	// Bare "Appendix" without a number (BNM letters use a single unnumbered appendix).
	myLetterAppendixRe = regexp.MustCompile(`(?i)^APPENDIX\b`)
)

// parseBNMLetterDoc parses a BNM letter/circular into numbered paragraph
// sections plus optional appendices. Returns nil when the text has fewer than
// 3 bare numbered paragraph lines (not enough structure to justify splitting).
func parseBNMLetterDoc(lines []string) []Section {
	// Pre-scan: count bare-numbered paragraphs for the detection threshold.
	count := 0
	for _, ln := range lines {
		if myLetterParaRe.MatchString(ln) {
			count++
		}
	}
	if count < 3 {
		return nil // not enough structure; let fulltext fallback handle it
	}

	p := &myParser{root: &myBuild{level: -1}}
	p.stack = []*myBuild{p.root}

	inAppendix := false
	for _, line := range lines {
		if myPDNoiseRe.MatchString(line) {
			continue
		}
		// Appendix detection: try numbered first (reuses PD regex), then bare.
		if m := myPDAppendixRe.FindStringSubmatch(line); m != nil {
			inAppendix = true
			p.push("schedule", "Appendix "+strings.ToUpper(m[1]), myLevelPart, "appendix-"+strings.ToLower(m[1]))
			p.setHeading(strings.TrimSpace(line[len(m[0]):]))
			continue
		}
		if !inAppendix && myLetterAppendixRe.MatchString(line) {
			inAppendix = true
			p.push("schedule", "Appendix 1", myLevelPart, "appendix-1")
			rest := strings.TrimSpace(myLetterAppendixRe.ReplaceAllString(line, ""))
			if rest != "" {
				p.setHeading(rest)
			}
			continue
		}
		if inAppendix {
			p.appendContent(line)
			continue
		}
		if m := myLetterParaRe.FindStringSubmatch(line); m != nil && p.acceptLetterPara(m[1]) {
			p.push("section", "Paragraph "+m[1], myLevelSection, "section-"+m[1])
			if rest := strings.TrimSpace(m[2]); rest != "" {
				p.appendContent(rest)
			}
			continue
		}
		p.appendContent(line)
	}
	return p.root.toSections()
}

// acceptLetterPara admits a paragraph number in monotonic sequence, allowing
// the run to start at any number (BNM letters often begin with an unnumbered
// preamble, so the first explicit number may be 2 or higher).
func (p *myParser) acceptLetterPara(num string) bool {
	base := leadingInt(num)
	if base == 0 {
		return false
	}
	if p.lastSec == 0 {
		// First numbered paragraph — accept any positive number.
		p.lastSec = base
		return true
	}
	if base == p.lastSec+1 {
		p.lastSec = base
		return true
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
