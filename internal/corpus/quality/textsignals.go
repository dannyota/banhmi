package quality

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// legacyTextSignalsVersion identifies diagnostic arithmetic, not qualification.
const legacyTextSignalsVersion = "legacy-text-signals-v1"

func textSignals(text string) []string {
	var total, letters, nonASCII, bad, whitespace, pua, markers, cyrillic int64
	for _, r := range text {
		total++
		if r == '\ufffd' || (unicode.IsControl(r) && r != '\n' && r != '\t' && r != '\r' && r != '\f') {
			bad++
		}
		if unicode.IsLetter(r) {
			letters++
			if r > unicode.MaxASCII {
				nonASCII++
			}
		}
		if unicode.IsSpace(r) {
			whitespace++
		}
		if r >= 0xe000 && r <= 0xf8ff {
			pua++
		}
		if strings.ContainsRune("√∆·ªƒ∫≠‚ÄØ", r) {
			markers++
		}
		if r >= 0x0400 && r <= 0x04ff {
			cyrillic++
		}
	}
	if total == 0 {
		return []string{"TEXT_EMPTY"}
	}
	// Use the same overflow-safe rational comparison as policy thresholds.
	greater := func(n, d int64, numerator, denominator uint64) bool {
		maximum := threshold{numerator: numerator, denominator: denominator}
		return !maximum.passes(n, d)
	}
	conditions := []struct {
		code string
		on   bool
	}{
		{"TEXT_REPLACEMENT_CHARS", greater(bad, total, 1, 100)},
		{"TEXT_TOO_FEW_LETTERS", letters < 50},
		{"TEXT_HIGH_WHITESPACE", greater(whitespace, total, 2, 5)},
		{"TEXT_PUA_RUNES", greater(pua, total, 1, 200)},
		{"TEXT_MOJIBAKE_MARKERS", markers >= 8 && greater(markers, total, 1, 200)},
		{"TEXT_CYRILLIC_MOJIBAKE", cyrillic >= 8 && (threshold{minimum: true, numerator: 1, denominator: 200}).passes(cyrillic, total)},
		{"TEXT_LOW_DIACRITIC_DENSITY", letters >= 200 && !(threshold{minimum: true, numerator: 1, denominator: 50}).passes(nonASCII, letters)},
		{"SOURCE_PLACEHOLDER", placeholder(text)},
	}
	codes := make([]string, 0, len(conditions))
	for _, condition := range conditions {
		if condition.on {
			codes = append(codes, condition.code)
		}
	}
	return codes
}

func placeholder(text string) bool {
	var folded strings.Builder
	for _, r := range norm.NFD.String(strings.ToLower(strings.Join(strings.Fields(norm.NFC.String(text)), " "))) {
		switch {
		case r == 'đ':
			folded.WriteByte('d')
		case unicode.Is(unicode.Mn, r):
			continue
		default:
			folded.WriteRune(r)
		}
	}
	s := folded.String()
	letters := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			letters++
		}
	}
	return letters <= 200 && strings.Contains(s, "dang cap nhat") &&
		(strings.Contains(s, "file") || strings.Contains(s, "dinh kem") || letters < 80)
}
