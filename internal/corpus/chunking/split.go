package chunking

import (
	"strings"
	"unicode/utf8"
)

// RoughTokenCount estimates tokens as half the rune count, with a minimum of one
// for nonempty input. It is an approximation, not a tokenizer.
func RoughTokenCount(s string) int {
	n := utf8.RuneCountInString(s)
	if n == 0 {
		return 0
	}
	est := n / 2
	if est == 0 {
		est = 1
	}
	return est
}

// SplitLongContent trims content and packs lines or whole words under the rough
// token cap. A nonpositive cap leaves the trimmed content intact. A word larger
// than the cap remains intact and may exceed it.
func SplitLongContent(content string, maxTokens int) []string {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	if maxTokens <= 0 || RoughTokenCount(content) <= maxTokens {
		return []string{content}
	}

	var parts []string
	current := ""
	flush := func() {
		current = strings.TrimSpace(current)
		if current != "" {
			parts = append(parts, current)
			current = ""
		}
	}

	for _, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if RoughTokenCount(line) > maxTokens {
			flush()
			parts = append(parts, splitLongTextByWords(line, maxTokens)...)
			continue
		}
		next := line
		if current != "" {
			next = current + "\n" + line
		}
		if current != "" && RoughTokenCount(next) > maxTokens {
			flush()
			current = line
			continue
		}
		current = next
	}
	flush()
	return parts
}

func splitLongTextByWords(text string, maxTokens int) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}
	var parts []string
	current := ""
	for _, word := range words {
		next := word
		if current != "" {
			next = current + " " + word
		}
		if current != "" && RoughTokenCount(next) > maxTokens {
			parts = append(parts, current)
			current = word
			continue
		}
		current = next
	}
	if current != "" {
		parts = append(parts, current)
	}
	return parts
}
