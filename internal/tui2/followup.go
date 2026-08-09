package tui2

// submitInput sends the current input text as a new prompt, if any.
func (t *TUI2) submitInput() {
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
func (t *TUI2) submitFollowUp() {
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
func (t *TUI2) clearConversation() {
	t.conversationLog = nil
	t.reasoningBlocks = nil
	t.toolBlocks = nil
	t.subagentBlocks = nil
	t.userScrolled = false
	t.refreshMessages()
}

// doClear clears the conversation and invokes the OnClear callback, if set.
func (t *TUI2) doClear() {
	if t.OnClear != nil {
		t.OnClear()
	}
	t.clearConversation()
}
