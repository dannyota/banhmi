package chunking_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"danny.vn/banhmi/internal/corpus/chunking"
)

func TestRoughTokenCount(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"a", 1},
		{"abc", 1},
		{"Điều 1", 3},
		{"กขคง", 2},
		{"កខគឃ", 2},
		{"😀😀😀", 1},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := chunking.RoughTokenCount(tt.input); got != tt.want {
				t.Fatalf("RoughTokenCount(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestSplitLongContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
		cap     int
		want    []string
	}{
		{"empty", "", 2, nil},
		{"whitespace", " \t\n ", 2, nil},
		{"zero cap", "  a\n\n b  ", 0, []string{"a\n\n b"}},
		{"negative cap", "  a\n\n b  ", -1, []string{"a\n\n b"}},
		{"short content", "  a\n\n b  ", 100, []string{"a\n\n b"}},
		{"exact cap", "abcd", 2, []string{"abcd"}},
		{"short whitespace preserved", "  aa\tbb  cc  ", 100, []string{"aa\tbb  cc"}},
		{"line packing", " aa \n\n bb \n cc ", 3, []string{"aa\nbb", "cc"}},
		{"long line flushes pending part", "aa\nbbbbbb\ncc", 2, []string{"aa", "bbbbbb", "cc"}},
		{"word packing", "aa\tbb  cc", 2, []string{"aa bb", "cc"}},
		{"oversized word", "abcdef", 1, []string{"abcdef"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := chunking.SplitLongContent(tt.content, tt.cap); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("SplitLongContent(%q, %d) = %#v, want %#v", tt.content, tt.cap, got, tt.want)
			}
		})
	}
}

func TestSplitLongContentUnicode(t *testing.T) {
	for _, word := range []string{"Ngân", "กขคง", "កខគឃ", "😀😀😀"} {
		t.Run(word, func(t *testing.T) {
			const maxTokens = 8
			content := strings.Repeat(word+" ", 30)
			parts := chunking.SplitLongContent(content, maxTokens)
			if len(parts) < 2 {
				t.Fatalf("parts = %d, want split content", len(parts))
			}
			for i, part := range parts {
				if strings.TrimSpace(part) == "" {
					t.Fatalf("part %d is empty", i)
				}
				if got := chunking.RoughTokenCount(part); got > maxTokens {
					t.Fatalf("part %d tokens = %d, want <= %d", i, got, maxTokens)
				}
			}
			if got := strings.Fields(strings.Join(parts, " ")); !slices.Equal(got, strings.Fields(content)) {
				t.Fatalf("split changed ordered words: %q", parts)
			}
			if again := chunking.SplitLongContent(content, maxTokens); !slices.Equal(parts, again) {
				t.Fatalf("split is not deterministic: %q then %q", parts, again)
			}
		})
	}
}
