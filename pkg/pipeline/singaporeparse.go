package pipeline

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// ParseSingaporeAct parses Singapore legal text into a []Section tree. It
// auto-detects the document format:
//
//   - SSO Acts use Part > Division > Section > Subsection > Paragraph (+ Schedule)
//     with Arabic-numeral Parts and section-number dot/em-dash separators.
//   - MAS Notices and Guidelines use paragraph numbering: plain integers ("1 …",
//     "2 …"), dot-terminated integers ("1. …"), or dot-notation sub-paragraphs
//     ("1.1 …", "4.2(a) …"), grouped under section headings ("Introduction",
//     "Definitions", etc.). These are cited as "paragraph N" or "paragraph N.M".
//
// The parser reuses the MY parser's myBuild tree, uniqueSeg, and helpers.
// It is a deterministic line-by-line state machine — no AI.
func ParseSingaporeAct(text string) []Section {
	lines := sgBodyLines(text)
	if !hasEmDashSections(lines) && isMASNotice(lines) {
		return parseMASNotice(lines)
	}
	p := &sgParser{root: &myBuild{level: -1}}
	p.stack = []*myBuild{p.root}
	for _, line := range lines {
		p.consume(line)
	}
	return p.root.toSections()
}

// ---- patterns (anchored at line start) --------------------------------------

var (
	// SSO PDFs render page headers as "Banking Act 1970" or "BANKING ACT 1970".
	// We strip bare Act-title lines detected by being ALL-CAPS single-line titles
	// without section numbering (handled by sgPageNoise below).

	// Long-title / enacting clause: "An Act to …" or "BE IT ENACTED …".
	sgEnactingRe  = regexp.MustCompile(`(?i)^(?:be it enacted|enacted by)`)
	sgLongTitleRe = regexp.MustCompile(`(?i)^An Act `)

	// PART 1, Part 2, PART 1A — Arabic numerals (+ optional letter suffix), with
	// optional em-dash/dash and title on the same line.
	sgPartRe = regexp.MustCompile(`(?i)^PART\s+(\d+[A-Z]?)\s*(?:[—–\-]\s*(.*))?$`)

	// Division 1, DIVISION 2 — same format as Part but for mid-Part groupings.
	sgDivisionRe = regexp.MustCompile(`(?i)^DIVISION\s+(\d+[A-Z]?)\s*(?:[—–\-]\s*(.*))?$`)

	// Section number: SSO PDFs typically render "2.—(1) In this Act" (no "Section"
	// prefix) or sometimes "Section 2. — Interpretation". The regex strips an
	// optional "Section " prefix, captures the number, and handles the em-dash.
	// Format: [Section ]N[A].[ —]rest
	sgSectionRe = regexp.MustCompile(`^(?:Section\s+)?(\d+[A-Z]*)\.\s*(?:[—–\-]\s*)?(.*)$`)

	// Subsection: same as MY — "(1) text", 1–3 digits + optional letter.
	sgSubsecRe = regexp.MustCompile(`^\((\d{1,3}[A-Z]?)\)\s+(.*)$`)

	// Paragraph: "(a) text" — lowercase alpha.
	sgParaRe = regexp.MustCompile(`^\(([a-z]{1,3})\)\s+(.*)$`)

	// Schedule markers: "FIRST SCHEDULE", "SECOND SCHEDULE", "THE SCHEDULE", etc.
	// Case-sensitive: SSO PDFs render real Schedule headings in ALL-CAPS.
	// Inline references like "the First Schedule;" or "Fifth Schedule." are
	// mixed-case and must NOT trigger the schedule gate (which stops section parsing).
	sgScheduleRe = regexp.MustCompile(`^(?:THE\s+)?(?:(?:FIRST|SECOND|THIRD|FOURTH|FIFTH|SIXTH|SEVENTH|EIGHTH|NINTH|TENTH|ELEVENTH|TWELFTH)\s+)?SCHEDULE\b`)
)

