package context

import (
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/types"
)

// PruneMessages replaces large tool and assistant messages with abbreviated
// markers to reduce token load before LLM summarization. Tool outputs become
// compact summary markers; assistant messages are truncated with rune-safe
// head+tail preservation.
func PruneMessages(msgs []types.Message, maxLen int) []types.Message {
	out := make([]types.Message, len(msgs))
	for i, m := range msgs {
		out[i] = m
		if len(m.Content) <= maxLen {
			continue
		}
		switch m.Role {
		case "tool":
			lines := strings.Count(m.Content, "\n") + 1
			chars := len(m.Content)
			out[i].Content = fmt.Sprintf("[tool %s output — %d lines, %d chars]",
				m.Name, lines, chars)
		case "assistant":
			if len(m.ToolCalls) > 0 {
				continue
			}
			out[i].Content = TruncateRunes(m.Content, maxLen)
		}
	}
	return out
}

// FormatToolStub produces a compact, structured summary of a tool result
// message for the compaction serializer. Instead of embedding the full output
// (which can be thousands of lines of grep/cat/ls results), it emits a stub
// with line count and the first meaningful snippet.
func FormatToolStub(m types.Message) string {
	content := strings.TrimSpace(m.Content)
	if content == "" {
		return fmt.Sprintf("[tool:%s (empty output)]", m.Name)
	}
	lines := strings.Count(content, "\n") + 1
	chars := len(content)
	firstLine := content
	if idx := strings.IndexByte(content, '\n'); idx > 0 {
		firstLine = content[:idx]
	}
	if r := []rune(firstLine); len(r) > 120 {
		firstLine = string(r[:120]) + "..."
	}
	return fmt.Sprintf("[tool:%s — %d lines, %d chars, starts: %q]", m.Name, lines, chars, firstLine)
}
