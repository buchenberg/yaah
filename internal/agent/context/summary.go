package context

import (
	"regexp"
	"strings"
)

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
		if line == "" || isMarkdownHeading(line) {
			continue
		}
		if isPlaceholderLine(line) {
			continue
		}
		substantive++
	}
	if substantive == 0 {
		return false, "no substantive content (every section is a placeholder)"
	}
	return true, ""
}

// isMarkdownHeading reports whether line is a markdown heading ("#" runs
// followed by a space or end of line). A line like "#123 fixed the bug" is
// content, not scaffold.
func isMarkdownHeading(line string) bool {
	i := 0
	for i < len(line) && line[i] == '#' {
		i++
	}
	return i > 0 && (i == len(line) || line[i] == ' ')
}

// numberedBullet matches "1. ", "2) " and friends.
var numberedBullet = regexp.MustCompile(`^\d+[.)]\s*`)

// isPlaceholderLine reports whether a bullet carries no information:
// "(none)", "none", "n/a", a lone marker, or a bracketed template stub —
// including numbered bullets and trailing punctuation variants.
func isPlaceholderLine(line string) bool {
	text := strings.TrimSpace(line)
	text = strings.TrimPrefix(text, "- ")
	text = strings.TrimPrefix(text, "* ")
	text = strings.TrimPrefix(text, "+ ")
	text = numberedBullet.ReplaceAllString(text, "")
	text = strings.TrimSpace(text)
	text = strings.TrimRight(text, " .,;:!—-")
	switch strings.ToLower(text) {
	case "", "(none)", "none", "n/a", "na", "tbd", "todo", "...":
		return true
	}
	if strings.HasPrefix(text, "[") && strings.HasSuffix(text, "]") {
		return true
	}
	return false
}
