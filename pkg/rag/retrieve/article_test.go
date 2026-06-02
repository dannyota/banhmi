package retrieve

import (
	"strings"
	"testing"
)

func TestAssembleArticle(t *testing.T) {
	t.Run("single chunk returned verbatim", func(t *testing.T) {
		got := assembleArticle([]string{"Điều 5. Định nghĩa\nNội dung."})
		if got != "Điều 5. Định nghĩa\nNội dung." {
			t.Errorf("got %q", got)
		}
	})

	t.Run("split-by-Khoản chunks: shared Điều lead emitted once", func(t *testing.T) {
		// The indexer prepends the Điều lead-in to every Khoản chunk.
		chunks := []string{
			"Điều 7. An toàn hệ thống\n1. Khoản một.",
			"Điều 7. An toàn hệ thống\n2. Khoản hai.",
			"Điều 7. An toàn hệ thống\n3. Khoản ba.",
		}
		got := assembleArticle(chunks)
		want := "Điều 7. An toàn hệ thống\n1. Khoản một.\n2. Khoản hai.\n3. Khoản ba."
		if got != want {
			t.Errorf("assembled =\n%q\nwant\n%q", got, want)
		}
		if strings.Count(got, "An toàn hệ thống") != 1 {
			t.Errorf("Điều lead-in repeated; got:\n%s", got)
		}
	})

	t.Run("Đoạn continuation shard (no shared lead) keeps dedup working", func(t *testing.T) {
		// A long Khoản is split into Đoạn shards; only the first carries the Điều lead.
		// Global common-prefix would strip nothing here — per-first-chunk prefix must not.
		chunks := []string{
			"Điều 7. An toàn hệ thống\n1. Phần đầu của khoản dài.",
			"phần tiếp theo của khoản dài, không có lead.",
			"Điều 7. An toàn hệ thống\n2. Khoản hai.",
		}
		got := assembleArticle(chunks)
		want := "Điều 7. An toàn hệ thống\n1. Phần đầu của khoản dài.\nphần tiếp theo của khoản dài, không có lead.\n2. Khoản hai."
		if got != want {
			t.Errorf("assembled =\n%q\nwant\n%q", got, want)
		}
		if strings.Count(got, "An toàn hệ thống") != 1 {
			t.Errorf("Điều lead-in repeated despite Đoạn shard; got:\n%s", got)
		}
	})

	t.Run("empty input", func(t *testing.T) {
		if got := assembleArticle(nil); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
}

func TestArticleProvision(t *testing.T) {
	t.Run("fits cap → attach full text", func(t *testing.T) {
		a := articleProvision("Điều 7. Heading\n1. text", 14000)
		if !a.attach || a.truncated || a.text != "Điều 7. Heading\n1. text" {
			t.Errorf("got %+v, want full attach", a)
		}
	})

	t.Run("over cap → pointer, no text", func(t *testing.T) {
		long := strings.Repeat("ạ", 50)
		a := articleProvision(long, 10)
		if !a.attach || !a.truncated || a.text != "" {
			t.Errorf("got %+v, want truncated pointer with empty text", a)
		}
	})

	t.Run("empty → no attach", func(t *testing.T) {
		if a := articleProvision("   ", 14000); a.attach {
			t.Errorf("got %+v, want no attach", a)
		}
	})
}

func TestSharedPrefixLen(t *testing.T) {
	tests := []struct {
		name string
		a, b []string
		want int
	}{
		{"share two lines", []string{"a", "b", "c"}, []string{"a", "b", "d"}, 2},
		{"no shared lead", []string{"x", "y"}, []string{"p", "q"}, 0},
		{"b is longer", []string{"a", "b"}, []string{"a", "b", "c"}, 2},
		{"empty b", []string{"a"}, nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sharedPrefixLen(tt.a, tt.b); got != tt.want {
				t.Errorf("sharedPrefixLen = %d, want %d", got, tt.want)
			}
		})
	}
}
