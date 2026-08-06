package tui

import (
	"fmt"
	"strings"

	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/list"
	"charm.land/lipgloss/v2/table"
	"charm.land/lipgloss/v2/tree"

	zone "github.com/lrstanley/bubblezone/v2"
)

// createRenderer (re)creates the glamour markdown renderer.
func (m *Model) createRenderer() {
	width := m.width - 2
	if width < 20 {
		width = 20
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(width),
		glamour.WithEmoji(),
		glamour.WithChromaFormatter("terminal256"),
		glamour.WithPreservedNewLines(),
	)
	if err == nil {
		m.mdRenderer = r
	}
}

// glamourRender renders markdown through the reusable renderer.
func (m *Model) glamourRender(content string) string {
	if m.mdRenderer == nil {
		return content
	}
	content = injectHyperlinks(content)
	out, err := m.mdRenderer.Render(content)
	if err != nil {
		return content
	}
	return strings.TrimSpace(out)
}

// renderMarkdown renders raw markdown through glamour. Tables are extracted
// and rendered as plain compact text before passing the rest to glamour.
func (m *Model) renderMarkdown(content string) string {
	segments := parseAndRenderTables(content)
	var result strings.Builder
	for i, seg := range segments {
		if seg.isTable {
			if i > 0 {
				result.WriteString("\n\n")
			}
			result.WriteString(m.renderCompactTable(seg.content))
			result.WriteString("\n")
		} else if seg.content != "" {
			result.WriteString(m.glamourRender(seg.content))
		}
	}
	return strings.TrimSpace(result.String())
}

// parseAndRenderTables splits markdown into table and non-table segments.
// A table is: one or more lines starting with "|", where the first or second
// line contains "---" (the separator row).
func parseAndRenderTables(md string) []textSegment {
	lines := strings.Split(md, "\n")
	var segments []textSegment

	i := 0
	for i < len(lines) {
		line := lines[i]

		if strings.HasPrefix(line, "|") && i+1 < len(lines) {
			next := lines[i+1]
			if strings.HasPrefix(next, "|") && strings.Contains(next, "---") {
				var buf strings.Builder

				for i < len(lines) && strings.HasPrefix(lines[i], "|") {
					buf.WriteString(lines[i])
					buf.WriteString("\n")
					i++

					if i-1 >= 0 && strings.Contains(lines[i-1], "---") {
						for i < len(lines) && strings.HasPrefix(lines[i], "|") {
							buf.WriteString(lines[i])
							buf.WriteString("\n")
							i++
						}
						break
					}
				}

				segments = append(segments, textSegment{content: buf.String(), isTable: true})
				continue
			}
		}

		// Non-table line: accumulate into a text segment
		var buf strings.Builder
		for i < len(lines) {
			line := lines[i]

			if strings.HasPrefix(line, "|") && i+1 < len(lines) {
				next := lines[i+1]
				if strings.HasPrefix(next, "|") && strings.Contains(next, "---") {
					break
				}
			}
			buf.WriteString(line)
			buf.WriteString("\n")
			i++
		}
		s := strings.TrimSpace(buf.String())
		if s != "" {
			segments = append(segments, textSegment{content: s, isTable: false})
		}
	}
	return segments
}

// renderCompactTable renders a markdown table using lipgloss's table package.
// It constrains the table to the TUI width and wraps long cell content.
func (m *Model) renderCompactTable(md string) string {
	lines := strings.Split(strings.TrimSpace(md), "\n")
	if len(lines) < 2 {
		return md
	}

	var headers []string
	var data [][]string
	var pastHeader bool

	for _, line := range lines {
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cols := splitRow(line)
		if strings.Contains(line, "---") {
			pastHeader = true
			continue
		}
		if !pastHeader {
			headers = cols
			pastHeader = true
		} else {
			styled := make([]string, len(cols))
			for i, c := range cols {
				styled[i] = renderInlineMarkdown(c)
			}
			data = append(data, styled)
		}
	}

	if len(headers) == 0 {
		return md
	}

	styledHeaders := make([]string, len(headers))
	for i, h := range headers {
		styledHeaders[i] = renderInlineMarkdown(h)
	}

	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(assistantStyle.GetForeground())
	cellStyle := lipgloss.NewStyle().Foreground(assistantStyle.GetForeground())

	t := table.New().
		Width(m.width - 2).
		Wrap(true).
		Border(lipgloss.NormalBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(toggleStyle.GetForeground())).
		StyleFunc(func(row, col int) lipgloss.Style {
			if row == table.HeaderRow {
				return headerStyle
			}
			return cellStyle
		}).
		Headers(styledHeaders...).
		Rows(data...)

	return t.String()
}

