package tui

// thinking.go — thinking indicator control.

// ShowThinking shows the animated thinking indicator.
func (t *App) ShowThinking() {
	t.agentActive = true
	t.thinkingInd.Show()
	t.markDirty()
	t.renderInfoPane()
}

// HideThinking hides the animated thinking indicator.
func (t *App) HideThinking() {
	t.agentActive = false
	t.thinkingInd.Hide()
	t.markDirty()
	t.renderInfoPane()
}
