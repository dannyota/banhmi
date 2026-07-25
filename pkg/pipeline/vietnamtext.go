package pipeline

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// vietnamtext.go carries VN-only text cleanups applied inside the Vietnamese
// structure parser (ParseSections). It is the VN twin of the ID
// idCollapsePasalDigitSpaces cleanup that sits inside idBodyLines: the
// jurisdiction descriptor selects ParseSections for VN (ParserVNMarkdown) only,
// so these cleanups never touch the other five corpora.

// vnAllVowels lists every Vietnamese vowel letter (lower case) — plain, marked
// (ă â ê ô ơ ư), and every tone variant. Used to decide whether a token starts
// with a vowel.
const vnAllVowels = "aáàảãạăắằẳẵặâấầẩẫậ" +
	"eéèẻẽẹêếềểễệ" +
	"iíìỉĩị" +
	"oóòỏõọôốồổỗộơớờởỡợ" +
	"uúùủũụưứừửữự" +
	"yýỳỷỹỵ"

// vnPlainVowels are the six unmarked vowels. A token ending in one of these is
// NOT treated as a "diacritic vowel" end, so a plain-vowel-ending word never
// absorbs a following coda fragment (precision guard).
const vnPlainVowels = "aeiouy"

var (
	vnVowelSet         = runeSet(vnAllVowels)
	vnDiacriticVowelOf = diacriticVowelSet()

	// vnBareOnsets are consonant onsets that can never be a standalone
	// Vietnamese word (no vowel nucleus); a lone token equal to one is always a
	// syllable split by the mupdf spacing artifact.
	vnBareOnsets = map[string]bool{
		"kh": true, "ngh": true, "ng": true, "th": true, "tr": true,
		"ph": true, "ch": true, "gh": true, "nh": true, "qu": true, "gi": true,
	}

	// vnCodaFragments are the only valid Vietnamese syllable-final consonants.
	// A standalone token equal to one of these is a broken coda that belongs to
	// the preceding vowel-ending syllable — unless a vowel follows it, in which
	// case it may be an onset instead (guarded at the call site).
	vnCodaFragments = map[string]bool{
		"c": true, "ch": true, "m": true, "n": true,
		"ng": true, "nh": true, "p": true, "t": true,
	}
)

func runeSet(s string) map[rune]bool {
	m := make(map[rune]bool, len(s))
	for _, r := range s {
		m[r] = true
	}
	return m
}

// diacriticVowelSet is every Vietnamese vowel that carries a mark (all vowels
// except the six plain ones).
func diacriticVowelSet() map[rune]bool {
	m := runeSet(vnAllVowels)
	for _, r := range vnPlainVowels {
		delete(m, r)
	}
	return m
}

// vnCollapseSpacedDiacritics rejoins Vietnamese syllables that the mupdf text
// layer split with a stray space ("kh oản" → "khoản", "lệ nh" → "lệnh",
// "tổng kh ố i lượng" → "tổng khối lượng"). It is the SAFE variant: it only
// rejoins consonant onsets/codas that can never be standalone words, and only
// when the neighbour orthography confirms the join. It never merges enumeration
// markers, Roman numerals, digit-bearing tokens, or anything ambiguous — when in
// doubt it leaves the text untouched. Processing is per line so a merge never
// crosses a newline.
func vnCollapseSpacedDiacritics(text string) string {
	if !strings.Contains(text, " ") {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = vnCollapseSpacedDiacriticsLine(vnStripLoneReplacementMarks(line))
	}
	return strings.Join(lines, "\n")
}

// vnStripLoneReplacementMarks removes standalone U+FFFD tokens: vbpl encodes
// its consolidation footnote marker glyph badly, so tree/HTML text carries a
// lone "�" before each footnote ("� Khoản này được sửa đổi theo…"). A single
// replacement char between spaces is that marker — never legal content. Runs
// of several replacement chars are left intact so genuinely garbled text stays
// visible to the mojibake gap detector.
func vnStripLoneReplacementMarks(line string) string {
	if !strings.ContainsRune(line, '�') {
		return line
	}
	toks := strings.Split(line, " ")
	out := toks[:0]
	for _, tok := range toks {
		if tok == "�" {
			continue
		}
		out = append(out, tok)
	}
	return strings.Join(out, " ")
}

