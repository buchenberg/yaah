package acp

import (
	"encoding/json"
	"fmt"
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
