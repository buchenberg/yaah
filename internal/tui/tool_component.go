package tui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	zone "github.com/lrstanley/bubblezone/v2"
)

// ToolMessage renders a tool result message: a zone-marked toggle
// header (▶/▼ icon + tool header + duration) and, when expanded, the
// tool output inside a bordered box truncated to fit the viewport.
type ToolMessage struct {
	zoneID    string
	toolName  string
	toolArgs  string
	content   string
	width     int
	maxHeight int // viewport height, used for truncation budget
	expanded  bool
	running   bool   // true when the tool is still executing
	duration  string // formatted duration string (e.g. "2.3s")
}

// NewToolMessage creates a tool message component.
func NewToolMessage(zoneID, toolName, toolArgs, content string, width, maxHeight int, expanded, running bool, duration string) ToolMessage {
	return ToolMessage{
		zoneID:    zoneID,
		toolName:  toolName,
		toolArgs:  toolArgs,
		content:   content,
		width:     width,
		maxHeight: maxHeight,
		expanded:  expanded,
		running:   running,
		duration:  duration,
	}
}

// Render returns the zone-marked tool header, and when expanded,
// the bordered, truncated tool output beneath it.
func (t ToolMessage) Render() string {
	icon := "✓"
	if t.running {
		icon = "⏳"
	}

	header := toolHeader(t.toolName, t.toolArgs)
	summary := toolSummary(t.toolName, t.toolArgs, t.content)
	dur := ""
	if t.duration != "" {
		dur = " (" + t.duration + ")"
	}

	var b strings.Builder
	if !t.expanded {
		line := fmt.Sprintf("  ▶ %s %s%s", icon, header, dur)
		if summary != "" {
			line += fmt.Sprintf(" — %s", summary)
		}
		b.WriteString(zone.Mark(t.zoneID, toolStyle.Render(line)))
		b.WriteString("\n")
		return b.String()
	}

	line := fmt.Sprintf("  ▼ %s %s%s", icon, header, dur)
	if summary != "" {
		line += fmt.Sprintf(" — %s", summary)
	}
	b.WriteString(zone.Mark(t.zoneID, toolStyle.Render(line)))
	b.WriteString("\n")

	boxWidth := t.width - 4
	if boxWidth < 20 {
		boxWidth = 20
	}
	innerWidth := boxWidth - 4
	if innerWidth < 10 {
		innerWidth = 10
	}
	indented := toolIndent(innerWidth, t.content)

	maxLines := t.maxHeight/3 - 4
	if maxLines < 4 {
		maxLines = 4
	}
	if maxLines > 24 {
		maxLines = 24
	}
	indentedLines := strings.Split(indented, "\n")
	totalLines := len(indentedLines)
	var visible string
	if totalLines > maxLines {
		tail := indentedLines[totalLines-maxLines:]
		omitted := totalLines - maxLines
		headerLine := toolStyle.Render(fmt.Sprintf("  ··· %d more lines above ···", omitted))
		visible = headerLine + "\n" + strings.Join(tail, "\n")
	} else {
		visible = indented
	}
	boxStyle := toolBoxStyle
	if t.toolName == "delegate" {
		boxStyle = executorBoxStyle
	}
	b.WriteString(boxStyle.Width(boxWidth).Render(visible))
	b.WriteString("\n")
	return b.String()
}

// toolHeader builds the display header for a tool result message from
// the tool name and arguments.
func toolHeader(toolName, toolArgs string) string {
	header := toolName
	if toolName == "task" && toolArgs != "" {
		desc := matchJSONField(toolArgs, "description")
		role := matchJSONField(toolArgs, "role")
		switch {
		case role != "" && desc != "":
			header = "sub-agent: " + role + " — " + desc
		case desc != "":
			header = "sub-agent — " + desc
		case role != "":
			header = "sub-agent: " + role
		default:
			header = "sub-agent"
		}
	} else if toolName == "delegate" && toolArgs != "" {
		desc := matchJSONField(toolArgs, "task")
		if desc != "" {
			header = "executor — " + desc
		} else {
			header = "executor"
		}
	} else if toolName == "webfetch" && toolArgs != "" {
		re := regexp.MustCompile(`"url"\s*:\s*"([^"]*)"`)
		if match := re.FindStringSubmatch(toolArgs); len(match) > 1 && match[1] != "" {
			header = "web_fetch → " + match[1]
		}
	} else if toolName == "bash" && toolArgs != "" {
		header = "bash — " + toolArgs
	}
	return header
}

