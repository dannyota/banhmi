package retrieve

import "testing"

func TestParseSearchMode(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want SearchMode
		ok   bool
	}{
		{name: "empty parse fallback", want: ModeHybrid, ok: true},
		{name: "hybrid", in: "hybrid", want: ModeHybrid, ok: true},
		{name: "bm25", in: "bm25", want: ModeBM25, ok: true},
		{name: "vector", in: "vector", want: ModeVector, ok: true},
		{name: "trims and folds", in: " BM25 ", want: ModeBM25, ok: true},
		{name: "rejects unknown", in: "sparse", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSearchMode(tt.in)
			if tt.ok && err != nil {
				t.Fatalf("ParseSearchMode(%q): %v", tt.in, err)
			}
			if !tt.ok && err == nil {
				t.Fatalf("ParseSearchMode(%q) succeeded, want error", tt.in)
			}
			if got != tt.want {
				t.Fatalf("ParseSearchMode(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestResolveDefaultModeFollowsEmbedder(t *testing.T) {
	noEmbed := &hybridRetriever{}
	got, err := noEmbed.resolve(SearchOpts{})
	if err != nil {
		t.Fatalf("resolve without embedder: %v", err)
	}
	if got.mode != ModeBM25 {
		t.Fatalf("mode without embedder = %q, want %q", got.mode, ModeBM25)
	}

	withEmbed := &hybridRetriever{embedder: &fakeEmbedder{}}
	got, err = withEmbed.resolve(SearchOpts{})
	if err != nil {
		t.Fatalf("resolve with embedder: %v", err)
	}
	if got.mode != ModeVector {
		t.Fatalf("mode with embedder = %q, want %q", got.mode, ModeVector)
	}
}
