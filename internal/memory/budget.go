// Package memory provides simple context-budget management for prompts.
package memory

import "strings"

// TrimContext limits a context block to maxBytes. When over budget it keeps the
// head (task/inputs) and the tail (most recent outputs), inserting a marker.
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
	return block[:head] + marker + block[len(block)-tail:]
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