// toolSummary generates a one-line description of the tool result for
// collapsed display, following the hermes-agent pattern.
func toolSummary(toolName, toolArgs, content string) string {
	switch toolName {
	case "grep":
		return grepSummary(content)
	case "glob":
		lines := strings.Count(strings.TrimRight(content, "\n"), "\n") + 1
		if content == "" {
			lines = 0
		}
		if lines == 0 {
			return "0 files"
		}
		return fmt.Sprintf("%d files", lines)
	case "ls":
		lines := strings.Count(content, "\n")
		if content == "" {
			lines = -1
		}
		if lines < 0 {
			return "0 entries"
		}
		return fmt.Sprintf("%d entries", lines+1)
	case "bash":
		return bashSummary(content)
	case "read":
		return fmt.Sprintf("read %s (%s chars)", extractFilePath(toolArgs), formatNumber(len(content)))
	case "write":
		return fmt.Sprintf("wrote %s (%s chars)", extractFilePath(toolArgs), formatNumber(len(content)))
	case "edit":
		return "edited " + extractFilePath(toolArgs)
	case "delete":
		return "deleted " + extractFilePath(toolArgs)
	case "http":
		re := regexp.MustCompile(`"url"\s*:\s*"([^"]*)"`)
		if match := re.FindStringSubmatch(toolArgs); len(match) > 1 {
			return match[1]
		}
		return ""
	case "webfetch":
		re := regexp.MustCompile(`"url"\s*:\s*"([^"]*)"`)
		if match := re.FindStringSubmatch(toolArgs); len(match) > 1 {
			return match[1]
		}
		re = regexp.MustCompile(`"urls"\s*:\s*\["([^"]*)"`)
		if match := re.FindStringSubmatch(toolArgs); len(match) > 1 {
			return match[1]
		}
		return ""
	case "git":
		re := regexp.MustCompile(`"action"\s*:\s*"([^"]*)"`)
		if match := re.FindStringSubmatch(toolArgs); len(match) > 1 {
			return match[1]
		}
		return ""
	case "replace":
		return "replaced in " + extractFilePath(toolArgs)
	case "delegate":
		return delegateSummary(content)
	default:
		firstLine, _, _ := strings.Cut(strings.TrimSpace(content), "\n")
		if len(firstLine) > 80 {
			return firstLine[:77] + "..."
		}
		return firstLine
	}
}

var grepMatchLine = regexp.MustCompile(`^(\d+):`)

func grepSummary(content string) string {
	if content == "" {
		return "0 matches"
	}
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	totalLines := len(lines)
	matches := 0
	matchLines := 0
	for _, line := range lines {
		if grepMatchLine.MatchString(line) {
			matchLines++
			matches += strings.Count(line, "\x1b[31m") // ANSI red highlight marker
		}
	}
	if matches == 0 {
		matches = matchLines
	}
	return fmt.Sprintf("%d matches in %d files", matches, totalLines-matchLines+1)
}

func bashSummary(content string) string {
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

func extractFilePath(args string) string {
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

func formatNumber(n int) string {
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

var delegateResultRE = regexp.MustCompile(`<executor_result\s+state="([^"]*)"(?:\s+truncated="([^"]*)")?>(.*)</executor_result>`)

// delegateSummary parses the structured executor result envelope and returns
// a one-line summary showing state and the first line of the executor's prose.
func delegateSummary(content string) string {
	m := delegateResultRE.FindStringSubmatch(content)
	if m == nil {
		return content
	}
	state := m[1]
	truncated := m[2] == "true"
	inner := strings.TrimSpace(m[3])
	firstLine, _, _ := strings.Cut(inner, "\n")
	if len(firstLine) > 60 {
		firstLine = firstLine[:57] + "..."
	}
	suffix := ""
	if truncated {
		suffix = " (truncated)"
	}
	if state == "error" {
		return "error — " + firstLine
	}
	if state == "exhausted" {
		return "exhausted — " + firstLine
	}
	return firstLine + suffix
}

// stripExecutorEnvelope removes the <executor_result> XML wrapper and
// returns the inner content, or the original string if no envelope found.
func stripExecutorEnvelope(content string) string {
	m := delegateResultRE.FindStringSubmatch(content)
	if m == nil {
		return content
	}
	return strings.TrimSpace(m[3])
}
