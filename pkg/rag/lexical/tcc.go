// Thai Character Cluster (TCC) segmenter for BM25 tokenization.
//
// TCC is the smallest linguistically meaningful unit in Thai writing — groups of
// characters that cannot be separated without breaking orthographic rules. This
// implementation follows Theeramunkong et al. (2000) and the PyThaiNLP TCC rules.
//
// Unlike the Vietnamese normalizer (NFD strip), Thai combining marks (tone marks,
// vowel marks) are integral to the script — stripping them destroys meaning. The
// TCC normalizer segments Thai text into clusters, inserts spaces between them,
// lower-cases any Latin text, and leaves Thai code points intact.
package lexical

import (
	"regexp"
	"strings"
	"unicode"
)

// isThai reports whether r is in the Thai Unicode block (U+0E00-U+0E7F).
func isThai(r rune) bool {
	return r >= '฀' && r <= '๿'
}

// TCC pattern: built from the PyThaiNLP pattern set with substitutions applied.
//
// Substitution key:
//
//	c = [ก-ฮ]                             Thai consonant
//	t = [่-๋]?                            optional tone mark
//	d = [ุู]                              lower vowels
//	k = ([ก-ฮ]{1,2}[ุูิ]?์)?             optional final consonant cluster with cancellation mark
//
// The patterns are ordered so that longer/more-specific patterns match first.
var tccPattern *regexp.Regexp

func init() {
	// Character class building blocks.
	c := `[ก-ฮ]`
	t := `[่-๋]?`
	d := `[ุู]`
	// k: optional final consonant cluster — consonant(s) with optional lower/upper vowel + การันต์
	k := `(?:` + c + `{1,2}(?:` + d + `|ิ)?์)?`

	// The TCC pattern alternatives, ordered from most specific to least.
	// Each alternative matches one valid Thai Character Cluster.
	alts := []string{
		// Sara-am special cases
		c + `ั` + `(?:` + `[่-๋]` + c + `)?`,

		// เ- leading vowel patterns (long, must precede short)
		`เ` + c + c + `ี` + t + `ยะ` + k,
		`เ` + c + c + `ี` + t + `ย` + k,
		`เ` + c + `[ิีุู]` + t + `ย` + k,
		`เ` + c + c + `็` + c + k,
		`เ` + c + `ิ` + c + `์` + c + k,
		`เ` + c + `ิ` + t + c + k,
		`เ` + c + `ี` + t + `ยะ?` + k,
		`เ` + c + `ื` + t + `อะ` + k,
		`เ` + c + `ื`,
		`เ` + c + `็` + c + k,
		`เ` + c + c + t + `าะ` + k,
		`เ` + c + t + `า?ะ?` + k,

		// แ- leading vowel patterns
		`แ` + c + c + c + `์` + k,
		`แ` + c + c + `็` + c + k,
		`แ` + c + c + `์` + k,
		`แ` + c + `็` + c + k,
		`แ` + c + t + `ะ` + k,

		// โ- leading vowel
		`โ` + c + t + `ะ` + k,

		// Generic leading vowels เ-ไ
		`[เ-ไ]` + c + t + k,

		// Consonant-initial patterns
		c + `[ึื]` + t + c + k,
		c + `รร` + c + `์`,
		c + `[ะ-ู]` + t + k,
		c + `[ิุู]์`,
		c + `็`,
		c + t + `[ะาำ]?` + k,

		// Special fixed clusters
		`ก็`,
		`อึ`,
		`หึ`,

		// Mai yamok (ๆ) as its own cluster
		`ๆ`,

		// Thai digits as one cluster
		`[๐-๙]+`,
	}

	tccPattern = regexp.MustCompile(strings.Join(alts, "|"))
}

// tccSegment splits Thai text into Thai Character Clusters. Non-Thai runs
// (Latin, digits, punctuation, whitespace) are yielded as-is. The function
// returns a slice of cluster strings.
func tccSegment(s string) []string {
	if len(s) == 0 {
		return nil
	}

	var clusters []string
	runes := []rune(s)
	i := 0

	for i < len(runes) {
		// Non-Thai run: collect contiguous non-Thai characters.
		if !isThai(runes[i]) {
			j := i + 1
			for j < len(runes) && !isThai(runes[j]) {
				j++
			}
			clusters = append(clusters, string(runes[i:j]))
			i = j
			continue
		}

		// Thai run: extract contiguous Thai characters, then apply TCC regex.
		j := i + 1
		for j < len(runes) && isThai(runes[j]) {
			j++
		}
		thaiRun := string(runes[i:j])
		pos := 0
		for pos < len(thaiRun) {
			loc := tccPattern.FindStringIndex(thaiRun[pos:])
			if loc == nil || loc[0] != 0 {
				// No match at current position: emit one character.
				_, size := firstRune(thaiRun[pos:])
				clusters = append(clusters, thaiRun[pos:pos+size])
				pos += size
			} else {
				clusters = append(clusters, thaiRun[pos:pos+loc[1]])
				pos += loc[1]
			}
		}
		i = j
	}
	return clusters
}

// firstRune returns the first rune and its byte size from a non-empty string.
func firstRune(s string) (rune, int) {
	for _, r := range s {
		return r, len(string(r))
	}
	return 0, 0
}

// thNormalize is the Thai BM25 text normalizer. It:
//  1. Lower-cases the input (no-op for Thai script; needed for mixed Latin).
//  2. Segments Thai runs into TCCs via the regex-based segmenter.
//  3. Replaces non-letter/non-digit characters with spaces.
//  4. Returns a space-separated string of TCC tokens and Latin words.
//
// It does NOT apply NFD decomposition — Thai combining marks are integral.
func thNormalize(s string) string {
	clusters := tccSegment(s)
	var b strings.Builder
	b.Grow(len(s) + len(clusters)) // rough estimate

	for i, cl := range clusters {
		if i > 0 {
			b.WriteByte(' ')
		}
		// Keep letters, digits, and combining marks (Thai tone/vowel marks are Mn).
		wrote := false
		for _, r := range cl {
			if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) {
				b.WriteRune(unicode.ToLower(r))
				wrote = true
			} else if wrote {
				b.WriteByte(' ')
				wrote = false
			}
		}
	}
	return b.String()
}
