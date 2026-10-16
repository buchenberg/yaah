package llm

import (
	"regexp"
	"strings"
)

var (
	// suspectFencedTranscriptRe matches a message that is entirely a
	// fenced command block followed by a response wrapper — an invented
	// tool transcript presenting made-up stdout as if the command had
	// really run.
	suspectFencedTranscriptRe = regexp.MustCompile("(?s)^\\s*```[^\\n]*\\n.*?```\\s*<tool_response>[\\s\\S]*\\z")

	// suspectProseLimit is the number of non-markup runes a suspect
	// report may carry before it reads as legitimate. Detection is
	// deliberately conservative: substantive prose discussing tool-call
	// markup (a bug report, a docs excerpt) always exceeds this budget,
	// while markup-only output leaves at most stray punctuation behind.
	suspectProseLimit = 40
)

// LooksLikeUnexecutedToolCalls reports whether a final assistant report
// consists essentially only of tool-call markup that was never executed:
// DSML blocks, the attribute-style dialect (issue #314), or a fenced
// command block presented as an invented tool transcript. The sub-agent
// runner consults it so a markup-only report surfaces as an explicit
// error instead of silently passing as the sub-agent's result.
func LooksLikeUnexecutedToolCalls(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return false
	}

	if suspectFencedTranscriptRe.MatchString(trimmed) {
		return true
	}

	stripped := trimmed
	markup := false

	if dsmlBlockRe.MatchString(stripped) || dsmlOpenRe.MatchString(stripped) {
		markup = true
		stripped = dsmlBlockRe.ReplaceAllString(stripped, "")
		stripped = dsmlInvokeRe.ReplaceAllString(stripped, "")
		stripped = dsmlParamRe.ReplaceAllString(stripped, "")
		stripped = dsmlOpenRe.ReplaceAllString(stripped, "")
		stripped = strings.ReplaceAll(stripped, dsmlCloseTag, "")
	}

	if attrFuncOpenRe.MatchString(stripped) || attrWrapperRe.MatchString(stripped) {
		markup = true
		stripped = attrFuncBlockRe.ReplaceAllString(stripped, "")
		stripped = attrParamRe.ReplaceAllString(stripped, "")
		stripped = attrFuncOpenRe.ReplaceAllString(stripped, "")
		stripped = attrParamOpenRe.ReplaceAllString(stripped, "")
		stripped = attrWrapperRe.ReplaceAllString(stripped, "")
	}

	if !markup {
		return false
	}
	return len([]rune(strings.TrimSpace(stripped))) <= suspectProseLimit
}
