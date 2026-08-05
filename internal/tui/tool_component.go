package tui

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/buchenberg/yaah/internal/toolfmt"
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
	summary := toolfmt.Summary(t.toolName, t.toolArgs, t.content)
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
	b.WriteString(boxStyle.Width(boxWidth).Render(visible))
	b.WriteString("\n")
	return b.String()
}

// toolHeader builds the display header for a tool result message from
// the tool name and arguments.
func toolHeader(toolName, toolArgs string) string {
	header := toolName
	if toolName == "spawn_subagent" && toolArgs != "" {
		desc := toolfmt.MatchJSONField(toolArgs, "description")
		role := toolfmt.MatchJSONField(toolArgs, "role")
		header = toolfmt.SubagentLabel(role, desc)
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

// toolIndent wraps each line of content to fit within the given width.
func toolIndent(width int, content string) string {
	width = max(width, 20)

	lines := strings.Split(content, "\n")
	var result strings.Builder
	for i, line := range lines {
		if i > 0 {
			result.WriteString("\n")
		}
		result.WriteString(wrapText(line, width))
	}
	return result.String()
}

// --- shared helpers in internal/toolfmt ---