func vnCollapseSpacedDiacriticsLine(line string) string {
	if !strings.Contains(line, " ") {
		return line
	}
	// Split on single spaces so runs of spaces (indentation, table cells) survive
	// as empty tokens and are re-emitted verbatim; only spaces consumed by a merge
	// are removed.
	toks := strings.Split(line, " ")
	out := make([]string, 0, len(toks))
	merged := false
	i := 0
	for i < len(toks) {
		cur := toks[i]
		i++
		if cur == "" {
			out = append(out, cur)
			continue
		}
		for i < len(toks) {
			next := toks[i]
			if next == "" {
				break
			}
			// Wedge: onset + lone marked vowel + vowel-initial fragment
			// ("kh ố i" → "khối"). The lone marked vowel between two fragments is
			// itself an artifact, so all three rejoin.
			if i+1 < len(toks) && vnIsBareOnset(cur) && vnIsLoneDiacriticVowel(next) &&
				vnStartsWithVowel(toks[i+1]) && !vnStartsUpper(toks[i+1]) &&
				!vnHasDigit(next) && !vnHasDigit(toks[i+1]) {
				cur += next + toks[i+1]
				i += 2
				merged = true
				continue
			}
			// Onset right-merge: a bare onset absorbs a following vowel-initial
			// neighbour ("kh oản" → "khoản", "ph ò" → "phò", "th ể" → "thể").
			if vnIsBareOnset(cur) && vnStartsWithVowel(next) &&
				!vnStartsUpper(next) && !vnHasDigit(next) && !vnIsRoman(next) {
				cur += next
				i++
				merged = true
				continue
			}
			// Coda left-merge: a syllable ending in a marked vowel absorbs a
			// following bare coda fragment ("hoạ t" → "hoạt", "HÀ NG" → "HÀNG").
			// Skip when a vowel follows the fragment, since the fragment is then
			// an onset of the next syllable rather than a coda of this one.
			if vnEndsWithDiacriticVowel(cur) && vnIsCodaFragment(next) &&
				(i+1 >= len(toks) || !vnStartsWithVowel(toks[i+1])) {
				cur += next
				i++
				merged = true
				continue
			}
			break
		}
		out = append(out, cur)
	}
	if !merged {
		return line
	}
	return strings.Join(out, " ")
}

func vnIsBareOnset(tok string) bool {
	return vnBareOnsets[strings.ToLower(tok)]
}

func vnIsCodaFragment(tok string) bool {
	if vnHasDigit(tok) || vnIsRoman(tok) {
		return false
	}
	return vnCodaFragments[strings.ToLower(tok)]
}

func vnStartsWithVowel(tok string) bool {
	for _, r := range tok {
		return vnVowelSet[unicode.ToLower(r)]
	}
	return false
}

func vnEndsWithDiacriticVowel(tok string) bool {
	r, size := utf8.DecodeLastRuneInString(tok)
	if size == 0 || r == utf8.RuneError {
		return false
	}
	return vnDiacriticVowelOf[unicode.ToLower(r)]
}

func vnIsLoneDiacriticVowel(tok string) bool {
	if utf8.RuneCountInString(tok) != 1 {
		return false
	}
	r, _ := utf8.DecodeRuneInString(tok)
	return vnDiacriticVowelOf[unicode.ToLower(r)]
}

func vnStartsUpper(tok string) bool {
	for _, r := range tok {
		return unicode.IsUpper(r)
	}
	return false
}

func vnHasDigit(tok string) bool {
	for _, r := range tok {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}

// vnIsRoman reports whether tok is a bare upper-case Roman numeral (I, V, X, …),
// which enumerates chapters/appendices and must never be merged.
func vnIsRoman(tok string) bool {
	if tok == "" {
		return false
	}
	for _, r := range tok {
		switch r {
		case 'I', 'V', 'X', 'L', 'C', 'D', 'M':
		default:
			return false
		}
	}
	return true
}
