package context

import "strings"

// summaryPrefixBytes bounds the summary prefix recorded on the compaction
// span: enough to recognize what the summarizer said, small enough to stay
// an attribute.
const summaryPrefixBytes = 256

// BoundedSummaryPrefix returns the first summaryPrefixBytes bytes of the
// summary on a UTF-8 rune boundary, for observability attributes.
func BoundedSummaryPrefix(summary string) string {
	if len(summary) <= summaryPrefixBytes {
		return summary
	}
	cut := summaryPrefixBytes
	for cut > 0 && !isRuneStart(summary[cut]) {
		cut--
	}
	return summary[:cut] + "..."
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// ValidateSummary reports whether a summarizer response carries usable
// information, and the reason it fails when it does not.
//
// A summary that is empty, or whose every bullet is a placeholder —
// "(none)", "none", "n/a", or a bracketed stub like "[task summary]" —
// passes the call site's non-empty check while carrying zero information.
// Installing it replaces the conversation with nothing: the recorded
// 2026-10-26 incident had the summarizer echo the template verbatim and
// the session lost hours of state (yaah-v378.1).
func ValidateSummary(summary string) (bool, string) {
	trimmed := strings.TrimSpace(summary)
	if trimmed == "" {
		return false, "empty"
	}
	substantive := 0
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		// Blank lines and markdown scaffold headings carry nothing; a
		// real summary may be prose (no bullets), so every other line
		// counts unless it is a placeholder.
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		text := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "- "), "* "))
		switch strings.ToLower(text) {
		case "", "(none)", "none", "n/a":
			continue
		}
		if strings.HasPrefix(text, "[") && strings.HasSuffix(text, "]") {
			continue
		}
		substantive++
	}
	if substantive == 0 {
		return false, "no substantive content (every section is a placeholder)"
	}
	return true, ""
}
