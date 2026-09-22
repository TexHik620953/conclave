// Package memory provides simple context-budget management for prompts.
package memory

import (
	"strings"
	"unicode/utf8"
)

// TrimContext limits a context block to maxBytes. When over budget it keeps the
// head (task/inputs) and the tail (most recent outputs), inserting a marker.
// Cuts are adjusted to UTF-8 rune boundaries so multi-byte characters are never
// split.
func TrimContext(block string, maxBytes int) string {
	if maxBytes <= 0 || len(block) <= maxBytes {
		return block
	}
	head := maxBytes * 2 / 3
	tail := maxBytes - head
	marker := "\n\n... [context truncated to fit budget] ...\n\n"
	if head+tail >= len(block) {
		return block
	}
	head = runeFloor(block, head)
	tailStart := runeCeil(block, len(block)-tail)
	return block[:head] + marker + block[tailStart:]
}

// runeFloor returns the largest index <= i that starts a rune.
func runeFloor(s string, i int) int {
	if i >= len(s) {
		return len(s)
	}
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}

// runeCeil returns the smallest index >= i that starts a rune.
func runeCeil(s string, i int) int {
	if i <= 0 {
		return 0
	}
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return i
}

// Summarize condenses long text by keeping the first and last paragraphs.
func Summarize(text string, maxBytes int) string {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text
	}
	paras := strings.Split(text, "\n\n")
	if len(paras) <= 2 {
		return TrimContext(text, maxBytes)
	}
	half := maxBytes / 2
	return TrimContext(paras[0], half) + "\n\n...\n\n" + TrimContext(paras[len(paras)-1], half)
}
