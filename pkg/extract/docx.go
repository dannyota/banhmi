package extract

import (
	"regexp"

	"danny.vn/banhmi/internal/corpus/conversion"
)

// DOCX extracts paragraph text and Markdown tables from an OOXML package.
// A blank line precedes Vietnamese legal headings for legacy chunk boundaries.
func DOCX(data []byte) (string, error) {
	return conversion.DOCX(data, legalHeading.MatchString)
}

// legalHeading matches a paragraph opening a legal structural unit, so a blank
// line can be emitted before it for the chunker. Tested on NFC text.
var legalHeading = regexp.MustCompile(`^(Điều|Khoản|Điểm|Mục|Chương|Phần)\s+[\dIVXLC]`)
