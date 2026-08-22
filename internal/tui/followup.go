package tui

// submitInput sends the current input text as a new prompt, if any.
func (t *App) submitInput() {
	if t.OnSubmit != nil {
		text := t.Input.GetText()
		if text != "" {
			t.flushRefresh()
			t.Input.SetText("", false)
			t.OnSubmit(text)
		}
	}
}

// submitFollowUp sends the current input text as a follow-up, if any.
func (t *App) submitFollowUp() {
	if t.OnFollowUp != nil {
		text := t.Input.GetText()
		if text != "" {
			t.flushRefresh()
			t.Input.SetText("", false)
			t.OnFollowUp(text)
		}
	}
}

// clearConversation resets all conversation state and re-renders the view.
func (t *App) clearConversation() {
	t.conversationLog = nil
	t.userScrolled = false
	t.resetUsage()
	t.refreshMessages()
}

// doClear clears the conversation and invokes the OnClear callback, if set.
func (t *App) doClear() {
	if t.OnClear != nil {
		t.OnClear()
	}
	t.clearConversation()
}
