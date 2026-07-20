package tui

import (
	"fmt"
	"regexp"
	"strings"

	zone "github.com/lrstanley/bubblezone/v2"
)

// ToolMessage renders a tool result message: a zone-marked toggle
// header (▶/▼ icon + tool header) and, when expanded, the tool output
// inside a bordered box truncated to fit the viewport.
type ToolMessage struct {
	zoneID    string
	toolName  string
	toolArgs  string
	content   string
	width     int
	maxHeight int // viewport height, used for truncation budget
	expanded  bool
	running   bool // true when the tool is still executing
}

// NewToolMessage creates a tool message component.
func NewToolMessage(zoneID, toolName, toolArgs, content string, width, maxHeight int, expanded, running bool) ToolMessage {
	return ToolMessage{
		zoneID:    zoneID,
		toolName:  toolName,
		toolArgs:  toolArgs,
		content:   content,
		width:     width,
		maxHeight: maxHeight,
		expanded:  expanded,
		running:   running,
	}
}

// Render returns the zone-marked tool header, and when expanded,
// the bordered, truncated tool output beneath it.
func (t ToolMessage) Render() string {
	header := toolHeader(t.toolName, t.toolArgs)
	icon := "✓"
	if t.running {
		icon = "⏳"
	}

	var b strings.Builder
	if !t.expanded {
		b.WriteString(zone.Mark(t.zoneID, toolStyle.Render(fmt.Sprintf("  ▶ %s %s", icon, header))))
		b.WriteString("\n")
		return b.String()
	}

	b.WriteString(zone.Mark(t.zoneID, toolStyle.Render(fmt.Sprintf("  ▼ %s %s", icon, header))))
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
	b.WriteString(toolBoxStyle.Width(boxWidth).Render(visible))
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
