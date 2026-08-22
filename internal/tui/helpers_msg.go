package tui

// AddUserMessage appends a styled user message to the conversation.
func (t *App) AddUserMessage(text string) {
	t.appendMessage(t.Theme.Tag(t.Theme.User, "You: ") + text + "\n")
}

// addAssistantResponse stores raw markdown in the conversation log.
// Markdown is rendered at display time via renderMarkdown, which produces
// valid tview tags — no raw brackets survive to confuse SetDynamicColors.
func (t *App) addAssistantResponse(md string) {
	t.conversationLog = append(t.conversationLog, convItem{text: md, isMarkdown: true})
	t.markDirty()
	t.App.SetFocus(t.Input)
}

// appendMessage adds plain text to the conversation log and refreshes.
func (t *App) appendMessage(text string) {
	t.conversationLog = append(t.conversationLog, convItem{text: text})
	t.markDirty()
	t.App.SetFocus(t.Input)
}
