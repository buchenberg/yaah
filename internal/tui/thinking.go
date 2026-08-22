package tui

// thinking.go — thinking indicator control.

// ShowThinking shows the animated thinking indicator. The indicator line is
// appended by the next full render, so entering the visible state forces a
// rebuild.
func (t *App) ShowThinking() {
	t.agentActive = true
	if !t.thinkingInd.Visible() {
		t.thinkingInd.Show()
		t.needsFullRender.Store(true)
	}
	t.markDirty()
	t.renderInfoPane()
}

// HideThinking hides the animated thinking indicator. The indicator line may
// be baked into the last full render, so leaving the visible state forces a
// rebuild to remove it.
func (t *App) HideThinking() {
	t.agentActive = false
	if t.thinkingInd.Hide() {
		t.needsFullRender.Store(true)
	}
	t.markDirty()
	t.renderInfoPane()
}
