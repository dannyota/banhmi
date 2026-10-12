package extract

import "danny.vn/banhmi/internal/corpus/conversion"

// HTML extracts NFC plain text while preserving block boundaries as line breaks.
func HTML(s string) (string, error) {
	return conversion.HTML(s)
}
