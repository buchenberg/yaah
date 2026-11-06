package substrate

import "unicode/utf8"

// ToolResultMaxBytes caps the success detail a single tool call returns. The
// agent loop applies a second, larger cap with spillover; this one exists so a
// tool never hands the loop more than a bounded payload.
const ToolResultMaxBytes = 8192

// TruncationMarker is the marker yaah appends when it caps a payload for
// display: capped tool output, a bounded proposal diff, or a dispatch note.
const TruncationMarker = "\n...[truncated]..."

// TruncateAt caps s at max bytes on a UTF-8 rune boundary and appends
// TruncationMarker when it cuts. max <= 0 means no cap.
//
// It lives with the workspace substrate so a dispatch note and a tool result
// are capped the same way; the callers differ, the convention does not.
func TruncateAt(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + TruncationMarker
}

// TailTruncationMarker prefixes text kept from the END of an over-long result.
const TailTruncationMarker = "...[earlier text truncated]..."

// KeepTail caps s at max bytes on a UTF-8 rune boundary, keeping the LAST
// max bytes and prefixing TailTruncationMarker when it cuts. Teardown notes
// end with the worktree path they are reporting, so a note that must fit a
// budget keeps its tail, not its head.
func KeepTail(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := len(s) - max
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return TailTruncationMarker + s[cut:]
}
