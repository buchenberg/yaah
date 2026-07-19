package tui

import (
	"fmt"
	"regexp"
	"strings"

	"charm.land/bubbles/v2/key"

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
	if toolName == "task" {
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

	for msgIdx, msg := range m.messages {
		switch msg.Role {
		case "user":
			rendered := userStyle.Render(chatWrap("", msg.Content, m.width))
			b.WriteString(userBgStyle.Width(m.width).Render(rendered))
			b.WriteString("\n")

		case "assistant":
			if msg.Reasoning != "" {
				b.WriteString("\n")
				zoneID := fmt.Sprintf("reasoning-%d", msgIdx)
				m.reasoningZones = append(m.reasoningZones, zoneID)
				if !m.reasoningExpanded[zoneID] {
					b.WriteString(zone.Mark(zoneID, toggleStyle.Render("  ▶ Reasoning...")))
				} else {
					b.WriteString(zone.Mark(zoneID, toggleStyle.Render("  ▼ Reasoning...")))
					b.WriteString("\n\n")
					b.WriteString(reasoningBgStyle.Width(m.width).Render(
						thinkingStyle.Render(chatWrap("", msg.Reasoning, m.width))))
				}
				b.WriteString("\n")
			}
			b.WriteString("\n")
			b.WriteString(assistantStyle.Render(msg.Content))
			b.WriteString("\n\n")

		case "tool":
			// Build a header from the tool name and args.
			header := msg.ToolName
			if msg.ToolName == "task" && msg.ToolArgs != "" {
				re := regexp.MustCompile(`"description"\s*:\s*"([^"]*)"`)
				if match := re.FindStringSubmatch(msg.ToolArgs); len(match) > 1 && match[1] != "" {
					header = "sub-agent — " + match[1]
				} else {
					header = "sub-agent"
				}
			} else if msg.ToolName == "webfetch" && msg.ToolArgs != "" {
				re := regexp.MustCompile(`"url"\s*:\s*"([^"]*)"`)
				if match := re.FindStringSubmatch(msg.ToolArgs); len(match) > 1 && match[1] != "" {
					header = "web_fetch → " + match[1]
				}
			} else if msg.ToolName == "bash" && msg.ToolArgs != "" {
				header = "bash — " + msg.ToolArgs
			}

			// Icon: ⏳ if still running, ✓ if completed.
			icon := "✓"
			if m.toolCall == msg.ToolName {
				icon = "⏳"
			}

			zoneID := fmt.Sprintf("tool-%d", msgIdx)
			m.toolZones = append(m.toolZones, zoneID)

			// Default: collapsed when completed, expanded while running.
			expanded, has := m.toolExpanded[zoneID]
			if !has {
				expanded = m.toolCall == msg.ToolName
			}

			if expanded {
				b.WriteString(zone.Mark(zoneID, toolStyle.Render(fmt.Sprintf("  ▼ %s %s", icon, header))))
				b.WriteString("\n")
				// Tool output content — toolIndent handles wrapping to fit within width.
				indented := toolIndent(m.width, msg.Content)
				// Cap visible lines to prevent overflow past footer/prompt.
				maxLines := m.viewport.Height() / 3
				if maxLines < 8 {
					maxLines = 8
				}
				if maxLines > 24 {
					maxLines = 24
				}
				indentedLines := strings.Split(indented, "\n")
				totalLines := len(indentedLines)
				var visible string
				if totalLines > maxLines {
					// Show the last maxLines lines with a truncation header.
					tail := indentedLines[totalLines-maxLines:]
					omitted := totalLines - maxLines
					headerLine := toolStyle.Render(fmt.Sprintf("  ··· %d more lines above ···", omitted))
					visible = headerLine + "\n" + strings.Join(tail, "\n")
				} else {
					visible = indented
				}
				// Wrap in a bordered box constrained to viewport width.
				boxWidth := m.width - 4 // leave margin for indent
				if boxWidth < 20 {
					boxWidth = 20
				}
				b.WriteString(toolBoxStyle.Width(boxWidth).Render(visible))
			} else {
				b.WriteString(zone.Mark(zoneID, toolStyle.Render(fmt.Sprintf("  ▶ %s %s", icon, header))))
			}
			b.WriteString("\n")

		default:
			rendered := chatWrap("", msg.Content, m.width)
			rendered = systemBgStyle.Width(m.width).Render(systemStyle.Render(rendered))
			b.WriteString(rendered)
			b.WriteString("\n")
		}
	}

	if m.thinkContent != "" {
		b.WriteString("\n")
		if m.thinking && !m.streaming {
			rendered := spinnerStyle.Render(fmt.Sprintf("  %s Reasoning...", m.spinner.View()))
			b.WriteString(rendered)
			b.WriteString("\n")
			b.WriteString(reasoningBgStyle.Width(m.width).Render(
				thinkingStyle.Render(chatWrap("", m.thinkContent, m.width))))
		} else {
			m.reasoningZones = append(m.reasoningZones, "reasoning-live")
			if !m.reasoningExpanded["reasoning-live"] {
				b.WriteString(zone.Mark("reasoning-live", toggleStyle.Render("  ▶ Reasoning...")))
			} else {
				b.WriteString(zone.Mark("reasoning-live", toggleStyle.Render("  ▼ Reasoning...")))
				b.WriteString("\n")
				b.WriteString(reasoningBgStyle.Width(m.width).Render(
					thinkingStyle.Render(chatWrap("", m.thinkContent, m.width))))
			}
			b.WriteString("\n")
		}
		b.WriteString("\n\n")
	}

	if m.streaming && m.streamContent != "" {
		b.WriteString("\n")
		rendered := assistantStyle.Render(m.renderMarkdown(m.streamContent))
		b.WriteString(rendered)
		b.WriteString("\n\n")
	}

	if m.thinking && !m.streaming && m.thinkContent == "" {
		b.WriteString("\n")
		rendered := spinnerStyle.Render(fmt.Sprintf("  %s Thinking...", m.spinner.View()))
		b.WriteString(rendered)
		b.WriteString("\n\n")
	}

	return b.String()
}

// renderModelPalette renders the model selection list above the input.
func (m *Model) renderModelPalette() string {
	filtered := m.filteredModels()
	if len(filtered) == 0 {
		return commandPaletteStyle.Render("No matching models")
	}

	// Build display rows: provider heading + model items
	type row struct {
		isHeading bool
		text      string
		modelIdx  int
	}
	var rows []row
	lastProvider := ""
	for i, model := range filtered {
		parts := strings.SplitN(model, "/", 2)
		providerKey := parts[0]
		name := model
		if len(parts) == 2 {
			name = parts[1]
		}
		if providerKey != lastProvider {
			displayName := providerKey
			if m.providerNames != nil {
				if dn, ok := m.providerNames[providerKey]; ok && dn != "" {
					displayName = dn
				}
			}
			rows = append(rows, row{isHeading: true, text: displayName})
			lastProvider = providerKey
		}
		rows = append(rows, row{text: name, modelIdx: i})
	}

	selectedRowIdx := 0
	for i, r := range rows {
		if r.modelIdx == m.modelSelected {
			selectedRowIdx = i
			break
		}
	}

	maxVisible := m.maxModelLines()
	start := selectedRowIdx - maxVisible/2
	if start < 0 {
		start = 0
	}
	end := start + maxVisible
	if end > len(rows) {
		end = len(rows)
		start = end - maxVisible
		if start < 0 {
			start = 0
		}
	}

	current := m.provider + "/" + m.modelName
	var lines []string
	for i := start; i < end; i++ {
		r := rows[i]
		if r.isHeading {
			lines = append(lines, commandNameStyle.Render(r.text))
			continue
		}
		model := filtered[r.modelIdx]
		marker := "  "
		if r.modelIdx == m.modelSelected {
			marker = "> "
		}
		styled := listItemStyle.Render(r.text)
		if model == current {
			styled = commandNameStyle.Render(r.text + " (current)")
		}
		lines = append(lines, "  "+marker+styled)
	}

	return commandPaletteStyle.Render(strings.Join(lines, "\n"))
}

// renderCommandPalette renders the command suggestion list above the input.
func (m *Model) renderCommandPalette() string {
	filter := strings.TrimPrefix(strings.TrimSpace(m.input.Value()), ":")
	filter = strings.ToLower(filter)

	var visible []Command
	for _, c := range m.commands {
		name := strings.TrimPrefix(c.Name, ":")
		if filter == "" || strings.HasPrefix(strings.ToLower(name), filter) {
			visible = append(visible, c)
		}
	}

	var lines []string
	for _, c := range visible {
		name := commandNameStyle.Render(c.Name)
		desc := commandDescStyle.Render(c.Description)
		lines = append(lines, name+" "+desc)
	}

	if len(lines) == 0 {
		return ""
	}

	return commandPaletteStyle.Width(m.width).Render(strings.Join(lines, "\n"))
}

// renderQuestionModal renders the interactive question dialog.
func (m *Model) renderQuestionModal() string {
	contentWidth := m.width - 6
	if contentWidth < 20 {
		contentWidth = 20
	}

	var lines []string
	lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(listBulletStyle.GetForeground()).Render(chatWrap("", m.questionModal.Header, contentWidth)))
	lines = append(lines, "")
	lines = append(lines, chatWrap("", m.questionModal.Question, contentWidth))
	lines = append(lines, "")

	maxVisible := m.maxQuestionLines()
	start := m.questionIdx - maxVisible/2
	if start < 0 {
		start = 0
	}
	end := start + maxVisible
	if end > len(m.questionModal.Options) {
		end = len(m.questionModal.Options)
		start = end - maxVisible
		if start < 0 {
			start = 0
		}
	}

	for i := start; i < end; i++ {
		opt := m.questionModal.Options[i]
		prefix := "  "
		if m.questionModal.Multiple {
			if m.questionMulti[i] {
				prefix = " ☑ "
			} else {
				prefix = " ☐ "
			}
		} else {
			if i == m.questionIdx {
				prefix = " ▶ "
			}
		}
		fullLine := prefix + opt.Label
		if opt.Description != "" {
			fullLine += " — " + opt.Description
		}
		wrapped := chatWrap(prefix, fullLine[len(prefix):], contentWidth)
		lines = append(lines, zone.Mark(fmt.Sprintf("question-opt-%d", i), listItemStyle.Render(wrapped)))
	}

	if start > 0 || end < len(m.questionModal.Options) {
		lines = append(lines, commandDescStyle.Render(fmt.Sprintf("  (%d-%d of %d)", start+1, end, len(m.questionModal.Options))))
	}

	lines = append(lines, "")
	help := "↑↓ navigate · Enter select · Esc cancel"
	if m.questionModal.Multiple {
		help += " · Space toggle"
	}
	lines = append(lines, commandDescStyle.Render(help))

	return commandPaletteStyle.Width(m.width).Render(strings.Join(lines, "\n"))
}

