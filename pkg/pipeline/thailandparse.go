package pipeline

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ParseThaiAct parses Thai legal text into a []Section tree. It auto-detects
// the document format:
//
//   - OCS Acts use หมวด (Chapter) > ส่วนที่ (Part/Division) > มาตรา (Section)
//     with Thai or Arabic numerals. Amendment suffixes ทวิ/ตรี/จัตวา are
//     preserved in labels.
//   - BOT notifications use ข้อ (Clause) as the primary provision unit, with
//     plain numbered sub-items.
//
// The parser reuses the MY parser's myBuild tree, uniqueSeg, and helpers.
// It is a deterministic line-by-line state machine — no AI.
func ParseThaiAct(text string) []Section {
	lines := thBodyLines(text)
	if isBOTNotification(lines) {
		return parseBOTNotification(lines)
	}
	p := &thParser{root: &myBuild{level: -1}}
	p.stack = []*myBuild{p.root}
	for _, line := range lines {
		p.consume(line)
	}
	return p.root.toSections()
}

// ---- Thai numeral conversion -----------------------------------------------

// thaiDigits maps Thai digit characters (U+0E50–U+0E59) to ASCII digits.
var thaiDigits = map[rune]rune{
	'๐': '0', '๑': '1', '๒': '2', '๓': '3', '๔': '4',
	'๕': '5', '๖': '6', '๗': '7', '๘': '8', '๙': '9',
}