// sgBodyLines strips per-page noise and locates the body start. SSO PDFs
// typically have "An Act to…" as a long title followed by the enacting
// clause. We cut everything before the enacting clause (or long title if no
// enacting clause found).
func sgBodyLines(text string) []string {
	text = strings.ReplaceAll(text, " ", " ") // NBSP
	text = strings.ReplaceAll(text, " ", " ") // en-space
	raw := strings.Split(text, "\n")

	// Find the body start: after enacting clause, or after long title, or start.
	start := 0
	for i, ln := range raw {
		if sgEnactingRe.MatchString(strings.TrimSpace(ln)) {
			start = i + 1
			break
		}
	}
	// If no enacting clause found, try long title.
	if start == 0 {
		for i, ln := range raw {
			if sgLongTitleRe.MatchString(strings.TrimSpace(ln)) {
				start = i + 1
				break
			}
		}
	}

	// Skip the "ARRANGEMENT OF SECTIONS" table-of-contents block. SSO PDFs
	// include a ToC that lists section numbers ("1. Short title", "2. Interpretation")
	// before the actual body. The parser's monotonic filter consumes these as real
	// sections and then rejects the body sections as duplicates.
	inTOC := false
	var out []string
	for _, ln := range raw[start:] {
		t := strings.TrimSpace(ln)
		if t == "" || sgIsPageNoise(t) {
			continue
		}
		up := strings.ToUpper(t)
		if strings.Contains(up, "ARRANGEMENT OF SECTIONS") || strings.Contains(up, "ARRANGEMENT OF PROVISIONS") {
			inTOC = true
			continue
		}
		if inTOC {
			if sgPartRe.MatchString(t) {
				inTOC = false
			} else {
				continue
			}
		}
		out = append(out, t)
	}
	return out
}

// sgIsPageNoise detects per-page header/footer lines to discard.
func sgIsPageNoise(t string) bool {
	// Bare page number.
	if _, err := strconv.Atoi(t); err == nil {
		return true
	}
	// Informal-Title-Of-Act header: single short line in title case/CAPS with
	// no section numbering. This is too aggressive to apply generically, so we
	// skip it for now — page noise will be absorbed as content, which is
	// harmless for search.
	return false
}

// ---- state machine ----------------------------------------------------------

type sgParser struct {
	root     *myBuild
	stack    []*myBuild
	lastSec  int    // highest section number accepted (monotonic filter)
	lastPara string // last alphabetic paragraph label (roman disambiguation)
	inSched  bool   // stop section parsing inside Schedules
}

