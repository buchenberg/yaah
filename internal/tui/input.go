package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/atotto/clipboard"
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
	case ":verbose":
		m.ToggleVerbose()
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
		for _, zoneID := range m.subagentZones {
			if z := zone.Get(zoneID); z != nil && z.InBounds(msg) {
				m.subagentExpanded[zoneID] = !m.subagentExpanded[zoneID]
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

	case key.Matches(msg, keys.Verbose):
		m.ToggleVerbose()
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
