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

// AdvanceThinking advances the thinking spinner and lolcat seed.
func (t *TUI2) AdvanceThinking() {
	if t.thinkingInd.Visible() {
		t.thinkingInd.Advance()
		t.markDirty()
	}
}
