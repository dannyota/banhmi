package pipeline

import (
	"testing"

	dbbronze "danny.vn/banhmi/pkg/store/bronze"
)

func TestDocNumberInSetMatchesNormalizedVBPLNumber(t *testing.T) {
	raw := "01/2024/QĐ-NHNN"
	rows := []dbbronze.BronzeSourceDocument{
		{DocNumber: &raw},
		{DocNumberNorm: "182024TTNHNN"},
	}
	set := map[string]struct{}{}
	addDocNumbers(set, rows)

	for _, number := range []string{"01 / 2024 / QĐ - NHNN", "18/2024/TT-NHNN"} {
		if !docNumberInSet(number, set) {
			t.Fatalf("docNumberInSet(%q) = false, want true", number)
		}
	}
	if docNumberInSet("", set) {
		t.Fatal("docNumberInSet(empty) = true, want false")
	}
	if docNumberInSet("19/2024/TT-NHNN", set) {
		t.Fatal("docNumberInSet(non-duplicate) = true, want false")
	}
}

func TestMatchLocalDiscoveryKeywords(t *testing.T) {
	keywords := normalizeLocalDiscoveryKeywords([]string{
		"an toàn bảo mật",
		"thanh toán trực tuyến",
		"chữ ký số",
	})

	tests := []struct {
		name     string
		number   string
		title    string
		wantTerm string
		want     bool
	}{
		{
			name:     "punctuation normalized",
			number:   "2345/QĐ-NHNN",
			title:    "QĐ về triển khai các giải pháp an toàn, bảo mật trong thánh toán trực tuyến",
			wantTerm: "an toàn bảo mật",
			want:     true,
		},
		{
			name:     "payment keyword",
			number:   "2872/QĐ-NHNN",
			title:    "Bãi bỏ quyết định về thanh toán trực tuyến",
			wantTerm: "thanh toán trực tuyến",
			want:     true,
		},
		{
			name:   "diacritics are not folded",
			number: "999/QĐ-NHNN",
			title:  "Quyết định về an toan bao mat trong thanh toan truc tuyen",
			want:   false,
		},
		{
			name:   "capital adequacy is not security",
			number: "01/TT-NHNN",
			title:  "Thông tư quy định tỷ lệ an toàn vốn",
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchLocalDiscoveryKeywords(tt.number, tt.title, "", keywords)
			if tt.want && len(got) == 0 {
				t.Fatalf("matchLocalDiscoveryKeywords() = nil, want %q", tt.wantTerm)
			}
			if !tt.want && len(got) > 0 {
				t.Fatalf("matchLocalDiscoveryKeywords() = %v, want no match", got)
			}
			if tt.wantTerm != "" && got[0] != tt.wantTerm {
				t.Fatalf("matchLocalDiscoveryKeywords()[0] = %q, want %q", got[0], tt.wantTerm)
			}
		})
	}
}
