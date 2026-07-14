package pipeline

import (
	"regexp"
	"strconv"
	"strings"
)

// ParseIndonesianUU parses the text of an Indonesian regulation (UU, PP, POJK,
// SEOJK, PBI, PADG) — typically extracted from a scanned+OCR'd BPK PDF via
// pdftotext or MarkItDown — into the same []Section tree that the VN and MY
// parsers produce.
//
// Indonesian provision hierarchy (Kind strings, lowercase):
//
//	bab (BAB I) > bagian (Bagian Kesatu) > paragraf (Paragraf 1) >
//	pasal (Pasal 1, THE chunking unit) > ayat ((1)) > huruf (a.)
//
// Plus penjelasan (explanatory notes) and lampiran (annexes).
//
// The parser is a deterministic line-by-line state machine — no AI — tuned for
// the systematic OCR noise in BPK scanned PDFs: digit confusion (O→0, I/l→1,
// T→7), missing spaces, trailing dots, and per-page watermarks/headers. A
// monotonic Pasal filter (accept only if num == last+1, first must be 1)
// rejects cross-references and PENJELASAN's re-numbered articles.
//
// Proven on UU 27/2022 PDP: 76 Pasal (0 gaps, 0 duplicates).
func ParseIndonesianUU(text string) []Section {
	p := &idParser{root: &myBuild{level: -1}}
	p.stack = []*myBuild{p.root}
	for _, line := range idBodyLines(text) {
		p.consume(line)
	}
	return p.root.toSections()
}

// ---- OCR noise stripping ---------------------------------------------------

var (
	// idSKNoRe matches the "SK No 016999A" watermark lines.
	idSKNoRe = regexp.MustCompile(`(?i)^SK\s*No\s*\d+\s*A?\s*$`)

	// idPresHeaderRe matches per-page "PRESIDEN"/"FRESIDEN"/"PRESTDEN"/
	// "PRESlDEN" etc — fuzzy for OCR confusion (F/P, U/LI, ]/I, S/5, T/I).
	idPresHeaderRe = regexp.MustCompile(`(?i)^(?:P(?:RE|RES[T1I]?)D?EN|FRES[I1]DEN|PRESTDEN|PRESLDEN)$`)

	// idRepublikRe matches "REPUBLIK INDONESIA" and its OCR variants:
	// "REPUELIK", "REPI.JBLIK", "REPTIBLIK", "UBLIK INDONESlA",
	// "REPUBLIK INDONES", "UALIK INDONES", etc.
	idRepublikRe = regexp.MustCompile(`(?i)^(?:REP.{0,6}LIK|UBLIK|UALIK)\s+INDONES.*$`)

	// idPageDashRe matches hyphenated page markers: "- 2 -", "-6Pasal 7",
	// "- 15-", etc. These lines start with a dash+digits and are OCR artefacts.
	idPageDashRe = regexp.MustCompile(`^-\s*\d+`)

	// idBarePageNoRe matches standalone page numbers.
	idBarePageNoRe = regexp.MustCompile(`^\d+\s*$`)

	// idBrokenRepublikRe matches broken OCR fragments of "REPUBLIK": "BLIK",
	// "ELIK" followed by "INDONESIA" or similar.
	idBrokenRepublikRe = regexp.MustCompile(`(?i)^[BE]LIK\s+INDONES`)
)

