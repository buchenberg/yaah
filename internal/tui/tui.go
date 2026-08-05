package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/atotto/clipboard"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/buchenberg/yaah/internal/observability"
)

// RegisterCommand adds a slash command at runtime. Safe to call from
// any goroutine (e.g., when MCP tools register themselves).
func (m *Model) RegisterCommand(name, description string) {
	m.commands = append(m.commands, Command{Name: name, Description: description})
}

// executeCommand executes a colon command and adds the result to messages.
// :quit is handled by the caller (returns tea.Quit from Update).
func (m *Model) executeCommand(input string) {
	cmd := strings.TrimSpace(input)
	switch cmd {
	case ":help":
		var b strings.Builder
		b.WriteString("Available commands:\n")
		for _, c := range m.commands {
			b.WriteString(fmt.Sprintf("  %s  %s\n", c.Name, c.Description))
		}
		m.AddMessage("system", b.String())
	case ":clear":
		m.messages = nil
		m.refreshViewport()
	case ":compact":
		if m.onCompact != nil {
			m.onCompact()
		}
	case ":banner":
		m.showBanner = !m.showBanner
		m.adjustViewport()
		m.refreshViewport()
		if m.showBanner {
			m.SetEphemeral("Banner shown.")
		} else {
			m.SetEphemeral("Banner hidden. Use /banner to show it again.")
		}
	case ":model":
		if len(m.modelItems) == 0 {
			m.AddMessage("system", "No models available. Configure providers or wait for model list to load.")
			return
		}
		m.modelMode = true
		m.modelSelected = 0
		m.input.SetValue("")
		m.input.Placeholder = "Search models..."
		m.clearCommandMode()
		m.adjustViewport()
		return
	case ":mcp":
		m.AddMessage("system", m.renderMCPStatus())
	case ":login":
		if m.onLogin != nil {
			m.onLogin()
		} else {
			m.AddMessage("system", "Login not available in this mode.")
		}
	case ":logout":
		if m.onLogout != nil {
			m.onLogout()
		} else {
			m.AddMessage("system", "Logout not available in this mode.")
		}
	case ":stop":
		if !m.thinking {
			m.AddMessage("system", "No agent is running.")
			return
		}
		if m.onAbort != nil {
			m.onAbort()
		}
		m.SetEphemeral("Agent stopped.")
	case ":copyview":
		scanned := zone.Scan(m.lastBody)
		plain := stripANSI(scanned)
		if err := clipboard.WriteAll(plain); err != nil {
			m.SetEphemeral("Copy failed: " + err.Error())
		} else {
			m.SetEphemeral("View copied to clipboard.")
		}
	default:
		// :steer is the only command that takes an argument, so it
		// doesn't fit cleanly into a static switch case. Match the
		// prefix and handle it before falling through to unknown.
		if strings.HasPrefix(cmd, ":steer") {
			body := strings.TrimSpace(strings.TrimPrefix(cmd, ":steer"))
			if body == "" {
				m.AddMessage("system", "Usage: :steer <text to inject>")
				return
			}
			if !m.thinking {
				m.AddMessage("system", "Steer is only meaningful while the agent is running. Type and press Enter to send a new message instead.")
				return
			}
			m.AddMessage("user", body+"  ⚡")
			if m.onSteer != nil {
				m.onSteer(body)
			}
			return
		}
		m.AddMessage("system", fmt.Sprintf("Unknown command: %s", cmd))
	}
}

// selectModel applies the currently highlighted model and exits model mode.
func (m *Model) selectModel() {
	filtered := m.filteredModels()
	if m.modelSelected < len(filtered) {
		selected := filtered[m.modelSelected]
		parts := strings.SplitN(selected, "/", 2)
		providerName := parts[0]
		modelName := selected
		if len(parts) == 2 {
			modelName = parts[1]
		}
		m.provider = providerName
		m.modelName = modelName
		if m.onModel != nil {
			m.onModel(providerName, modelName)
		}
	}
	m.exitModelMode()
}

// filteredModels returns modelItems filtered by the current input value.
func (m *Model) filteredModels() []string {
	filter := strings.ToLower(m.input.Value())
	if filter == "" {
		return m.modelItems
	}
	var out []string
	for _, model := range m.modelItems {
		if strings.Contains(strings.ToLower(model), filter) {
			out = append(out, model)
		}
	}
	return out
}

// exitModelMode exits model-selection mode and resets the input.
func (m *Model) exitModelMode() {
	m.modelMode = false
	m.modelSelected = 0
	m.input.SetValue("")
	m.input.Placeholder = "Type a message..."
	m.adjustViewport()
}

// --- mouse ---

