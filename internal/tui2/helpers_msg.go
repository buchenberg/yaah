package tui2

// AddUserMessage appends a styled user message to the conversation.
func (t *TUI2) AddUserMessage(text string) {
	t.appendMessage(t.Theme.Tag(t.Theme.User, "You: ") + text + "\n")
}

// addAssistantResponse appends raw markdown to the conversation log.
// Rendering happens later in refreshMessages() at the current viewport width.
func (t *TUI2) addAssistantResponse(md string) {
	t.appendMarkdown(md)
}

// appendMessage adds plain text to the conversation log and refreshes.
func (t *TUI2) appendMessage(text string) {
	t.plainMessages = append(t.plainMessages, text)
	t.conversationLog = append(t.conversationLog, convItem{text: text})
	t.refreshMessages()
	t.App.SetFocus(t.Input)
}

// appendMarkdown adds raw markdown to the conversation log and refreshes.
// The markdown is rendered during refreshMessages() at the current width.
func (t *TUI2) appendMarkdown(md string) {
	t.plainMessages = append(t.plainMessages, md)
	t.conversationLog = append(t.conversationLog, convItem{rawMarkdown: md})
	t.refreshMessages()
	t.App.SetFocus(t.Input)
}