// idBodyLines strips per-page OCR noise and cuts the front matter (title,
// DENGAN RAHMAT TUHAN, Menimbang/Mengingat) at the first "BAB I" or "Pasal 1"
// (whichever comes first), returning non-empty body lines.
func idBodyLines(text string) []string {
	text = strings.ReplaceAll(text, " ", " ")
	text = strings.ReplaceAll(text, " ", " ")
	raw := strings.Split(text, "\n")

	// Find the start of the body: first BAB or first standalone Pasal heading.
	start := 0
	for i, ln := range raw {
		t := strings.TrimSpace(ln)
		if idBabRe.MatchString(t) {
			start = i
			break
		}
		if m := idPasalHeadingRe.FindStringSubmatch(t); m != nil {
			num := idFixOCRNumber(m[1])
			if num == 1 {
				start = i
				break
			}
		}
	}

	var out []string
	for _, ln := range raw[start:] {
		t := strings.TrimSpace(ln)
		if t == "" || isIDPageNoise(t) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// isIDPageNoise returns true for lines that are per-page OCR artefacts.
func isIDPageNoise(t string) bool {
	if idSKNoRe.MatchString(t) {
		return true
	}
	if idPresHeaderRe.MatchString(t) {
		return true
	}
	if idRepublikRe.MatchString(t) {
		return true
	}
	if idPageDashRe.MatchString(t) {
		return true
	}
	if idBarePageNoRe.MatchString(t) {
		return true
	}
	if idBrokenRepublikRe.MatchString(t) {
		return true
	}
	return false
}

// ---- patterns (anchored at line start) ------------------------------------

var (
	// idBabRe matches "BAB I", "BAB XVI", "BAB xII", "BABVIII" (OCR may
	// produce lowercase or omit the space). Trailing dots/spaces tolerated.
	idBabRe = regexp.MustCompile(`(?i)^BAB\s*([IVXLCDMivxlcdm]+)\s*[.\s]*$`)

	// idBagianRe matches "Bagian Kesatu", "Bagian Kedua", etc.
	idBagianRe = regexp.MustCompile(`(?i)^Bagian\s+(\S+)`)

	// idParagrafRe matches "Paragraf 1", "Paragraf 2", etc.
	idParagrafRe = regexp.MustCompile(`(?i)^Paragraf\s+(\d+)`)

	// idPasalHeadingRe matches Pasal headings with OCR noise. Handles both
	// standalone headings ("Pasal 7") and inline cases where BPK OCR merges
	// the heading with the first ayat or sentence on the same line:
	//   "Pasal 7", "Pasal I", "Pasa722", "Pasal2T", "PasalT2",
	//   "Pasal 7. . .", "Pasal 1O", "Pasal 25...",
	//   "Pasal 2 (1) Undang-Undang ini berlaku...",
	//   "Pasal 5 Subjek Data Pribadi berhak..."
	// Group 1 captures the raw number (may contain O/I/l/T).
	// Group 2 captures any trailing text after the number (may be empty).
	// The regex allows optional space between "Pasal/Pasa7/..." and the number,
	// and tolerates trailing dots/spaces before any inline content.
	idPasalHeadingRe = regexp.MustCompile(`^Pasa[l17]\s*([0-9OoIlT]+)\s*[.\s]*(.*)$`)

	// idAyatRe matches ayat: "(1)", "(2)", "(1O)", "(1l)".
	idAyatRe = regexp.MustCompile(`^\(([0-9OoIlT]+)\)\s*(.*)$`)

	// idHurufRe matches huruf: "a.", "b.", "c.".
	idHurufRe = regexp.MustCompile(`^([a-z])\.\s*(.*)$`)

	// idPenjelasanBannerRe matches the "PENJELASAN ATAS..." banner.
	idPenjelasanBannerRe = regexp.MustCompile(`(?i)^PENJ[E\[]L[A\s]*S[A\s]*N`)

	// idPasalDemiPasalRe matches "PASAL DEMI PASAL" or "II. PASAL" (with OCR
	// noise like "PASALDEMIPASAL").
	idPasalDemiPasalRe = regexp.MustCompile(`(?i)PASAL\s*DEMI\s*PASAL|^II\.\s*PASAL`)

	// idLampiranRe matches "LAMPIRAN" annex headings.
	idLampiranRe = regexp.MustCompile(`(?i)^LAMPIRAN`)
)

// ---- hierarchy levels -----------------------------------------------------

const (
	idLevelBab = iota
	idLevelBagian
	idLevelParagraf
	idLevelPasal
	idLevelAyat
	idLevelHuruf
)

// ---- OCR fixup ------------------------------------------------------------

// idFixOCRNumber fixes OCR digit confusion in a raw number string:
// O/o→0, I/l→1, T→7, then strips any remaining non-digits and parses.
func idFixOCRNumber(s string) int {
	s = strings.ReplaceAll(s, "O", "0")
	s = strings.ReplaceAll(s, "o", "0")
	s = strings.ReplaceAll(s, "I", "1")
	s = strings.ReplaceAll(s, "l", "1")
	s = strings.ReplaceAll(s, "T", "7")
	// Strip any remaining non-digit characters (dots, spaces).
	var clean strings.Builder
	for _, c := range s {
		if c >= '0' && c <= '9' {
			clean.WriteRune(c)
		}
	}
	n, err := strconv.Atoi(clean.String())
	if err != nil {
		return -1
	}
	return n
}

// ---- Indonesian ordinal words ---------------------------------------------

// idOrdinalWords maps Indonesian spelled-out ordinals used in Bagian headings.
var idOrdinalWords = map[string]int{
	"kesatu":      1,
	"kedua":       2,
	"ketiga":      3,
	"keempat":     4,
	"kelima":      5,
	"keenam":      6,
	"ketujuh":     7,
	"kedelapan":   8,
	"kesembilan":  9,
	"kesepuluh":   10,
	"kesebelas":   11,
	"kedua belas": 12, // not reachable via single-word match but kept for completeness
}

// idBagianOrdinal parses a Bagian ordinal word to an integer. Falls back to a
// running counter if the word is unrecognized.
func idBagianOrdinal(word string) int {
	if n, ok := idOrdinalWords[strings.ToLower(word)]; ok {
		return n
	}
	return 0 // caller uses running counter
}

// ---- state machine --------------------------------------------------------

type idParser struct {
	root         *myBuild
	stack        []*myBuild
	lastPasal    int  // highest Pasal number accepted (monotonic 1..N)
	inPenjelasan bool // once PENJELASAN starts, stop main-body parsing
	inLampiran   bool // annex section
	pendingTitle bool // top structural node awaiting its title line(s)
}

func (p *idParser) consume(line string) {
	// Detect PENJELASAN — stop main-body parsing.
	if idPenjelasanBannerRe.MatchString(line) {
		p.inPenjelasan = true
		return
	}
	if idPasalDemiPasalRe.MatchString(line) {
		p.inPenjelasan = true
		// Emit PENJELASAN as a single top-level section. We'll collect content
		// from here on in the penjelasan node.
		p.push("penjelasan", "Penjelasan", idLevelBab, "penjelasan")
		return
	}
	if p.inPenjelasan {
		// Accumulate all penjelasan text as flat content.
		p.appendContent(line)
		return
	}

	// Detect LAMPIRAN.
	if idLampiranRe.MatchString(line) {
		p.inLampiran = true
		p.push("lampiran", line, idLevelBab, slug(line))
		return
	}
	if p.inLampiran {
		p.appendContent(line)
		return
	}

	// BAB heading.
	if m := idBabRe.FindStringSubmatch(line); m != nil {
		numeral := strings.ToUpper(m[1])
		ord := romanToInt(numeral)
		label := "BAB " + numeral
		p.push("bab", label, idLevelBab, "bab-"+strings.ToLower(numeral))
		// The heading title is usually on the next line(s) — mark pending.
		p.pendingTitle = true
		_ = ord // ordinal set by push via running counter if needed
		return
	}

	// Bagian heading.
	if m := idBagianRe.FindStringSubmatch(line); m != nil {
		word := m[1]
		ord := idBagianOrdinal(word)
		label := "Bagian " + word
		seg := "bagian-" + strings.ToLower(word)
		p.pushWithOrdinal("bagian", label, idLevelBagian, seg, ord)
		p.pendingTitle = true
		return
	}

	// Paragraf heading.
	if m := idParagrafRe.FindStringSubmatch(line); m != nil {
		num, _ := strconv.Atoi(m[1])
		label := "Paragraf " + m[1]
		p.pushWithOrdinal("paragraf", label, idLevelParagraf, "paragraf-"+m[1], num)
		p.pendingTitle = true
		return
	}

	// Pasal heading — monotonic filter.
	if m := idPasalHeadingRe.FindStringSubmatch(line); m != nil {
		num := idFixOCRNumber(m[1])
		if num > 0 && p.acceptPasal(num) {
			label := "Pasal " + strconv.Itoa(num)
			p.pushWithOrdinal("pasal", label, idLevelPasal, "pasal-"+strconv.Itoa(num), num)
			// BPK OCR often merges the Pasal number with the first ayat or
			// sentence on the same line. Feed any trailing text back through
			// the parser so it gets parsed as ayat/huruf/content.
			if trailing := strings.TrimSpace(m[2]); trailing != "" {
				p.consume(trailing)
			}
			return
		}
	}

	// Ayat: "(1) text..." — only inside an open Pasal.
	if p.inPasal() {
		if m := idAyatRe.FindStringSubmatch(line); m != nil {
			num := idFixOCRNumber(m[1])
			if num > 0 {
				label := "ayat (" + strconv.Itoa(num) + ")"
				p.pushWithOrdinal("ayat", label, idLevelAyat, "ayat-"+strconv.Itoa(num), num)
				if rest := strings.TrimSpace(m[2]); rest != "" {
					p.appendContent(rest)
				}
				return
			}
		}

		// Huruf: "a. text..." — only inside an open Pasal (or ayat).
		if m := idHurufRe.FindStringSubmatch(line); m != nil {
			letter := m[1]
			ord := int(letter[0]-'a') + 1
			label := "huruf " + letter
			p.pushWithOrdinal("huruf", label, idLevelHuruf, "huruf-"+letter, ord)
			if rest := strings.TrimSpace(m[2]); rest != "" {
				p.appendContent(rest)
			}
			return
		}
	}

	// Text line — attach to current node or use as pending title.
	if p.pendingTitle {
		top := p.stack[len(p.stack)-1]
		if top != p.root {
			if top.sec.Heading == "" {
				top.sec.Heading = line
			} else {
				// Multi-line heading: append to existing heading.
				top.sec.Heading += " " + line
			}
			// Check if this looks like a heading continuation (short all-caps or
			// title-case line). Stay pending for one more line unless this line
			// contains a sentence (period + lowercase).
			if !looksLikeHeadingContinuation(line) {
				p.pendingTitle = false
			}
			return
		}
	}
	p.pendingTitle = false
	p.appendContent(line)
}

// looksLikeHeadingContinuation returns true if the line looks like it could be
// a continuation of a structural heading (short, mostly uppercase).
func looksLikeHeadingContinuation(line string) bool {
	// If the line is short and mostly uppercase, it's likely a heading continuation.
	runes := []rune(line)
	if len(runes) > 60 {
		return false
	}
	upper := 0
	letters := 0
	for _, r := range runes {
		if r >= 'A' && r <= 'Z' {
			upper++
			letters++
		} else if r >= 'a' && r <= 'z' {
			letters++
		}
	}
	if letters == 0 {
		return false
	}
	return float64(upper)/float64(letters) >= 0.6
}

func (p *idParser) acceptPasal(num int) bool {
	if p.lastPasal == 0 && num == 1 {
		p.lastPasal = 1
		return true
	}
	if num == p.lastPasal+1 {
		p.lastPasal = num
		return true
	}
	return false
}

func (p *idParser) inPasal() bool {
	for i := len(p.stack) - 1; i >= 0; i-- {
		if p.stack[i].sec.Kind == "pasal" {
			return true
		}
	}
	return false
}

func (p *idParser) push(kind, label string, level int, seg string) {
	p.pushWithOrdinal(kind, label, level, seg, 0)
}

func (p *idParser) pushWithOrdinal(kind, label string, level int, seg string, ord int) {
	p.pendingTitle = false
	for len(p.stack) > 1 && p.stack[len(p.stack)-1].level >= level {
		p.stack = p.stack[:len(p.stack)-1]
	}
	parent := p.stack[len(p.stack)-1]

	// Compute ordinal from siblings if not provided.
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

func (p *idParser) appendContent(s string) {
	top := p.stack[len(p.stack)-1]
	if top == p.root {
		return // stray text before first heading
	}
	if top.sec.Content != "" {
		top.sec.Content += "\n"
	}
	top.sec.Content += strings.TrimSpace(s)
}
