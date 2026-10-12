package db

import "testing"

func TestParseMaxConns(t *testing.T) {
	const fallback int32 = 17
	tests := []struct {
		name  string
		value string
		want  int32
	}{
		{"unset", "", fallback},
		{"minimum positive", "1", 1},
		{"ordinary positive", "32", 32},
		{"explicit positive sign", "+32", 32},
		{"leading zeros", "0032", 32},
		{"maximum int32", "2147483647", 2147483647},
		{"maximum int32 plus one", "2147483648", fallback},
		{"would wrap to positive int32", "4294967297", fallback},
		{"larger than int64", "9223372036854775808", fallback},
		{"negative", "-1", fallback},
		{"minimum int32", "-2147483648", fallback},
		{"below minimum int32", "-2147483649", fallback},
		{"zero", "0", fallback},
		{"malformed", "many", fallback},
		{"fraction", "1.5", fallback},
		{"whitespace", " 32 ", fallback},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseMaxConns(tt.value, fallback); got != tt.want {
				t.Fatalf("parseMaxConns(%q, %d) = %d, want %d", tt.value, fallback, got, tt.want)
			}
		})
	}
}