func (m *Model) handleMouseClick(msg tea.MouseClickMsg) tea.Cmd {
	if msg.Button == tea.MouseLeft {
		if m.questionMode {
			for i := range m.questionModal.Options {
				zoneID := fmt.Sprintf("question-opt-%d", i)
				if z := zone.Get(zoneID); z != nil && z.InBounds(msg) {
					if m.questionModal.Multiple {
						m.questionMulti[i] = !m.questionMulti[i]
					}
					m.questionIdx = i
					m.refreshViewport()
					return nil
				}
			}
			return nil
		}
		for _, zoneID := range m.reasoningZones {
			if z := zone.Get(zoneID); z != nil && z.InBounds(msg) {
				m.reasoningExpanded[zoneID] = !m.reasoningExpanded[zoneID]
				m.refreshViewport()
				return nil
			}
		}
		for _, zoneID := range m.toolZones {
			if z := zone.Get(zoneID); z != nil && z.InBounds(msg) {
				m.toolExpanded[zoneID] = !m.toolExpanded[zoneID]
				m.refreshViewport()
				return nil
			}
		}
	}
	return m.viewportUpdate(msg)
}

func (m *Model) viewportUpdate(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return cmd
}

// --- key dispatch ---

// handleKeyPress routes a key press to the active mode handler.
func (m *Model) handleKeyPress(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Dismiss overlays first
	if m.showHelp {
		m.showHelp = false
		m.adjustViewport()
		return m, nil
	}
	if m.searchMode {
		return m, m.handleSearchKey(msg)
	}
	if m.questionMode {
		return m, m.handleQuestionKey(msg)
	}
	if m.modelMode {
		return m, m.handleModelKey(msg)
	}
	return m, m.handleNormalKey(msg)
}

func (m *Model) handleSearchKey(msg tea.KeyPressMsg) tea.Cmd {
	switch {
	case key.Matches(msg, keys.Cancel):
		m.searchMode = false
		m.searchQuery = ""
		return nil
	case key.Matches(msg, keys.NextMatch):
		m.searchNextMatch()
		return nil
	case key.Matches(msg, keys.PrevMatch):
		m.searchPrevMatch()
		return nil
	case key.Matches(msg, keys.Submit):
		m.searchMode = false
		return nil
	case msg.String() == "backspace":
		if len(m.searchQuery) > 0 {
			m.searchQuery = m.searchQuery[:len(m.searchQuery)-1]
			m.buildSearchMatches()
		}
		return nil
	default:
		s := msg.String()
		if len(s) == 1 && s[0] >= 32 && s[0] < 127 {
			m.searchQuery += s
			m.buildSearchMatches()
		}
		return nil
	}
}

func (m *Model) handleQuestionKey(msg tea.KeyPressMsg) tea.Cmd {
	switch {
	case key.Matches(msg, keys.Cancel):
		m.answerQuestion("")
		return nil
	case key.Matches(msg, keys.Submit):
		m.commitQuestionAnswer()
		return nil
	case key.Matches(msg, keys.Up):
		if m.questionIdx > 0 {
			m.questionIdx--
		}
		m.refreshViewport()
		return nil
	case key.Matches(msg, keys.Down):
		if m.questionIdx < len(m.questionModal.Options)-1 {
			m.questionIdx++
		}
		m.refreshViewport()
		return nil
	case msg.String() == "space":
		if m.questionModal.Multiple {
			m.questionMulti[m.questionIdx] = !m.questionMulti[m.questionIdx]
		}
		m.refreshViewport()
		return nil
	}
	return nil
}

func (m *Model) handleModelKey(msg tea.KeyPressMsg) tea.Cmd {
	switch {
	case key.Matches(msg, keys.Cancel):
		m.exitModelMode()
		return nil
	case key.Matches(msg, keys.Up):
		if m.modelSelected > 0 {
			m.modelSelected--
		}
		return nil
	case key.Matches(msg, keys.Down):
		filtered := m.filteredModels()
		if m.modelSelected < len(filtered)-1 {
			m.modelSelected++
		}
		return nil
	case key.Matches(msg, keys.Submit):
		m.selectModel()
		return nil
	}
	return nil
}

