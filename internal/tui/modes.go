package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

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
