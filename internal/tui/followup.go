package tui

// submitInput sends the current input text as a new prompt, if any.
//
// Text starting with ":" is dispatched as a colon command instead, so ":steer
// keep it short" reaches the stepper rather than the model.
func (t *App) submitInput() {
	text := t.Input.GetText()
	if text == "" {
		return
	}
	if t.handleColonInput(text) {
		return
	}
	if t.OnSubmit != nil {
		t.flushRefresh()
		t.Input.SetText("", false)
		t.OnSubmit(text)
	}
}

// submitFollowUp sends the current input text as a follow-up, if any. Colon
// commands are honoured here too: ":" must not surprise the user just because a
// turn is running.
func (t *App) submitFollowUp() {
	text := t.Input.GetText()
	if text == "" {
		return
	}
	if t.handleColonInput(text) {
		return
	}
	if t.OnFollowUp != nil {
		t.flushRefresh()
		t.Input.SetText("", false)
		t.OnFollowUp(text)
	}
}

// clearConversation resets all conversation state and re-renders the view.
func (t *App) clearConversation() {
	t.conversationLog = nil
	t.subAgents = nil
	t.subAgentsBusy.Store(false)
	t.userScrolled = false
	t.resetUsage()
	t.renderBackgroundJobsPane()
	t.refreshMessages()
}

// doClear clears the conversation and invokes the OnClear callback, if set.
func (t *App) doClear() {
	if t.OnClear != nil {
		t.OnClear()
	}
	t.clearConversation()
}