// thaiToArabic replaces Thai numeral characters with ASCII digits.
func thaiToArabic(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if d, ok := thaiDigits[r]; ok {
			b.WriteRune(d)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// thaiParseInt parses a string that may contain Thai or Arabic digits as an
// integer. Returns -1 on failure.
func thaiParseInt(s string) int {
	s = thaiToArabic(strings.TrimSpace(s))
	n, err := strconv.Atoi(s)
	if err != nil {
		return -1
	}
	return n
}

// ---- Thai amendment suffixes -----------------------------------------------

// thAmendSuffixes maps Thai amendment ordinal suffixes to their Latin equivalents.
var thAmendSuffixes = map[string]string{
	"ทวิ":   "/1",
	"ตรี":   "/2",
	"จัตวา": "/3",
}

// ---- patterns (anchored at line start) ------------------------------------

var (
	// thMatraRe matches มาตรา N (with Thai or Arabic numerals) optionally
	// followed by an amendment suffix (ทวิ/ตรี/จัตวา).
	// Group 1: number (Thai or Arabic digits)
	// Group 2: optional amendment suffix
	thMatraRe = regexp.MustCompile(`^มาตรา\s+([๐-๙0-9]+)\s*(ทวิ|ตรี|จัตวา)?\s*$`)

	// thMatraInlineRe matches มาตรา N with trailing content on the same line.
	// Group 1: number, Group 2: optional amendment suffix, Group 3: trailing text.
	thMatraInlineRe = regexp.MustCompile(`^มาตรา\s+([๐-๙0-9]+)\s*(ทวิ|ตรี|จัตวา)?\s+(.+)$`)

	// thMuatRe matches หมวด N (Chapter) — Thai or Arabic numerals.
	// Group 1: number (may be Thai digits).
	thMuatRe = regexp.MustCompile(`^หมวด\s+([๐-๙0-9]+)\s*$`)

	// thMuatInlineRe matches หมวด N with a title on the same line.
	// Group 1: number, Group 2: heading text.
	thMuatInlineRe = regexp.MustCompile(`^หมวด\s+([๐-๙0-9]+)\s+(.+)$`)

	// thSuanthiRe matches ส่วนที่ N (Part/Division).
	// Group 1: number.
	thSuanthiRe = regexp.MustCompile(`^ส่วนที่\s+([๐-๙0-9]+)\s*$`)

	// thSuanthiInlineRe matches ส่วนที่ N with a title.
	// Group 1: number, Group 2: heading text.
	thSuanthiInlineRe = regexp.MustCompile(`^ส่วนที่\s+([๐-๙0-9]+)\s+(.+)$`)

	// thKhorRe matches ข้อ N (BOT clause) — standalone heading.
	// Group 1: number (Thai or Arabic).
	thKhorRe = regexp.MustCompile(`^ข้อ\s+([๐-๙0-9]+)\s*$`)

	// thKhorInlineRe matches ข้อ N with trailing content.
	// Group 1: number, Group 2: content.
	thKhorInlineRe = regexp.MustCompile(`^ข้อ\s+([๐-๙0-9]+)\s+(.+)$`)

	// thThaiItemRe matches numbered items in Thai numerals: (๑), (๒), etc.
	// Group 1: the Thai/Arabic digit(s) inside parens.
	thThaiItemRe = regexp.MustCompile(`^\(([๐-๙0-9]+)\)\s+(.*)$`)

	// thPageNoRe matches bare page numbers (Thai or Arabic).
	thPageNoRe = regexp.MustCompile(`^[๐-๙0-9]+$`)

	// thTransitionalRe matches บทเฉพาะกาล (transitional provisions).
	thTransitionalRe = regexp.MustCompile(`^บทเฉพาะกาล\s*$`)

	// thNotesRe matches หมายเหตุ (notes at the end).
	thNotesRe = regexp.MustCompile(`^หมายเหตุ`)
)

// ---- page noise stripping -------------------------------------------------

// thIsPageNoise returns true for lines that are per-page headers/footers.
func thIsPageNoise(t string) bool {
	// Bare page number (Thai or Arabic).
	if thPageNoRe.MatchString(t) {
		return true
	}
	// Very short lines that are just digits or whitespace.
	if utf8.RuneCountInString(t) <= 3 {
		if _, err := strconv.Atoi(thaiToArabic(t)); err == nil {
			return true
		}
	}
	return false
}

// thBodyLines strips per-page noise and returns non-empty body lines.
func thBodyLines(text string) []string {
	text = strings.ReplaceAll(text, " ", " ") // NBSP
	text = strings.ReplaceAll(text, " ", " ") // en-space
	raw := strings.Split(text, "\n")

	var out []string
	for _, ln := range raw {
		t := strings.TrimSpace(ln)
		if t == "" || thIsPageNoise(t) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// ---- format detection -----------------------------------------------------

// isBOTNotification detects BOT notification format by looking for ข้อ markers
// and the absence of มาตรา markers.
func isBOTNotification(lines []string) bool {
	matraCount := 0
	khorCount := 0
	for _, ln := range lines {
		if thMatraRe.MatchString(ln) || thMatraInlineRe.MatchString(ln) {
			matraCount++
		}
		if thKhorRe.MatchString(ln) || thKhorInlineRe.MatchString(ln) {
			khorCount++
		}
	}
	// BOT if we have ข้อ markers and no มาตรา, or ข้อ significantly outnumber มาตรา.
	return khorCount >= 2 && matraCount == 0
}

// ---- hierarchy levels -----------------------------------------------------

const (
	thLevelChapter = iota // หมวด (outermost structural)
	thLevelPart           // ส่วนที่ (Part/Division, nests under หมวด)
	thLevelSection        // มาตรา or ข้อ
	thLevelItem           // (๑), (๒) numbered items
)

// ---- Act parser -----------------------------------------------------------

type thParser struct {
	root         *myBuild
	stack        []*myBuild
	lastMatra    int  // highest มาตรา number accepted (monotonic)
	pendingTitle bool // structural node awaiting title line
	inNotes      bool // after หมายเหตุ — stop parsing
}

func (p *thParser) consume(line string) {
	if p.inNotes {
		p.appendContent(line)
		return
	}

	// Notes section — stop structural parsing.
	if thNotesRe.MatchString(line) {
		p.inNotes = true
		p.push("heading", line, thLevelPart, slug(line))
		p.setHeading(line)
		return
	}

	// Transitional provisions — treated as a chapter-level heading.
	if thTransitionalRe.MatchString(line) {
		p.push("chapter", "บทเฉพาะกาล", thLevelChapter, "bot-chapakaan")
		p.setHeading("บทเฉพาะกาล")
		return
	}

	// ส่วนที่ N (Part/Division) — outermost structural level.
	if m := thSuanthiInlineRe.FindStringSubmatch(line); m != nil {
		num := thaiParseInt(m[1])
		if num > 0 {
			label := "ส่วนที่ " + thaiToArabic(m[1])
			p.pushWithOrdinal("part", label, thLevelPart, "part-"+strconv.Itoa(num), num)
			p.setHeading(strings.TrimSpace(m[2]))
			return
		}
	}
	if m := thSuanthiRe.FindStringSubmatch(line); m != nil {
		num := thaiParseInt(m[1])
		if num > 0 {
			label := "ส่วนที่ " + thaiToArabic(m[1])
			p.pushWithOrdinal("part", label, thLevelPart, "part-"+strconv.Itoa(num), num)
			p.pendingTitle = true
			return
		}
	}

	// หมวด N (Chapter).
	if m := thMuatInlineRe.FindStringSubmatch(line); m != nil {
		num := thaiParseInt(m[1])
		if num > 0 {
			label := "หมวด " + thaiToArabic(m[1])
			p.pushWithOrdinal("chapter", label, thLevelChapter, "chapter-"+strconv.Itoa(num), num)
			p.setHeading(strings.TrimSpace(m[2]))
			return
		}
	}
	if m := thMuatRe.FindStringSubmatch(line); m != nil {
		num := thaiParseInt(m[1])
		if num > 0 {
			label := "หมวด " + thaiToArabic(m[1])
			p.pushWithOrdinal("chapter", label, thLevelChapter, "chapter-"+strconv.Itoa(num), num)
			p.pendingTitle = true
			return
		}
	}

	// มาตรา N [ทวิ|ตรี|จัตวา] — Section (the chunking unit).
	if m := thMatraInlineRe.FindStringSubmatch(line); m != nil {
		num := thaiParseInt(m[1])
		suffix := m[2]
		if num > 0 && p.acceptMatra(num, suffix) {
			label, seg := thMastraLabelSeg(m[1], suffix)
			p.pushWithOrdinal("section", label, thLevelSection, seg, num)
			if rest := strings.TrimSpace(m[3]); rest != "" {
				// Check if rest starts with a Thai-numeral item.
				if im := thThaiItemRe.FindStringSubmatch(rest); im != nil {
					inum := thaiParseInt(im[1])
					if inum > 0 {
						ilabel := "(" + thaiToArabic(im[1]) + ")"
						p.pushWithOrdinal("paragraph", ilabel, thLevelItem, "paragraph-"+strconv.Itoa(inum), inum)
						p.appendContent(im[2])
						return
					}
				}
				p.appendContent(rest)
			}
			return
		}
	}
	if m := thMatraRe.FindStringSubmatch(line); m != nil {
		num := thaiParseInt(m[1])
		suffix := m[2]
		if num > 0 && p.acceptMatra(num, suffix) {
			label, seg := thMastraLabelSeg(m[1], suffix)
			p.pushWithOrdinal("section", label, thLevelSection, seg, num)
			return
		}
	}

	// Numbered items: (๑), (๒), etc. — only inside a section.
	if p.inSection() {
		if m := thThaiItemRe.FindStringSubmatch(line); m != nil {
			num := thaiParseInt(m[1])
			if num > 0 {
				label := "(" + thaiToArabic(m[1]) + ")"
				p.pushWithOrdinal("paragraph", label, thLevelItem, "paragraph-"+strconv.Itoa(num), num)
				p.appendContent(m[2])
				return
			}
		}
	}

	// Text line — pending title or content.
	if p.pendingTitle {
		top := p.stack[len(p.stack)-1]
		if top != p.root {
			top.sec.Heading = line
			p.pendingTitle = false
			return
		}
	}
	p.pendingTitle = false
	p.appendContent(line)
}

// acceptMatra implements the monotonic filter for มาตรา numbers. Amendment
// suffixes (ทวิ/ตรี/จัตวา) are accepted as same-number variants (e.g. มาตรา 5
// then มาตรา 5 ทวิ is valid).
func (p *thParser) acceptMatra(num int, suffix string) bool {
	if p.lastMatra == 0 && num == 1 && suffix == "" {
		p.lastMatra = 1
		return true
	}
	if suffix != "" && num == p.lastMatra {
		return true // amendment variant of current number
	}
	if num == p.lastMatra+1 && suffix == "" {
		p.lastMatra = num
		return true
	}
	return false
}

// thMastraLabelSeg builds the human label and citation-path segment for a มาตรา.
func thMastraLabelSeg(rawNum, suffix string) (string, string) {
	arabicNum := thaiToArabic(rawNum)
	label := "มาตรา " + arabicNum
	seg := "section-" + arabicNum
	if suffix != "" {
		label += " " + suffix
		if sfx, ok := thAmendSuffixes[suffix]; ok {
			seg += sfx
		}
	}
	return label, seg
}

func (p *thParser) inSection() bool {
	for i := len(p.stack) - 1; i >= 0; i-- {
		if p.stack[i].sec.Kind == "section" {
			return true
		}
	}
	return false
}

func (p *thParser) push(kind, label string, level int, seg string) {
	p.pushWithOrdinal(kind, label, level, seg, 0)
}

func (p *thParser) pushWithOrdinal(kind, label string, level int, seg string, ord int) {
	p.pendingTitle = false
	for len(p.stack) > 1 && p.stack[len(p.stack)-1].level >= level {
		p.stack = p.stack[:len(p.stack)-1]
	}
	parent := p.stack[len(p.stack)-1]

	if ord == 0 {
		for _, c := range parent.children {
			if c.sec.Kind == kind {
				ord++
			}
		}
		ord++
	}

	seg = uniqueSeg(parent, seg)
	path := seg
	if parent.sec.CitationPath != "" {
		path = parent.sec.CitationPath + "/" + seg
	}
	node := &myBuild{level: level, sec: Section{
		Kind: kind, Ordinal: ord, Label: label, CitationPath: path,
	}}
	parent.children = append(parent.children, node)
	p.stack = append(p.stack, node)
}

func (p *thParser) appendContent(s string) {
	top := p.stack[len(p.stack)-1]
	if top == p.root {
		return
	}
	if top.sec.Content != "" {
		top.sec.Content += "\n"
	}
	top.sec.Content += strings.TrimSpace(s)
}

func (p *thParser) setHeading(h string) {
	top := p.stack[len(p.stack)-1]
	if top != p.root {
		top.sec.Heading = strings.TrimSpace(h)
	}
}

// ---- BOT notification parser ----------------------------------------------

// parseBOTNotification parses a Bank of Thailand notification using ข้อ (clause)
// as the primary provision unit, with (๑)(๒) numbered sub-items.
func parseBOTNotification(lines []string) []Section {
	p := &thBOTParser{root: &myBuild{level: -1}}
	p.stack = []*myBuild{p.root}
	for _, line := range lines {
		p.consume(line)
	}
	return p.root.toSections()
}

type thBOTParser struct {
	root     *myBuild
	stack    []*myBuild
	lastKhor int  // highest ข้อ number accepted (monotonic)
	inNotes  bool // after หมายเหตุ
}

func (p *thBOTParser) consume(line string) {
	if p.inNotes {
		p.appendContent(line)
		return
	}

	if thNotesRe.MatchString(line) {
		p.inNotes = true
		p.push("heading", line, thLevelPart, slug(line))
		return
	}

	// ข้อ N — Clause (the chunking unit for BOT).
	if m := thKhorInlineRe.FindStringSubmatch(line); m != nil {
		num := thaiParseInt(m[1])
		if num > 0 && p.acceptKhor(num) {
			label := "ข้อ " + thaiToArabic(m[1])
			seg := "section-" + strconv.Itoa(num)
			p.pushWithOrdinal("section", label, thLevelSection, seg, num)
			if rest := strings.TrimSpace(m[2]); rest != "" {
				p.appendContent(rest)
			}
			return
		}
	}
	if m := thKhorRe.FindStringSubmatch(line); m != nil {
		num := thaiParseInt(m[1])
		if num > 0 && p.acceptKhor(num) {
			label := "ข้อ " + thaiToArabic(m[1])
			seg := "section-" + strconv.Itoa(num)
			p.pushWithOrdinal("section", label, thLevelSection, seg, num)
			return
		}
	}

	// Numbered items: (๑), (๒) — only inside a ข้อ.
	if p.inSection() {
		if m := thThaiItemRe.FindStringSubmatch(line); m != nil {
			num := thaiParseInt(m[1])
			if num > 0 {
				label := "(" + thaiToArabic(m[1]) + ")"
				p.pushWithOrdinal("paragraph", label, thLevelItem, "paragraph-"+strconv.Itoa(num), num)
				p.appendContent(m[2])
				return
			}
		}
	}

	p.appendContent(line)
}

func (p *thBOTParser) acceptKhor(num int) bool {
	if p.lastKhor == 0 && num == 1 {
		p.lastKhor = 1
		return true
	}
	if num == p.lastKhor+1 {
		p.lastKhor = num
		return true
	}
	return false
}

func (p *thBOTParser) inSection() bool {
	for i := len(p.stack) - 1; i >= 0; i-- {
		if p.stack[i].sec.Kind == "section" {
			return true
		}
	}
	return false
}

func (p *thBOTParser) push(kind, label string, level int, seg string) {
	p.pushWithOrdinal(kind, label, level, seg, 0)
}

func (p *thBOTParser) pushWithOrdinal(kind, label string, level int, seg string, ord int) {
	for len(p.stack) > 1 && p.stack[len(p.stack)-1].level >= level {
		p.stack = p.stack[:len(p.stack)-1]
	}
	parent := p.stack[len(p.stack)-1]

	if ord == 0 {
		for _, c := range parent.children {
			if c.sec.Kind == kind {
				ord++
			}
		}
		ord++
	}

	seg = uniqueSeg(parent, seg)
	path := seg
	if parent.sec.CitationPath != "" {
		path = parent.sec.CitationPath + "/" + seg
	}
	node := &myBuild{level: level, sec: Section{
		Kind: kind, Ordinal: ord, Label: label, CitationPath: path,
	}}
	parent.children = append(parent.children, node)
	p.stack = append(p.stack, node)
}

func (p *thBOTParser) appendContent(s string) {
	top := p.stack[len(p.stack)-1]
	if top == p.root {
		return
	}
	if top.sec.Content != "" {
		top.sec.Content += "\n"
	}
	top.sec.Content += strings.TrimSpace(s)
}