func (p *sgParser) consume(line string) {
	switch {
	case sgScheduleRe.MatchString(line):
		p.inSched = true
		p.push("schedule", line, myLevelPart, slug(line))
		return
	case p.inSched:
		p.appendContent(line)
		return
	}

	if m := sgPartRe.FindStringSubmatch(line); m != nil {
		p.push("part", "Part "+m[1], myLevelPart, "part-"+strings.ToLower(m[1]))
		if title := strings.TrimSpace(m[2]); title != "" {
			p.setHeading(title)
		}
		return
	}
	if m := sgDivisionRe.FindStringSubmatch(line); m != nil {
		p.push("division", "Division "+m[1], myLevelChapter, "division-"+strings.ToLower(m[1]))
		if title := strings.TrimSpace(m[2]); title != "" {
			p.setHeading(title)
		}
		return
	}
	if m := sgSectionRe.FindStringSubmatch(line); m != nil && p.acceptSection(m[1]) {
		p.lastPara = ""
		p.push("section", "Section "+m[1], myLevelSection, "section-"+strings.ToLower(m[1]))
		if rest := strings.TrimSpace(m[2]); rest != "" {
			p.consumeInline(rest)
		}
		return
	}
	if m := sgSubsecRe.FindStringSubmatch(line); m != nil && p.inSection() {
		p.lastPara = ""
		p.push("subsection", "("+m[1]+")", myLevelSubsection, "subsection-"+strings.ToLower(m[1]))
		p.appendContent(m[2])
		return
	}
	if m := sgParaRe.FindStringSubmatch(line); m != nil && p.inSection() {
		tok := m[1]
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

func (p *sgParser) consumeInline(rest string) {
	if m := sgSubsecRe.FindStringSubmatch(rest); m != nil {
		p.push("subsection", "("+m[1]+")", myLevelSubsection, "subsection-"+strings.ToLower(m[1]))
		p.appendContent(m[2])
		return
	}
	p.appendContent(rest)
}

func (p *sgParser) acceptSection(num string) bool {
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

func (p *sgParser) isSubparagraph(tok string) bool {
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

// push, appendContent, setHeading, inSection delegate to the shared myBuild
// tree and reuse the MY level constants + uniqueSeg helper.

func (p *sgParser) push(kind, label string, level int, seg string) {
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

func (p *sgParser) appendContent(s string) {
	top := p.stack[len(p.stack)-1]
	if top == p.root {
		return
	}
	if top.sec.Content != "" {
		top.sec.Content += "\n"
	}
	top.sec.Content += strings.TrimSpace(s)
}

func (p *sgParser) setHeading(h string) {
	top := p.stack[len(p.stack)-1]
	if top != p.root {
		top.sec.Heading = strings.TrimSpace(h)
	}
}

func (p *sgParser) inSection() bool {
	for i := len(p.stack) - 1; i >= 0; i-- {
		if p.stack[i].sec.Kind == "section" {
			return true
		}
	}
	return false
}

// ---- MAS Notice / Guideline parser ------------------------------------------
//
// MAS Notices and Guidelines cite by paragraph number, not by Section in the
// Act sense. Three numbering formats appear in practice (all in the real SG
// corpus):
//
//   - Plain integer: "1 This Notice …" / "2 For the purpose …"
//   - Dot-terminated integer: "1. This Notice …" / "2. For the purpose …"
//   - Dot-notation sub-paragraph: "1.1 This Notice …" / "4.2 A Bank must …"
//
// Section headings ("Introduction", "Definitions", "Technology Risk Management",
// "Effective Date") appear as bare title-case or ALL-CAPS lines between numbered
// paragraphs. Roman-numeral section headers ("I. Introduction", "II. Definitions")
// also appear in some Notices.
//
// The parser emits section nodes for each heading (with the heading's text),
// and paragraph-level children for each numbered paragraph. (a)/(b) alphabetic
// items and (i)/(ii) roman sub-items nest inside the current paragraph.

var (
	// MAS Notice page noise: "MAS Notice No.: FSM-N05", "Notice to banks in
	// Singapore", title-of-notice headers repeated per page, bare page numbers.
	// Also matches "Monetary Authority of Singapore" letterhead and Act title
	// references at the top.
	sgMASNoiseRe = regexp.MustCompile(`(?i)^(?:` +
		`MAS Notice No\.|` +
		`Guideline No[. ]|` +
		`Notice No\s*:|` +
		`Notice to |` +
		`Monetary Authority of Singapore|` +
		`(?:Financial|Banking|Insurance|Securities|Payment)\s+(?:Services\s+and\s+Markets\s+)?Act|` +
		`\((?:Cap\.?|Act)\s+\d|` +
		`Issue Date\s*:|` +
		`Effective Date\s*:|` +
		`Last revised on|` +
		`\[(?:Amended|Deleted)\s)`)

	// Section heading: a short bare text line that is NOT a numbered paragraph.
	// Examples: "Introduction", "Definitions", "Technology Risk Management",
	// "Effective Date", "NOTICE ON TECHNOLOGY RISK MANAGEMENT".
	// Roman-numeral prefixed: "I. Introduction", "II. Definitions".
	sgMASRomanHeadingRe = regexp.MustCompile(`^([IVXLC]+)\.\s+(.+)$`)

	// Dot-notation paragraph: "1.1 text", "4.2 text", "14A.3 text".
	// Captures: [1]=major, [2]=minor, [3]=rest of line.
	sgMASDotParaRe = regexp.MustCompile(`^(\d{1,3}[A-Z]?)\.(\d{1,3})\s+(.*)$`)

	// Plain or dot-terminated integer paragraph: "1 text" or "1. text".
	// Must be followed by at least one space and non-digit text to avoid matching
	// bare numbers or dates. Captures: [1]=number, [2]=optional letter suffix,
	// [3]=rest of line.
	sgMASIntParaRe = regexp.MustCompile(`^(\d{1,3})([A-Z]?)\.?\s+(\S.*)$`)

	// Page-header noise for MAS documents: the title repeated at page boundaries,
	// often with a page number. "NOTICE ON CYBER HYGIENE" / "NOTICE ON TECHNOLOGY
	// RISK MANAGEMENT" at line start.
	sgMASNoticeTitleRe = regexp.MustCompile(`(?i)^(?:NOTICE ON |GUIDELINES? (?:ON |TO ))`)

	// Underline-style separator: "___…"
	sgMASUnderlineRe = regexp.MustCompile(`^_{3,}$`)
)

// isMASNotice reports whether lines look like a MAS Notice/Guideline rather
// than an SSO Act. The distinguishing signals: MAS documents lack an enacting
// hasEmDashSections returns true if the text has SSO Act-style section markers
// with em-dash separators ("2.—(1) In this Act..."). MAS Notices never have
// this pattern, so it disambiguates Acts misidentified by isMASNotice.
var emDashSectionRe = regexp.MustCompile(`(?m)^\d+[A-Z]*\.—`)

func hasEmDashSections(lines []string) bool {
	n := 0
	for _, ln := range lines {
		if emDashSectionRe.MatchString(ln) {
			n++
			if n >= 2 {
				return true
			}
		}
	}
	return false
}

// clause (which sgBodyLines already cuts) and have plain-integer or
// dot-notation paragraphs WITHOUT the Act parser's "N.—" section separators.
// We detect by counting lines that match MAS paragraph patterns (plain integer
// or dot-notation) in monotonic sequence — 3+ matches is a confident signal.
func isMASNotice(lines []string) bool {
	n := 0
	last := 0
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if m := sgMASDotParaRe.FindStringSubmatch(t); m != nil {
			major := leadingInt(m[1])
			if major >= last {
				last = major
				if n++; n >= 3 {
					return true
				}
			}
			continue
		}
		if m := sgMASIntParaRe.FindStringSubmatch(t); m != nil {
			num, _ := strconv.Atoi(m[1])
			if num > 0 && num >= last {
				last = num
				if n++; n >= 3 {
					return true
				}
			}
		}
	}
	return false
}

// sgMASBodyLines strips MAS-specific page noise from the raw body lines.
func sgMASBodyLines(lines []string) []string {
	var out []string
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if t == "" {
			continue
		}
		if sgMASNoiseRe.MatchString(t) {
			continue
		}
		if sgMASUnderlineRe.MatchString(t) {
			continue
		}
		// Bare page numbers.
		if _, err := strconv.Atoi(t); err == nil {
			continue
		}
		out = append(out, t)
	}
	return out
}

// sgMASIsHeading reports whether a line looks like a section heading: a short
// line that is NOT a numbered paragraph, NOT a definition term, and looks like
// a title (title-case or ALL-CAPS).
func sgMASIsHeading(line string) bool {
	// Too long for a heading.
	if len(line) > 120 {
		return false
	}
	// Ends with period — likely a sentence, not a heading.
	if strings.HasSuffix(line, ".") || strings.HasSuffix(line, ";") || strings.HasSuffix(line, ",") {
		return false
	}
	// Starts with a parenthetical — "(a)", "(1)" — not a heading.
	if strings.HasPrefix(line, "(") {
		return false
	}
	// Starts with a lowercase letter — not a heading.
	runes := []rune(line)
	if len(runes) > 0 && unicode.IsLower(runes[0]) {
		return false
	}
	// Starts with a digit — not a heading (it's a paragraph number).
	if len(runes) > 0 && unicode.IsDigit(runes[0]) {
		return false
	}
	// Starts with a quote — a definition term, not a heading.
	if strings.HasPrefix(line, `"`) || strings.HasPrefix(line, "“") {
		return false
	}
	return true
}

// parseMASNotice parses a MAS Notice or Guideline into:
//
//	section (per heading) > section (per numbered paragraph, cited as "Paragraph N") >
//	  paragraph (a)/(b) > subparagraph (i)/(ii)
//
// Numbered paragraphs are accepted monotonically. Dot-notation sub-paragraphs
// (e.g. "4.2 text") are absorbed as content of their major paragraph, with a
// content prefix for citation. The three numbering variants (plain "N text",
// dot-terminated "N. text", dot-notation "N.M text") all produce the same
// section tree shape so downstream chunking and citation are uniform.
func parseMASNotice(rawLines []string) []Section {
	lines := sgMASBodyLines(rawLines)
	p := &masParser{
		root: &myBuild{level: -1},
	}
	p.stack = []*myBuild{p.root}

	// Skip the notice title line(s) at the top (e.g. "NOTICE ON TECHNOLOGY RISK
	// MANAGEMENT"). These are the document title, not a section heading.
	start := 0
	for i, ln := range lines {
		if sgMASNoticeTitleRe.MatchString(ln) {
			start = i + 1
			continue
		}
		// Stop skipping once we hit the first non-title line.
		if start > 0 {
			break
		}
		// If the first line is already content, don't skip it.
		break
	}

	for _, line := range lines[start:] {
		// Filter out repeated notice title lines at page boundaries.
		if sgMASNoticeTitleRe.MatchString(line) {
			continue
		}
		p.consume(line)
	}
	return p.root.toSections()
}

type masParser struct {
	root     *myBuild
	stack    []*myBuild
	lastPara int    // highest major paragraph number accepted (monotonic)
	lastAlph string // last alphabetic paragraph label (roman disambiguation)
}

func (p *masParser) consume(line string) {
	// Roman-numeral section heading: "I. Introduction", "II. Definitions".
	if m := sgMASRomanHeadingRe.FindStringSubmatch(line); m != nil {
		title := strings.TrimSpace(m[2])
		num := strings.ToLower(m[1])
		p.pushSection("heading", title, "heading-"+num)
		p.setHeading(title)
		return
	}

	// Dot-notation sub-paragraph: "1.1 text", "4.2 text".
	if m := sgMASDotParaRe.FindStringSubmatch(line); m != nil {
		major := leadingInt(m[1])
		minor := m[2]
		rest := strings.TrimSpace(m[3])
		label := m[1] + "." + minor
		// Open the major paragraph if not already open.
		p.openMajorParagraph(m[1], major)
		// Add sub-paragraph content with a label prefix for citation.
		p.appendContent(label + " " + rest)
		return
	}

	// Plain or dot-terminated integer paragraph: "N text" or "N. text".
	if m := sgMASIntParaRe.FindStringSubmatch(line); m != nil {
		num, _ := strconv.Atoi(m[1])
		suffix := m[2] // e.g. "A" in "2A"
		rest := strings.TrimSpace(m[3])
		label := m[1] + suffix
		if num > 0 && p.acceptParagraph(num, suffix) {
			p.lastAlph = ""
			seg := "section-" + strings.ToLower(label)
			p.pushParagraph("section", "Paragraph "+label, seg)
			if rest != "" {
				p.consumeInline(rest)
			}
			return
		}
	}

	// Alphabetic paragraph: "(a) text".
	if m := sgParaRe.FindStringSubmatch(line); m != nil && p.inParagraph() {
		tok := m[1]
		if p.isSubparagraph(tok) {
			p.push("paragraph", "("+tok+")", myLevelSubparagraph, "subparagraph-"+tok)
		} else {
			p.push("paragraph", "("+tok+")", myLevelParagraph, "paragraph-"+tok)
			p.lastAlph = tok
		}
		p.appendContent(m[2])
		return
	}

	// Subsection: "(1) text" — rare in MAS Notices but some use it.
	if m := sgSubsecRe.FindStringSubmatch(line); m != nil && p.inParagraph() {
		p.lastAlph = ""
		p.push("subsection", "("+m[1]+")", myLevelSubsection, "subsection-"+strings.ToLower(m[1]))
		p.appendContent(m[2])
		return
	}

	// Section heading: bare title-case or ALL-CAPS line.
	if sgMASIsHeading(line) {
		p.pushSection("heading", line, slug(line))
		p.setHeading(line)
		return
	}

	p.appendContent(line)
}

func (p *masParser) consumeInline(rest string) {
	// Check if the rest of the line starts with a subsection marker.
	if m := sgSubsecRe.FindStringSubmatch(rest); m != nil {
		p.push("subsection", "("+m[1]+")", myLevelSubsection, "subsection-"+strings.ToLower(m[1]))
		p.appendContent(m[2])
		return
	}
	if m := sgParaRe.FindStringSubmatch(rest); m != nil {
		tok := m[1]
		p.push("paragraph", "("+tok+")", myLevelParagraph, "paragraph-"+tok)
		p.lastAlph = tok
		p.appendContent(m[2])
		return
	}
	p.appendContent(rest)
}

func (p *masParser) acceptParagraph(num int, suffix string) bool {
	hasSuffix := suffix != ""
	if num == p.lastPara+1 || (hasSuffix && num == p.lastPara) {
		p.lastPara = num
		return true
	}
	// Allow the first paragraph (num==1) when nothing has been accepted yet.
	if p.lastPara == 0 && num == 1 {
		p.lastPara = 1
		return true
	}
	return false
}

// openMajorParagraph opens the section for major paragraph num when it is not
// already open and passes the monotonic filter, so dot-notation sub-paragraphs
// (e.g. "4.2 text") can implicitly open their major paragraph.
func (p *masParser) openMajorParagraph(label string, num int) {
	suffix := ""
	if len(label) > len(strconv.Itoa(num)) {
		suffix = label[len(strconv.Itoa(num)):]
	}
	if p.acceptParagraph(num, suffix) {
		p.lastAlph = ""
		seg := "section-" + strings.ToLower(label)
		p.pushParagraph("section", "Paragraph "+label, seg)
	}
}

func (p *masParser) pushSection(kind, label, seg string) {
	// Pop to root for a new heading.
	p.stack = []*myBuild{p.root}
	parent := p.root
	ord := 0
	for _, c := range parent.children {
		if c.sec.Kind == kind {
			ord++
		}
	}
	seg = uniqueSeg(parent, seg)
	path := seg
	node := &myBuild{level: myLevelPart, sec: Section{Kind: kind, Ordinal: ord + 1, Label: label, CitationPath: path}}
	parent.children = append(parent.children, node)
	p.stack = append(p.stack, node)
}

func (p *masParser) pushParagraph(kind, label, seg string) {
	// Pop to heading level (keep heading on stack, push paragraph under it).
	for len(p.stack) > 1 && p.stack[len(p.stack)-1].level >= myLevelSection {
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
	node := &myBuild{level: myLevelSection, sec: Section{Kind: kind, Ordinal: ord + 1, Label: label, CitationPath: path}}
	parent.children = append(parent.children, node)
	p.stack = append(p.stack, node)
}

func (p *masParser) push(kind, label string, level int, seg string) {
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

func (p *masParser) appendContent(s string) {
	top := p.stack[len(p.stack)-1]
	if top == p.root {
		return
	}
	if top.sec.Content != "" {
		top.sec.Content += "\n"
	}
	top.sec.Content += strings.TrimSpace(s)
}

func (p *masParser) setHeading(h string) {
	top := p.stack[len(p.stack)-1]
	if top != p.root {
		top.sec.Heading = strings.TrimSpace(h)
	}
}

func (p *masParser) inParagraph() bool {
	for i := len(p.stack) - 1; i >= 0; i-- {
		if p.stack[i].sec.Kind == "section" {
			return true
		}
	}
	return false
}

func (p *masParser) isSubparagraph(tok string) bool {
	if !isRomanLower(tok) {
		return false
	}
	if len(tok) > 1 {
		return true
	}
	switch tok {
	case "i":
		return p.lastAlph != "h"
	case "v":
		return p.lastAlph != "u"
	case "x":
		return p.lastAlph != "w"
	}
	return false
}
