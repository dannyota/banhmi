package sbvhanoi

import (
	"context"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"

	"danny.vn/banhmi/pkg/ingest"
)

// SearchByNumber looks up one SBV legal document by exact số ký hiệu using the
// portal's keyword filter, then verifies normalized equality locally. The portal
// filters server-side on the number, so the title hint is not needed here.
func (s *Source) SearchByNumber(ctx context.Context, number, _ string) (*ingest.DiscoveredDoc, bool, error) {
	number = strings.TrimSpace(number)
	if number == "" {
		return nil, false, nil
	}
	docs, err := s.Discover(ctx, time.Time{}, number)
	if err != nil {
		return nil, false, err
	}
	want := comparableNumber(number)
	for _, d := range docs {
		if comparableNumber(d.Number) == want {
			return &d, true, nil
		}
	}
	return nil, false, nil
}

func comparableNumber(s string) string {
	s = norm.NFC.String(strings.ToUpper(strings.TrimSpace(s)))
	repl := strings.NewReplacer(" ", "", "Đ", "D", "–", "-", "—", "-")
	return repl.Replace(s)
}
