package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/buchenberg/yaah/internal/observability"
)

// paletteLines returns the number of terminal rows the command palette
// occupies when visible. Includes the rounded border (2) and padding (2).
// maxModelLines returns the maximum number of model items that can fit
// in the terminal without pushing the input off-screen.
func (m *Model) maxModelLines() int {
	if m.height == 0 {
		return 10
	}
	inputHeight := m.inputAreaHeight()
	available := m.height - m.headerHeight() - 1 - inputHeight // 1: status line
	items := available - 4                                     // border (2) + padding (2)
	if items < 1 {
		items = 1
	}
	return items
}

func (m *Model) maxQuestionLines() int {
	n := m.maxModelLines() - 6 // 6 = header + blank + question + blank + blank + help
	if n < 1 {
		n = 1
	}
	return n
}

func (m *Model) paletteLines() int {
	if m.showHelp {
		// Help overlay: title + 4 groups with headers + footer + border/padding
		// Rough estimate: 22 content lines + 4 border/padding = 26.
		// Cap at 80% of available terminal height.
		available := m.height - m.headerHeight() - 1 - m.inputAreaHeight() // status, dynamic input area
		if available < 10 {
			return 10
		}
		max := available * 4 / 5
		helpLines := 26
		if helpLines > max {
			helpLines = max
		}
		return helpLines
	}
	if m.questionMode {
		optCount := len(m.questionModal.Options)
		max := m.maxQuestionLines()
		visible := optCount
		if visible > max {
			visible = max
		}
		lines := 10 + visible // 4 (border+padding) + 6 (header+question+help+blanks) + options
		if optCount > visible {
			lines++ // overflow indicator
		}
		return lines
	}
	if m.modelMode {
		filtered := m.filteredModels()
		if len(filtered) == 0 {
			return 4
		}
		// Count display rows: model items + provider heading rows
		rowCount := len(filtered)
		providers := make(map[string]bool)
		for _, model := range filtered {
			parts := strings.SplitN(model, "/", 2)
			providers[parts[0]] = true
		}
		rowCount += len(providers)
		truncated := rowCount > m.maxModelLines()
		if truncated {
			rowCount = m.maxModelLines()
		}
		lines := 4 + rowCount
		if truncated {
			lines++ // overflow indicator
		}
		return lines
	}
	if !m.commandMode {
		return 0
	}
	filter := strings.TrimPrefix(strings.TrimSpace(m.input.Value()), ":")
	filter = strings.ToLower(filter)
	count := 0
	for _, c := range m.commands {
		name := strings.TrimPrefix(c.Name, ":")
		if filter == "" || strings.HasPrefix(strings.ToLower(name), filter) {
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return 4 + count // border (2) + padding (2) + command lines
}

// --- layout ---

// adjustViewport recalculates and applies the viewport height based on
// current terminal dimensions, overlay state, and dynamic input height.
func (m *Model) adjustViewport() {
	if m.height == 0 {
		return
	}
	// Reserve space for header, status line, minimum chat area, and overlays.
	// Whatever is left is the maximum input height (including its border).
	overhead := m.headerHeight() + NewInfoBar("", "", 0).Height() + NewStatusBar("", 0, 0, false, 0).Height()
	minChat := 5
	if m.ephemMsg != "" {
		overhead++
	}
	paletteH := 0
	if m.commandMode || m.modelMode || m.questionMode || m.showHelp {
		paletteH = m.paletteLines()
	}
	searchH := 0
	if m.searchMode {
		searchH = 1
	}
	maxInputContent := m.height - overhead - minChat - paletteH - searchH - 2 // -2: border
	if maxInputContent < 1 {
		maxInputContent = 1
	}
	m.input.MaxHeight = maxInputContent

	// input area = content lines + top/bottom border (2 lines)
	inputHeight := m.inputAreaHeight()
	chatHeight := m.height - overhead - inputHeight - paletteH - searchH - 2 // -2: viewport border
	if chatHeight < minChat {
		chatHeight = minChat
	}
	m.viewport.SetWidth(m.width)
	m.viewport.SetHeight(chatHeight)
	m.refreshViewport()
}

// renderMessages produces the full chat content (messages, streaming,
// thinking indicator, tool call) as a single string suitable for
// handing to the viewport. Width is m.width; callers must ensure
// m.width is set.

// View implements tea.Model.
func (m *Model) View() tea.View {
	if m.width == 0 {
		return tea.NewView("Initializing...")
	}

	// Minimum size check: if the terminal is too small, show a message.
	if m.width < 60 || m.height < 20 {
		msg := lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("9")).
			Render(fmt.Sprintf(
				"Terminal too small — yaah needs at least 60×20 (current: %d×%d)",
				m.width, m.height))
		v := tea.NewView(zone.Scan(msg))
		v.AltScreen = true
		v.MouseMode = tea.MouseModeAllMotion
		return v
	}

	// Header: figlet banner + provider/model line (or compact if hidden)
	header := NewHeader(m.banner, m.provider, m.modelName, m.showBanner, m.width, m.mcpInfos, m.version).Render()

	activeView := ""
	if m.thinking || m.streaming {
		activeView = stripANSI(m.spinner.View())
	}

	// Info bar (between header and viewport) — shows active prompt
	infoBar := NewInfoBar(m.activePrompt, activeView, m.width).Render()

	// Status bar (1 line): message count + context bar only.
	status := NewStatusBar(m.cwd, len(m.messages), m.contextPct, m.contextWindow > 0, m.width).Render()

	// Ephemeral message line (shown only when active, auto-clears)
	var ephemLine string
	if m.ephemMsg != "" {
		ephemLine = noticeStyle.
			Width(m.width).
			Render(m.ephemMsg)
	}

	// Viewport holds the scrollable chat history
	viewportView := m.viewport.View()

	viewportView = "\n" + viewportView + "\n"

	// Search indicator line
	var searchLine string
	if m.searchMode {
		matchInfo := ""
		if len(m.searchMatches) > 0 && m.searchIdx >= 0 {
			matchInfo = fmt.Sprintf("  [%d/%d]", m.searchIdx+1, len(m.searchMatches))
		} else if m.searchQuery != "" && len(m.searchMatches) == 0 {
			matchInfo = "  [no matches]"
		}
		searchLine = commandDescStyle.Render(fmt.Sprintf("/%s%s", m.searchQuery, matchInfo))
	}

	// Palette (shown above input when in command, model, question, or help mode)
	var palette string
	if m.showHelp {
		palette = NewHelpOverlay(m.width).Render()
	} else if m.questionMode {
		palette = NewQuestionPalette(m.questionModal, m.questionIdx, m.questionMulti, m.maxQuestionLines(), m.width).Render()
	} else if m.modelMode {
		palette = NewModelPalette(m.filteredModels(), m.providerNames, m.modelSelected, m.provider+"/"+m.modelName, m.maxModelLines(), m.width).Render()
	} else if m.commandMode {
		palette = NewCommandPalette(m.commands, m.input.Value(), m.width).Render()
	}

	// Input (1 line)
	inputView := m.input.View()

	// Pink border around input area
	inputView = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("205")).
		Padding(0, 1).
		Width(m.width).
		Render(inputView)

	elements := []string{header, infoBar, viewportView, status}
	if ephemLine != "" {
		elements = append(elements, ephemLine)
	}
	if palette != "" {
		elements = append(elements, palette)
	}
	if searchLine != "" {
		elements = append(elements, searchLine)
	}
	elements = append(elements, inputView)
	body := lipgloss.JoinVertical(lipgloss.Left, elements...)
	m.lastBody = body
	scanned := zone.Scan(body)
	if m.recordView {
		m.recordView = false
		observability.RecordTUIView(body, scanned)
	}

	v := tea.NewView(scanned)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeAllMotion
	// OSC 22: change terminal cursor to pointer when over a clickable zone.
	// Supported by Kitty, WezTerm, foot, iTerm2, and others; ignored by terminals
	// that don't understand it.
	if m.hoveredZone {
		v.Content += "\x1b]22;pointer\x07"
	} else {
		v.Content += "\x1b]22;text\x07"
	}
	return v
}