// renderInlineMarkdown renders basic inline markdown in a table cell:
// backtick code spans, bold, and italic.
func renderInlineMarkdown(s string) string {
	code := func(t string) string { return codeStyle.Render(t) }
	bold := func(t string) string { return boldStyle.Render(t) }
	italic := func(t string) string { return italicStyle.Render(t) }
	s = replacePattern(s, "`", "`", code)
	s = replacePattern(s, "**", "**", bold)
	s = replacePattern(s, "*", "*", italic)
	return s
}

// renderToolResult renders tool result content, detecting lists and trees.
func (m *Model) renderToolResult(toolName, content string) string {
	if content == "" {
		return ""
	}
	if toolName == "todowrite" {
		// Compact one-line summary
		return toolStyle.Render("✓ Todo list updated")
	}
	if toolName == "spawn_subagent" {
		return m.renderMarkdown(content)
	}
	if isTreeContent(content) {
		return m.renderTree(content)
	}
	if isListContent(content) {
		return m.renderList(content)
	}
	return toolStyle.Render(content)
}

// renderList renders bullet-list content using lipgloss's list package.
func (m *Model) renderList(md string) string {
	lines := strings.Split(md, "\n")
	var items []string
	var current strings.Builder

	flushCurrent := func() {
		if current.Len() > 0 {
			items = append(items, strings.TrimSpace(current.String()))
			current.Reset()
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if bulletPattern.MatchString(trimmed) {
			flushCurrent()
			content := bulletPattern.ReplaceAllString(trimmed, "")
			current.WriteString(content)
		} else if trimmed == "" {
			flushCurrent()
			items = append(items, "")
		} else if current.Len() > 0 {
			current.WriteString(" ")
			current.WriteString(trimmed)
		} else {
			flushCurrent()
			items = append(items, trimmed)
		}
	}
	flushCurrent()

	var nonEmpty []string
	for _, item := range items {
		if item != "" {
			nonEmpty = append(nonEmpty, item)
		}
	}
	if len(nonEmpty) == 0 {
		return toolStyle.Render(md)
	}

	l := list.New()
	for _, item := range nonEmpty {
		l.Item(item)
	}
	l.EnumeratorStyle(listBulletStyle).ItemStyle(listItemStyle)
	return l.String()
}

// renderTree renders tree-like content using lipgloss's tree package.
func (m *Model) renderTree(content string) string {
	lines := strings.Split(strings.TrimSpace(content), "\n")

	var rootName string
	var start int
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		_, rootName = splitTreePrefix(line)
		start = i + 1
		break
	}
	if rootName == "" {
		return toolStyle.Render(content)
	}

	t := tree.New().Root(rootName).
		Enumerator(tree.RoundedEnumerator).
		EnumeratorStyle(treeStyle).
		RootStyle(treeItemStyle).
		ItemStyle(treeItemStyle)
	stack := []*tree.Tree{t}

	for _, line := range lines[start:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		prefix, name := splitTreePrefix(line)
		depth := treeDepth(prefix)
		if depth < 1 {
			depth = 1
		}

		for len(stack) > depth {
			stack = stack[:len(stack)-1]
		}

		parent := stack[len(stack)-1]
		node := tree.New().Root(name).
			Enumerator(tree.RoundedEnumerator).
			EnumeratorStyle(treeStyle).
			ItemStyle(treeItemStyle)
		parent.Child(node)
		stack = append(stack, node)
	}

	return t.String()
}

// reRenderMessages re-renders all assistant messages through the current
// glamour renderer (used on window resize when word-wrap width changes).
func (m *Model) reRenderMessages() {
	for i := range m.messages {
		if m.messages[i].Role == "assistant" && m.messages[i].Raw != "" {
			m.messages[i].Content = m.renderMarkdown(m.messages[i].Raw)
		}
	}
}

