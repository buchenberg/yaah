package acp

import (
	"encoding/json"
	"fmt"
	"strings"
)

// text formats a string with fmt.Sprintf.
func text(format string, args ...any) string { return fmt.Sprintf(format, args...) }

// textContent builds a text content block for chunk updates.
func textContent(s string) ContentBlock {
	return ContentBlock{Type: "text", Text: s}
}

// rawOf marshals v to JSON when it is not already raw bytes.
func rawOf(v any) json.RawMessage {
	if r, ok := v.(json.RawMessage); ok {
		return r
	}
	data, _ := json.Marshal(v)
	return data
}

// truncate shortens s to at most max runes. A cut at max >= 4 is
// marked with an ellipsis; below that, a bare prefix is returned
// (and max <= 0 yields ""). Rune-safe: byte slicing would corrupt
// multi-byte characters.
func truncate(s string, max int) string {
	if max < 0 {
		max = 0
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 3 {
		return string(runes[:max])
	}
	return string(runes[:max-3]) + "..."
}

// compactHint condenses the abbreviated tool args into a single-line
// title hint.
func compactHint(args string) string {
	return truncate(strings.Join(strings.Fields(args), " "), 50)
}
