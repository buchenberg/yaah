package tui2

// thinking.go — thinking indicator control.

// ShowThinking shows the animated thinking indicator.
func (t *TUI2) ShowThinking() {
	t.agentActive = true
	t.thinkingInd.Show()
	t.markDirty()
	t.renderInfoPane()
}

// HideThinking hides the animated thinking indicator.
func (t *TUI2) HideThinking() {
	t.agentActive = false
	t.thinkingInd.Hide()
	t.markDirty()
	t.renderInfoPane()
}
