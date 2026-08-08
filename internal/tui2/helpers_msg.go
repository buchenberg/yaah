package tui2

// AddUserMessage appends a styled user message to the conversation.
func (t *TUI2) AddUserMessage(text string) {
	t.appendMessage(t.Theme.Tag(t.Theme.User, "You: ") + text + "\n")
}

// addAssistantResponse renders markdown once and stores the result.
// The rendered text is reused on every refresh — no re-rendering needed.
func (t *TUI2) addAssistantResponse(md string) {
	t.appendMessage(renderMarkdown(md))
}

// appendMessage adds plain text to the conversation log and refreshes.
func (t *TUI2) appendMessage(text string) {
	t.plainMessages = append(t.plainMessages, text)
	t.conversationLog = append(t.conversationLog, convItem{text: text})
	t.refreshMessages()
	t.App.SetFocus(t.Input)
}
