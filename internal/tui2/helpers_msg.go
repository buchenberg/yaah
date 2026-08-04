package tui2

import "github.com/buchenberg/yaah/internal/tui2/colors"

// AddUserMessage appends a styled user message to the conversation.
func (t *TUI2) AddUserMessage(text string) {
	t.appendMessage(colors.Tag(colors.Accent, "You: ") + text + "\n")
}

// addAssistantResponse appends a markdown-rendered assistant response.
func (t *TUI2) addAssistantResponse(text string) {
	t.appendMessage(renderMarkdown(text))
}

// appendMessage adds raw text to the conversation log and refreshes.
// A blank line is added before each message for visual spacing.
func (t *TUI2) appendMessage(text string) {
	t.plainMessages = append(t.plainMessages, text)
	t.conversationLog = append(t.conversationLog, convItem{text: text})
	t.refreshMessages()
	t.App.SetFocus(t.Input)
}
