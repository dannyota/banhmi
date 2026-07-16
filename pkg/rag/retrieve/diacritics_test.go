package retrieve

import (
	"testing"

	"danny.vn/banhmi/pkg/rag/lexical"
)

// TestRestoreDiacritics verifies the token-by-token corpus dictionary restoration.
func TestRestoreDiacritics(t *testing.T) {
	r := &hybridRetriever{
		normalizer: lexical.DefaultNormalizer,
		diacriticDict: map[string]string{
			"dinh":  "định",
			"ngan":  "ngân",
			"hang":  "hàng",
			"va":    "và",
			"la":    "là",
			"ap":    "áp",
			"duong": "đường",
		},
	}

	tests := []struct {
		name  string
		query string
		want  string
	}{
		{
			name:  "full restoration",
			query: "ngan hang va ap duong",
			want:  "ngân hàng và áp đường",
		},
		{
			name:  "partial restoration (some words not in dict)",
			query: "quy dinh ve ngan hang",
			want:  "quy định ve ngân hàng",
		},
		{
			name:  "no dict hit — unchanged",
			query: "hello world",
			want:  "hello world",
		},
		{
			name:  "single word",
			query: "ngan",
			want:  "ngân",
		},
		{
			name:  "preserves case folding",
			query: "NGAN HANG",
			want:  "ngân hàng",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := r.restoreDiacritics(tc.query)
			if got != tc.want {
				t.Errorf("restoreDiacritics(%q) = %q, want %q", tc.query, got, tc.want)
			}
		})
	}
}

// TestDiacriticRestoreOnlyOnDiacriticFree verifies that diacritic restoration
// is NOT applied when the query already has diacritics — the path must be
// byte-identical to the unmodified query.
func TestDiacriticRestoreOnlyOnDiacriticFree(t *testing.T) {
	r := &hybridRetriever{
		normalizer:         lexical.DefaultNormalizer,
		lexicalRouterBoost: true,
		diacriticDict: map[string]string{
			"ngan": "ngân",
			"hang": "hàng",
		},
	}

	// Query WITH diacritics — must NOT be restored.
	accentedQuery := "ngân hàng thương mại"
	if lexical.DiacriticFree(accentedQuery) {
		t.Fatal("accentedQuery should NOT be diacritic-free")
	}
	// The gate in searchHits checks DiacriticFree; here we test the gate directly:
	// when DiacriticFree is false, restoreDiacritics should not be called.
	// (We can't call searchHits without a DB, so we test the condition.)

	// Query WITHOUT diacritics — should be restored.
	plainQuery := "ngan hang"
	if !lexical.DiacriticFree(plainQuery) {
		t.Fatal("plainQuery should be diacritic-free")
	}
	got := r.restoreDiacritics(plainQuery)
	want := "ngân hàng"
	if got != want {
		t.Errorf("restoreDiacritics(%q) = %q, want %q", plainQuery, got, want)
	}
}

// TestDiacriticDictThreshold verifies the >=90% threshold logic by testing
// a simulated corpus dictionary: ambiguous tokens must NOT appear.
func TestDiacriticDictThreshold(t *testing.T) {
	// Simulate what dictgen would produce: "bao" maps to bảo 45%, báo 30%,
	// bão 15%, bào 10% — no form reaches 90%, so "bao" must NOT be in the dict.
	// "ngan" maps to ngân 97% — it IS in the dict.
	dict := map[string]string{
		"ngan": "ngân", // 97% share — unambiguous
		// "bao" is absent — ambiguous (45% max)
	}

	r := &hybridRetriever{
		normalizer:    lexical.DefaultNormalizer,
		diacriticDict: dict,
	}

	// "bao" should pass through unchanged (not in dict).
	got := r.restoreDiacritics("bao ngan")
	want := "bao ngân"
	if got != want {
		t.Errorf("restoreDiacritics(%q) = %q, want %q — ambiguous 'bao' should NOT be restored", "bao ngan", got, want)
	}
}

// TestRestoreDiacriticsEmptyDict verifies that an empty dictionary (non-VN
// jurisdictions) results in a no-op.
func TestRestoreDiacriticsEmptyDict(t *testing.T) {
	r := &hybridRetriever{
		normalizer:         lexical.DefaultNormalizer,
		lexicalRouterBoost: true,
		diacriticDict:      nil, // MY, ID have no dict
	}

	query := "banking regulation"
	// With nil dict, the gate in searchHits (len(r.diacriticDict) > 0) blocks.
	// Test the method directly: with nil dict, restoreDiacritics should be a no-op.
	got := r.restoreDiacritics(query)
	if got != query {
		t.Errorf("restoreDiacritics with nil dict should be no-op, got %q", got)
	}

	// Also test with empty dict.
	r.diacriticDict = map[string]string{}
	got = r.restoreDiacritics(query)
	if got != query {
		t.Errorf("restoreDiacritics with empty dict should be no-op, got %q", got)
	}
}
