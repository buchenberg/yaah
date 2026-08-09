package tui2

import (
	"github.com/buchenberg/yaah/internal/tui2/components/messages"
)

// scroll.go — scroll-preserving render logic extracted from tui2.go.
//
// The conversation text view (t.Messages) is re-rendered by refreshMessages,
// which preserves the user's scroll position (t.userScrolled, defined on
// TUI2 in tui2.go) and only auto-scrolls to the end when the user has not
// scrolled up. Scroll position itself is tracked by tview on the Messages
// widget; callers use ScrollTo / ScrollToEnd / ScrollTo(line, col).
//
// NOTE: t.userScrolled is defined on the TUI2 struct in tui2.go and is not
// moved here — moving struct fields would require editing tui2.go.

// refreshMessages re-renders the conversation text view from the current
// conversation log. If the user has not scrolled up (t.userScrolled is
// false), it auto-scrolls to the end; otherwise it preserves the current
// viewport position.
func (t *TUI2) refreshMessages() {
	w := messageWidth(t.Messages)

	items := make([]messages.Item, len(t.conversationLog))
	for i := range t.conversationLog {
		ci := &t.conversationLog[i]
		var text string
		if ci.text != "" {
			if ci.isMarkdown {
				if ci.cached == "" || ci.cachedWidth != w {
					ci.cached = renderMarkdown(ci.text, w, t.Theme)
					ci.cachedWidth = w
				}
				text = ci.cached
			} else {
				text = ci.text
			}
		}
		items[i] = messages.Item{
			Text:      text,
			ToolBlock: ci.toolBlock,
			SubBlock:  ci.subBlock,
			ReasBlock: ci.reasoningBlock,
		}
	}

	msg := messages.Format(items, "", messages.Content{
		Width: w,
		Theme: t.Theme,
	})
	t.conversationCache = msg

	if t.thinkingInd.Visible() {
		msg += "\n  " + t.thinkingInd.Render()
	}
	t.charsRendered.Store(int64(len(msg)))
	t.Messages.SetText(msg)
	if !t.userScrolled {
		t.Messages.ScrollToEnd()
	}
}