// renderMessages produces the full chat content (messages, streaming,
// thinking indicator, tool call) as a single string suitable for
// handing to the viewport. Width is m.width; callers must ensure
// m.width is set.
func (m *Model) renderMessages() string {
	var b strings.Builder

	m.reasoningZones = m.reasoningZones[:0]
	m.toolZones = m.toolZones[:0]
	m.subagentZones = m.subagentZones[:0]

	// Persistent todo list - always visible when there are tasks
	if len(m.todos) > 0 {
		todoTable := NewTodoTable(m.todos, m.width)
		b.WriteString(todoTable.Render())
		b.WriteString("\n\n")
	}

	for msgIdx, msg := range m.messages {
		switch msg.Role {
		case "user":
			b.WriteString(NewUserMessage(msg.Content, m.width).Render())

		case "assistant":
			if m.verbose && msg.Reasoning != "" {
				b.WriteString("\n")
				zoneID := fmt.Sprintf("reasoning-%d", msgIdx)
				m.reasoningZones = append(m.reasoningZones, zoneID)
				b.WriteString(NewExpandableSection(zoneID, lolcatRender("Reasoning..."), m.reasoningExpanded[zoneID], msg.Reasoning, m.width, reasoningBgStyle, thinkingStyle).AsPreWrapped().Render())
			}
			if msg.Content != "" {
				b.WriteString("\n")
				b.WriteString(NewAssistantMessage(msg.Content).Render())
				b.WriteString("\n\n")
			} else {
				b.WriteString("\n")
			}

		case "subagent":
			zoneID := fmt.Sprintf("subagent-%d", msgIdx)
			if msg.SubResult != "" {
				m.subagentZones = append(m.subagentZones, zoneID)
			}
			b.WriteString(NewSubAgentLine(zoneID, msg.SubRole, msg.Content, msg.SubRunning, msg.ToolDuration, msg.SubError, msg.SubResult, m.width, m.viewport.Height(), m.subagentExpanded[zoneID]).Render())

		case "tool":
			if m.verbose {
				zoneID := fmt.Sprintf("tool-%d", msgIdx)
				m.toolZones = append(m.toolZones, zoneID)

				expanded, has := m.toolExpanded[zoneID]
				if !has {
					expanded = m.toolCall == msg.ToolName
				}

				b.WriteString(NewToolMessage(zoneID, msg.ToolName, msg.ToolArgs, msg.Content, m.width, m.viewport.Height(), expanded, m.toolCall == msg.ToolName, msg.ToolDuration).Render())
			}

		case "error":
			b.WriteString(NewErrorMessage(msg.Content, m.width, m.viewport.Height()).Render())

		default:
			b.WriteString(NewSystemMessage(msg.Content, m.width).Render())
		}
	}

	if m.verbose && m.thinkContent != "" {
		b.WriteString("\n")
		if m.thinking && !m.streaming {
			rendered := lolcatRender(fmt.Sprintf("  %s Reasoning...", stripANSI(m.spinner.View())))
			b.WriteString(rendered)
			b.WriteString("\n\n")
			b.WriteString(reasoningBgStyle.Width(m.width).PaddingLeft(4).Render(
				thinkingStyle.Render(m.thinkContent)))
		} else {
			m.reasoningZones = append(m.reasoningZones, "reasoning-live")
			if !m.reasoningExpanded["reasoning-live"] {
				b.WriteString(zone.Mark("reasoning-live", lolcatRender("  ▶ Reasoning...")))
				b.WriteString("\n")
			} else {
				b.WriteString(zone.Mark("reasoning-live", lolcatRender("  ▼ Reasoning...")))
				b.WriteString("\n")
				b.WriteString(reasoningBgStyle.Width(m.width).PaddingLeft(4).Render(
					thinkingStyle.Render(m.thinkContent)))
			}
			b.WriteString("\n")
		}
	}

	if m.streaming && m.streamContent != "" {
		b.WriteString("\n")
		rendered := assistantStyle.Render(m.renderMarkdown(m.streamContent))
		b.WriteString(rendered)
		b.WriteString("\n\n")
	}

	if m.thinking && !m.streaming && m.thinkContent == "" {
		b.WriteString("\n")
		rendered := lolcatRender(fmt.Sprintf("  %s Thinking...", stripANSI(m.spinner.View())))
		b.WriteString(rendered)
		b.WriteString("\n\n")
	}

	return b.String()
}

// renderMCPStatus builds a status string for all discovered MCP servers.
func (m *Model) renderMCPStatus() string {
	if len(m.mcpInfos) == 0 {
		return "No MCP servers configured. Add mcp_servers to config.yaml"
	}
	var b strings.Builder
	b.WriteString("MCP Servers:\n")
	for _, info := range m.mcpInfos {
		status := "✓ connected"
		if !info.Connected {
			status = "✗ offline"
		}
		label := info.Name
		switch info.Transport {
		case "http":
			label += " → " + info.URL
		case "stdio":
			label += " → " + info.Command
		}
		b.WriteString(fmt.Sprintf("  %s  %s", status, label))
		if info.ToolCount > 0 {
			b.WriteString(fmt.Sprintf(" (%d tools)", info.ToolCount))
		}
		if info.Error != "" {
			b.WriteString(fmt.Sprintf("\n         error: %s", info.Error))
		}
		b.WriteString("\n")
	}
	return b.String()
}
