package tui

import "strings"

// AddUserMessage pins the submitted prompt as the sticky top line of
// the messages pane (prompt echo). The message is NOT appended to the
// conversation log — it stays visible at the top regardless of scroll
// position.
func (t *App) AddUserMessage(text string) {
	// Take the first line for the echo; multi-line prompts show the
	// opening line so the user sees what they submitted.
	echo := text
	if idx := strings.IndexByte(text, '\n'); idx >= 0 {
		echo = text[:idx] + "…"
	}
	t.promptEcho.SetText(t.Theme.Tag(t.Theme.User, "❯ ") + echo)
	t.App.SetFocus(t.Input)
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