func (m *Model) handleNormalKey(msg tea.KeyPressMsg) tea.Cmd {
	switch {
	case key.Matches(msg, keys.Quit):
		if m.onQuit != nil {
			m.onQuit()
		}
		return tea.Quit

	case key.Matches(msg, keys.Cancel):
		if m.thinking && m.onAbort != nil {
			m.onAbort()
		}
		if m.commandMode {
			m.input.SetValue("")
			m.clearCommandMode()
		}
		return nil

	case key.Matches(msg, keys.Help):
		if !m.commandMode && m.input.Value() == "" {
			m.showHelp = true
			m.adjustViewport()
			return nil
		}

	case key.Matches(msg, keys.Search):
		if !m.commandMode && m.input.Value() == "" {
			m.searchMode = true
			m.searchQuery = ""
			m.searchMatches = nil
			m.searchIdx = -1
			return nil
		}

	case key.Matches(msg, keys.Copy):
		for i := len(m.messages) - 1; i >= 0; i-- {
			if m.messages[i].Role == "assistant" && m.messages[i].Raw != "" {
				return tea.SetClipboard(m.messages[i].Raw)
			}
		}
		return nil

	case key.Matches(msg, keys.Reasoning):
		if m.hasReasoning() {
			anyExpanded := false
			for _, zid := range m.reasoningZones {
				if m.reasoningExpanded[zid] {
					anyExpanded = true
					break
				}
			}
			for _, zid := range m.reasoningZones {
				m.reasoningExpanded[zid] = !anyExpanded
			}
			m.refreshViewport()
		}
		return nil

	case key.Matches(msg, keys.Top):
		if !m.commandMode {
			m.viewport.GotoTop()
		}
		return nil

	case key.Matches(msg, keys.Bottom):
		if !m.commandMode {
			m.viewport.GotoBottom()
		}
		return nil

	case key.Matches(msg, keys.Submit):
		if m.commandMode {
			value := m.input.Value()
			m.input.SetValue("")
			m.clearCommandMode()
			if strings.TrimSpace(value) == ":quit" {
				return tea.Quit
			}
			m.executeCommand(value)
			return nil
		}
		value := m.input.Value()
		if strings.TrimSpace(value) == "" {
			return nil
		}
		if m.thinking {
			m.activePrompt = value
			m.input.SetValue("")
			if m.onFollowUp != nil {
				m.onFollowUp(value)
			}
			return nil
		}
		m.thinkContent = ""
		m.reasoningExpanded = make(map[string]bool)
		m.SetThinking(true)
		m.activePrompt = value
		m.input.SetValue("")
		if m.onSubmit != nil {
			m.onSubmit(value)
		}
		return nil

	case key.Matches(msg, keys.Up), key.Matches(msg, keys.Down),
		key.Matches(msg, keys.PageUp), key.Matches(msg, keys.PageDown):
		if !m.commandMode {
			return m.viewportUpdate(msg)
		}
	}

	// Key not consumed by any binding — forward to text input.
	var cmd tea.Cmd
	oldHeight := m.input.Height()
	m.input, cmd = m.input.Update(msg)
	if m.input.Height() != oldHeight {
		m.adjustViewport()
	}
	m.detectCommandMode()
	return cmd
}

// detectCommandMode enables or disables command mode based on the input prefix.
func (m *Model) detectCommandMode() {
	if m.modelMode || m.questionMode {
		return
	}
	if strings.HasPrefix(m.input.Value(), ":") {
		if !m.commandMode {
			m.commandMode = true
			m.adjustViewport()
		}
	} else {
		if m.commandMode {
			m.clearCommandMode()
		}
	}
}

func (m *Model) clearCommandMode() {
	m.commandMode = false
	m.adjustViewport()
}

func (m *Model) hasReasoning() bool {
	if m.thinkContent != "" {
		return true
	}
	for _, msg := range m.messages {
		if msg.Reasoning != "" {
			return true
		}
	}
	return false
}

func (m *Model) answerQuestion(labels string) {
	m.questionModal.AnswerCh <- labels
	m.questionMode = false
	m.questionModal = QuestionModal{}
	m.questionMulti = nil
	m.input.Placeholder = "Type a message..."
	m.adjustViewport()
}

func (m *Model) commitQuestionAnswer() {
	if m.questionModal.Multiple {
		var selected []string
		for i, toggled := range m.questionMulti {
			if toggled {
				selected = append(selected, m.questionModal.Options[i].Label)
			}
		}
		m.answerQuestion(strings.Join(selected, ", "))
	} else {
		m.answerQuestion(m.questionModal.Options[m.questionIdx].Label)
	}
}

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

// --- search ---

// buildSearchMatches scans the rendered message content for lines containing
// the current search query (case-insensitive) and populates m.searchMatches
// with line indices.
func (m *Model) buildSearchMatches() {
	m.searchMatches = nil
	m.searchIdx = -1
	if m.searchQuery == "" {
		return
	}
	query := strings.ToLower(m.searchQuery)
	content := m.viewport.View()
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.Contains(strings.ToLower(line), query) {
			m.searchMatches = append(m.searchMatches, i)
		}
	}
	if len(m.searchMatches) > 0 {
		m.searchIdx = 0
		m.scrollToMatch()
	}
}

// searchNextMatch advances to the next search match.
func (m *Model) searchNextMatch() {
	if len(m.searchMatches) == 0 {
		return
	}
	m.searchIdx++
	if m.searchIdx >= len(m.searchMatches) {
		m.searchIdx = 0
	}
	m.scrollToMatch()
}

// searchPrevMatch moves to the previous search match.
func (m *Model) searchPrevMatch() {
	if len(m.searchMatches) == 0 {
		return
	}
	m.searchIdx--
	if m.searchIdx < 0 {
		m.searchIdx = len(m.searchMatches) - 1
	}
	m.scrollToMatch()
}

// scrollToMatch scrolls the viewport to the current search match line.
func (m *Model) scrollToMatch() {
	if m.searchIdx < 0 || m.searchIdx >= len(m.searchMatches) {
		return
	}
	m.viewport.SetYOffset(m.searchMatches[m.searchIdx])
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


