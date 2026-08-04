package tui2

import "github.com/buchenberg/yaah/internal/tui2/colors"

// addUserMessage appends a styled user message to the conversation.
func (t *TUI2) addUserMessage(text string) {
	t.appendMessage(colors.Accent + "You: " + colors.Reset + text + "\n")
}

// addAssistantResponse appends an assistant response to the conversation.
func (t *TUI2) addAssistantResponse(text string) {
	t.appendMessage("[#00d787]Yaah: " + colors.Reset + text + "\n")
}

// appendMessage adds raw text to the plain messages and refreshes.
func (t *TUI2) appendMessage(text string) {
	t.plainMessages = append(t.plainMessages, text)
	t.refreshMessages()
	t.App.SetFocus(t.Input)
}
