package pipeline

import (
	"strings"

	"danny.vn/banhmi/pkg/extract"
)

func usableHTMLPayload(raw string) bool {
	if strings.TrimSpace(raw) == "" {
		return false
	}
	text, err := extract.HTML(raw)
	if err != nil {
		return false
	}
	return strings.TrimSpace(text) != ""
}
