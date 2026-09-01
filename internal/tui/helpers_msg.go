package tui

import (
	"strings"

	"github.com/rivo/tview"
)

// Prompt echo / prompt input growth limits (text lines, excluding the
// input's border rows).
const (
	promptEchoMaxLines   = 3
	promptMaxContentLine = 3
)

// truncateEcho shortens the echo body until prefix+body+"…" wraps into
// promptEchoMaxLines rows at width w.
func (t *App) truncateEcho(prefix, body string, w int) string {
	runes := []rune(body)
	for i := len(runes) - 1; i > 0; i-- {
		candidate := prefix + string(runes[:i]) + "…"
		if len(tview.WordWrap(candidate, w)) <= promptEchoMaxLines {
			return candidate
		}
	}
	return prefix + "…"
}

// echoWidth returns the wrapping width for the prompt echo: its current
// inner rect when laid out, otherwise the messages column width, else a
// sane default.
func (t *App) echoWidth() int {
	if _, _, w, _ := t.promptEcho.GetRect(); w > 4 {
		return w - 1
	}
	if _, _, w, _ := t.messagesCol.GetRect(); w > 4 {
		return w - 3
	}
	return 78
}

// resizePromptEcho grows/shrinks the echo row to fit its wrapped line
// count, capped at promptEchoMaxLines.
func (t *App) resizePromptEcho(lines int) {
	rows := lines
	if rows < 1 {
		rows = 1
	}
	if rows > promptEchoMaxLines {
		rows = promptEchoMaxLines
	}
	t.messagesCol.ResizeItem(t.promptEcho, rows, 0)
}

// growPromptInput resizes the prompt row to fit the TextArea's visual
// content — explicit newlines plus soft-wrapped long lines — up to
// promptMaxContentLine content rows; beyond that the TextArea scrolls
// internally. Runs on the main goroutine via SetChangedFunc.
func (t *App) growPromptInput() {
	n := t.promptVisualLines()
	rows := n + 2 // border
	maxRows := promptMaxContentLine + 2
	if rows < 3 {
		rows = 3
	}
	if rows > maxRows {
		rows = maxRows
	}
	t.Root.ResizeItem(t.prompt, rows, 0)
}

// promptVisualLines counts how many rows the TextArea content occupies
// once soft-wrapped to the field's inner width.
func (t *App) promptVisualLines() int {
	_, _, w, _ := t.Input.GetInnerRect()
	if w <= 4 {
		w = 76 // pre-layout fallback; border subtracts 2 from ~80 cols
	}
	total := 0
	for _, ln := range strings.Split(t.Input.GetText(), "\n") {
		total += len(tview.WordWrap(ln, w))
	}
	if total < 1 {
		total = 1
	}
	return total
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
