// Package toolfmt provides shared tool result formatting helpers used
// by both the TUI and web views.  These were previously duplicated between
// internal/tui/tool_component.go and cmd/yaah/web_view.go.
package toolfmt

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/buchenberg/yaah/internal/agent/subagent"
)

var grepMatchRe = regexp.MustCompile(`^\d+:`)

// Summary returns a one-line description of a tool's result for
// collapsed display, following the hermes-agent pattern.
func Summary(toolName, toolArgs, content string) string {
	switch toolName {
	case "grep":
		return GrepSummary(content)
	case "glob":
		return GlobSummary(content)
	case "ls":
		return LsSummary(content)
	case "bash":
		return BashSummary(content)
	case "read":
		return fmt.Sprintf("read %s (%s chars)", FilePath(toolArgs), Num(len(content)))
	case "write":
		return fmt.Sprintf("wrote %s (%s chars)", FilePath(toolArgs), Num(len(content)))
	case "edit":
		return "edited " + FilePath(toolArgs)
	case "delete":
		return "deleted " + FilePath(toolArgs)
	case "http":
		if u := MatchJSONField(toolArgs, "url"); u != "" {
			return u
		}
		return ""
	case "webfetch":
		if u := MatchJSONField(toolArgs, "url"); u != "" {
			return u
		}
		if u := MatchJSONField(toolArgs, "urls"); u != "" {
			return u
		}
		return ""
	case "git":
		if a := MatchJSONField(toolArgs, "action"); a != "" {
			return a
		}
		return ""
	case "replace":
		return "replaced in " + FilePath(toolArgs)
	case "spawn_subagent":
		role := MatchJSONField(toolArgs, "role")
		desc := MatchJSONField(toolArgs, "description")
		return SubagentLabel(role, desc)
	default:
		firstLine, _, _ := strings.Cut(strings.TrimSpace(content), "\n")
		if len(firstLine) > 80 {
			return firstLine[:77] + "..."
		}
		return firstLine
	}
}

// GrepSummary summarizes ripgrep output — match count and file count.
func GrepSummary(content string) string {
	if content == "" {
		return "0 matches"
	}
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	totalLines := len(lines)
	matches := 0
	matchLines := 0
	for _, line := range lines {
		if grepMatchRe.MatchString(line) {
			matchLines++
			matches += strings.Count(line, "\x1b[31m") // ANSI red highlight marker
		}
	}
	if matches == 0 {
		matches = matchLines
	}
	return fmt.Sprintf("%d matches in %d files", matches, totalLines-matchLines+1)
}

// GlobSummary summarizes glob output — file count.
func GlobSummary(content string) string {
	lines := strings.Count(strings.TrimRight(content, "\n"), "\n") + 1
	if content == "" {
		lines = 0
	}
	if lines == 0 {
		return "0 files"
	}
	return fmt.Sprintf("%d files", lines)
}

// LsSummary summarizes ls output — entry count.
func LsSummary(content string) string {
	lines := strings.Count(content, "\n")
	if content == "" {
		lines = -1
	}
	if lines < 0 {
		return "0 entries"
	}
	return fmt.Sprintf("%d entries", lines+1)
}

// BashSummary summarizes shell command output — first line, truncated.
func BashSummary(content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ""
	}
	firstLine, _, _ := strings.Cut(trimmed, "\n")
	if len(firstLine) > 60 {
		return firstLine[:57] + "..."
	}
	return firstLine
}

// FilePath extracts a filename from tool arguments by searching for
// "filePath" or "path" fields in the JSON.
func FilePath(args string) string {
	re := regexp.MustCompile(`"filePath"\s*:\s*"([^"]*)"`)
	if match := re.FindStringSubmatch(args); len(match) > 1 && match[1] != "" {
		parts := strings.Split(match[1], "/")
		return parts[len(parts)-1]
	}
	re = regexp.MustCompile(`"path"\s*:\s*"([^"]*)"`)
	if match := re.FindStringSubmatch(args); len(match) > 1 && match[1] != "" {
		parts := strings.Split(match[1], "/")
		return parts[len(parts)-1]
	}
	return ""
}

// Num formats an integer with comma separators (e.g. 12345 → "12,345").
func Num(n int) string {
	s := strconv.Itoa(n)
	var result strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			result.WriteRune(',')
		}
		result.WriteRune(c)
	}
	return result.String()
}

// MatchJSONField extracts a string field value from JSON-like text.
// The field name is escaped via regexp.QuoteMeta, so it is safe against
// metacharacters. Returns "" if the field is absent or unparseable.
func MatchJSONField(jsonStr, field string) string {
	re := regexp.MustCompile(`"` + regexp.QuoteMeta(field) + `"\s*:\s*"([^"]*)"`)
	if match := re.FindStringSubmatch(jsonStr); len(match) > 1 {
		return match[1]
	}
	return ""
}

// SubagentLabel builds a display label for a sub-agent from its role
// and description fields.
func SubagentLabel(role, desc string) string {
	displayName := subagent.RoleDisplayName(subagent.SubAgentRole(role))
	specialty := subagent.RoleSpecialty(subagent.SubAgentRole(role))
	label := displayName
	if specialty != "" {
		label += " — " + specialty
	}
	switch {
	case role != "" && desc != "":
		return "sub-agent: " + label + " · " + desc
	case desc != "":
		return "sub-agent · " + desc
	case role != "":
		return "sub-agent: " + label
	default:
		return "sub-agent"
	}
}