// renderHelpOverlay renders a full help screen with all keybindings.
func (m *Model) renderHelpOverlay() string {
	contentWidth := m.width - 6
	if contentWidth < 30 {
		contentWidth = 30
	}

	var lines []string
	lines = append(lines, boldStyle.Render("Keybindings"))
	lines = append(lines, "")

	type group struct {
		title    string
		bindings []key.Binding
	}
	groups := []group{
		{"Navigation", []key.Binding{keys.Up, keys.Down, keys.PageUp, keys.PageDown, keys.Top, keys.Bottom}},
		{"Actions", []key.Binding{keys.Search, keys.Copy, keys.Reasoning, keys.Help}},
		{"Input", []key.Binding{keys.Submit, keys.Cancel}},
		{"Commands", []key.Binding{keys.Commands}},
		{"System", []key.Binding{keys.Quit}},
	}

	for _, g := range groups {
		lines = append(lines, boldStyle.Render(g.title))
		for _, b := range g.bindings {
			keys := strings.Join(b.Keys(), ", ")
			line := fmt.Sprintf("  %-18s %s", keys, b.Help().Desc)
			lines = append(lines, chatWrap("", line, contentWidth))
		}
		lines = append(lines, "")
	}

	lines = append(lines, commandDescStyle.Render("Press any key to close"))

	return commandPaletteStyle.Width(m.width).Render(strings.Join(lines, "\n"))
}

// renderMCPStatus builds a status string for all discovered MCP servers.
func (m *Model) renderMCPStatus() string {
	if len(m.mcpInfos) == 0 {
		return "No MCP servers configured. Add manifests to ~/.yaah/mcp/"
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
