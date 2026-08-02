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

// appendMessage adds raw text to the conversation view and auto-scrolls.
func (t *TUI2) appendMessage(text string) {
	_, _ = t.Messages.Write([]byte(text))
	t.Messages.ScrollToEnd()

	// Keep the input focused after appending
	t.App.SetFocus(t.Input)
}

// clearMessages empties the conversation area.
func (t *TUI2) clearMessages() {
	t.Messages.SetText("")
}
