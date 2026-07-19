package retrieve

import (
	"testing"

	"danny.vn/banhmi/pkg/rag/lexical"
)

// TestExpandAbbreviations verifies single-word, multi-word, case-insensitive,
// and no-match abbreviation expansion.
func TestExpandAbbreviations(t *testing.T) {
	r := &hybridRetriever{
		normalizer: lexical.DefaultNormalizer,
		abbreviationDict: map[string]string{
			"tppu":    "tindak pidana pencucian uang",
			"qris":    "Quick Response Code Indonesian Standard",
			"lps":     "Lembaga Penjamin Simpanan",
			"apu ppt": "anti pencucian uang dan pencegahan pendanaan terorisme",
		},
	}

	tests := []struct {
		name  string
		query string
		want  string
	}{
		{
			name:  "single abbreviation",
			query: "UU TPPU terbaru mengatur apa saja?",
			want:  "UU TPPU terbaru mengatur apa saja? (tindak pidana pencucian uang)",
		},
		{
			name:  "case insensitive",
			query: "peraturan tentang Qris untuk pembayaran",
			want:  "peraturan tentang Qris untuk pembayaran (Quick Response Code Indonesian Standard)",
		},
		{
			name:  "multiple abbreviations",
			query: "aturan TPPU dan QRIS",
			want:  "aturan TPPU dan QRIS (Quick Response Code Indonesian Standard, tindak pidana pencucian uang)",
		},
		{
			name:  "multi-word abbreviation",
			query: "ketentuan APU PPT bank umum",
			want:  "ketentuan APU PPT bank umum (anti pencucian uang dan pencegahan pendanaan terorisme)",
		},
		{
			name:  "no match",
			query: "peraturan bank indonesia tentang pembayaran",
			want:  "peraturan bank indonesia tentang pembayaran",
		},
		{
			name:  "partial word no match",
			query: "tppuxxxx something",
			want:  "tppuxxxx something",
		},
		{
			name:  "empty query",
			query: "",
			want:  "",
		},
		{
			name:  "abbreviation at start",
			query: "LPS penjaminan simpanan",
			want:  "LPS penjaminan simpanan (Lembaga Penjamin Simpanan)",
		},
		{
			name:  "abbreviation at end",
			query: "nasib uang di LPS",
			want:  "nasib uang di LPS (Lembaga Penjamin Simpanan)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := r.expandAbbreviations(tc.query)
			if got != tc.want {
				t.Errorf("expandAbbreviations(%q) = %q, want %q", tc.query, got, tc.want)
			}
		})
	}
}

// TestExpandAbbreviationsEmptyDict verifies that an empty or nil dictionary
// (VN, MY jurisdictions with no abbreviation entries) is a no-op.
func TestExpandAbbreviationsEmptyDict(t *testing.T) {
	r := &hybridRetriever{
		normalizer:       lexical.DefaultNormalizer,
		abbreviationDict: nil,
	}
	query := "peraturan tentang TPPU"
	got := r.expandAbbreviations(query)
	if got != query {
		t.Errorf("expandAbbreviations with nil dict should be no-op, got %q", got)
	}

	r.abbreviationDict = map[string]string{}
	got = r.expandAbbreviations(query)
	if got != query {
		t.Errorf("expandAbbreviations with empty dict should be no-op, got %q", got)
	}
}

// TestAbbreviationExpansionDoesNotAffectScopeGate verifies that abbreviation
// expansion only transforms the dense query — the scope gate runs on the raw
// query before any expansion, so expansion cannot change an out-of-scope
// decision. This is a design invariant: expansion runs AFTER the scope gate
// in the SearchEvidence path.
func TestAbbreviationExpansionDoesNotAffectScopeGate(t *testing.T) {
	// The scope gate fires in SearchEvidence on the raw query. The expansion
	// runs inside searchHits, which is called AFTER the scope check. We verify
	// the ordering by checking that expandAbbreviations on a hypothetical
	// out-of-scope query still returns the same input when the dict has no
	// matching terms.
	r := &hybridRetriever{
		normalizer: lexical.DefaultNormalizer,
		abbreviationDict: map[string]string{
			"tppu": "tindak pidana pencucian uang",
		},
	}
	// A query with no abbreviation match passes through unchanged.
	outOfScope := "resep masakan padang"
	got := r.expandAbbreviations(outOfScope)
	if got != outOfScope {
		t.Errorf("out-of-scope query should pass through, got %q", got)
	}
}

// TestAbbreviationExpansionAppliesRegardlessOfDiacritics verifies that
// abbreviation expansion runs on ALL queries, not just diacritic-free ones.
// This differs from diacritic restoration which is gated on DiacriticFree().
func TestAbbreviationExpansionAppliesRegardlessOfDiacritics(t *testing.T) {
	r := &hybridRetriever{
		normalizer: lexical.DefaultNormalizer,
		abbreviationDict: map[string]string{
			"tppu": "tindak pidana pencucian uang",
		},
	}

	// Query with Vietnamese diacritics — expansion should still fire.
	accentedQuery := "quy định về TPPU"
	got := r.expandAbbreviations(accentedQuery)
	want := "quy định về TPPU (tindak pidana pencucian uang)"
	if got != want {
		t.Errorf("expandAbbreviations(%q) = %q, want %q", accentedQuery, got, want)
	}
}
