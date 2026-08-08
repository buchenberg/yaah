package tui2

import "strings"

// AddUserMessage appends a styled user message to the conversation.
func (t *TUI2) AddUserMessage(text string) {
	t.appendMessage(t.Theme.Tag(t.Theme.User, "You: ") + text + "\n")
}

// addAssistantResponse stores raw markdown in the conversation log.
// Bracket characters are escaped so tview's SetDynamicColors parser
// doesn't consume markdown link syntax or inline code as directives.
func (t *TUI2) addAssistantResponse(md string) {
	t.plainMessages = append(t.plainMessages, md)
	escaped := escapeBrackets(md)
	t.conversationLog = append(t.conversationLog, convItem{text: escaped, isMarkdown: true})
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

// escapeBrackets escapes literal '[' characters for tview's TextView
// with SetDynamicColors(true). Without this, any text containing
// "[...]" patterns (markdown links, code references, bracketed terms)
// gets silently consumed as invalid color/style directives.
func escapeBrackets(s string) string {
	return strings.ReplaceAll(s, "[", "[[]")
}
