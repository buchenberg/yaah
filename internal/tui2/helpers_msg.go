package tui2

import "strings"

// AddUserMessage appends a styled user message to the conversation.
func (t *TUI2) AddUserMessage(text string) {
	t.appendMessage(t.Theme.Tag(t.Theme.User, "You: ") + text + "\n")
}

// addAssistantResponse stores raw markdown in the conversation log.
// Markdown is rendered at display time via renderMarkdown, which produces
// valid tview tags — no raw brackets survive to confuse SetDynamicColors.
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

// escapeBrackets escapes literal '[' for tview SetDynamicColors.
// Raw text with [...] patterns (links, code refs) gets consumed as
// color directives. Streaming tokens call this before Write().
func escapeBrackets(s string) string {
	return strings.ReplaceAll(s, "[", "[[]")
}
