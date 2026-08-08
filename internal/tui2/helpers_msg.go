package tui2

// AddUserMessage appends a styled user message to the conversation.
func (t *TUI2) AddUserMessage(text string) {
	t.appendMessage(t.Theme.Tag(t.Theme.User, "You: ") + text + "\n")
}

// addAssistantResponse stores raw markdown in the conversation log.
// Rendering is lazy: done once on first display, cached by viewport width.
func (t *TUI2) addAssistantResponse(md string) {
	t.plainMessages = append(t.plainMessages, md)
	t.conversationLog = append(t.conversationLog, convItem{text: md, isMarkdown: true})
	t.refreshMessages()
	t.App.SetFocus(t.Input)
}

// appendMessage adds plain text to the conversation log and refreshes.
func (t *TUI2) appendMessage(text string) {
	t.plainMessages = append(t.plainMessages, text)
	t.conversationLog = append(t.conversationLog, convItem{text: text})
	t.refreshMessages()
	t.App.SetFocus(t.Input)
}
