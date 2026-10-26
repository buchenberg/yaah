package llm

import (
	"regexp"
	"strings"
)

var (
	// suspectFencedTranscriptRe matches a leading fenced command block
	// followed by a tool_call/tool_response wrapper — an invented tool
	// transcript presenting made-up stdout as if the command had really
	// run. The match ends at the wrapper's closing tag (or the end of
	// the message for a truncated wrapper) so any text after the
	// transcript stays available for the residual-prose allowance.
	suspectFencedTranscriptRe = regexp.MustCompile("(?s)^```[^\\n]*\\n.*?```\\s*<tool_(?:call|response)\\s*>[\\s\\S]*?(?:</tool_(?:call|response)\\s*>|\\z)")

	// fenceBlockRe matches a complete fenced code block; used to strip
	// example blocks when measuring a report's real prose.
	fenceBlockRe = regexp.MustCompile("(?s)```[^\\n]*\\n.*?```")

	// shellFenceInfoRe matches the info string of a fenced block
	// declaring a shell language.
	shellFenceInfoRe = regexp.MustCompile(`(?i)^(bash|sh|shell|zsh|console|prompt|cmd|command|powershell|ps1)$`)

	// shellOutputEvidenceRe matches lines that present text as execution
	// output: stream/exit-code markers or a shell prompt line.
	shellOutputEvidenceRe = regexp.MustCompile(`(?im)^\s*(?:(?:stdout|stderr|output|exit code|exit status)\s*[:=].*|\$\s+\S.*)$`)

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
// error instead of silently passing as the sub-agent's result. A report
// carrying substantial prose beyond the markup — including one that
// quotes a transcript and then explains it — passes.
func LooksLikeUnexecutedToolCalls(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return false
	}

	if m := suspectFencedTranscriptRe.FindStringIndex(trimmed); m != nil {
		// The transcript block itself is excluded from the allowance;
		// only the prose around it counts. A substantial explanation
		// after the closing wrapper reads as a real report quoting a
		// transcript, so it must not be rejected (#333 review).
		if len([]rune(strings.TrimSpace(trimmed[m[1]:]))) <= suspectProseLimit {
			return true
		}
	}

	stripped := trimmed
	markup := false

	if dsmlMarkupStart(stripped) >= 0 {
		markup = true
		stripped = dsmlBlockRe.ReplaceAllString(stripped, "")
		stripped = dsmlInvokeRe.ReplaceAllString(stripped, "")
		stripped = dsmlParamRe.ReplaceAllString(stripped, "")
		stripped = dsmlResidualRe.ReplaceAllString(stripped, "")
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

// hasShellFenceBlock reports whether content opens a fenced block that
// could hold a shell transcript: a shell language info string or no info
// string at all. Pair-aware scanning keeps a closing fence (or a ```go
// example) from counting as a shell opener.
func hasShellFenceBlock(content string) bool {
	inFence := false
	fence := ""
	pos := 0
	for pos < len(content) {
		lineEnd := strings.IndexByte(content[pos:], '\n')
		line := content[pos:]
		if lineEnd >= 0 {
			line = content[pos : pos+lineEnd]
		}
		trimmed := strings.TrimLeft(line, " \t")
		if inFence {
			if strings.HasPrefix(trimmed, fence) && strings.TrimSpace(trimmed[len(fence):]) == "" {
				inFence = false
			}
		} else if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			marker := "```"
			if trimmed[0] == '~' {
				marker = "~~~"
			}
			info := strings.TrimSpace(strings.TrimLeft(trimmed, "`~"))
			if info == "" || shellFenceInfoRe.MatchString(info) {
				return true
			}
			inFence = true
			fence = marker
		}
		if lineEnd < 0 {
			break
		}
		pos += lineEnd + 1
	}
	return false
}

// LooksLikeFabricatedTranscript reports whether a report is essentially
// only a fenced command block with invented output evidence — a
// "stdout:", "exit code" style marker or a prompt-led command line — and
// no real prose. It is the zero-tool sibling of
// LooksLikeUnexecutedToolCalls: a role given no tools cannot have
// executed anything, so a command transcript in its final report is
// fabricated (issue #329 — the grump role inventing a file count with
// "stdout: 842"). Callers must scope it to tool-less dispatches; a
// tool-enabled role quoting a transcript it really produced is
// legitimate and must not be rejected.
func LooksLikeFabricatedTranscript(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return false
	}
	if !hasShellFenceBlock(trimmed) || !shellOutputEvidenceRe.MatchString(trimmed) {
		return false
	}

	residual := fenceBlockRe.ReplaceAllString(trimmed, "")
	// An unterminated fence swallows the rest of the message.
	if i := strings.Index(residual, "```"); i >= 0 {
		residual = residual[:i]
	}
	residual = shellOutputEvidenceRe.ReplaceAllString(residual, "")
	return len([]rune(strings.TrimSpace(residual))) <= suspectProseLimit
}
