package tui2

import (
	"github.com/buchenberg/yaah/internal/tui2/components/messages"
)

// markDirty sets the refresh flag. Call instead of refreshMessages()
// to coalesce multiple rapid updates into a single render pass.
func (t *TUI2) markDirty() {
	t.needsRefresh.Store(true)
}

// flushRefresh performs the actual render if dirty, then clears the flag.
func (t *TUI2) flushRefresh() {
	if t.needsRefresh.Swap(false) {
		t.refreshMessages()
	}
}

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

	if t.thinkingInd.Visible() {
		msg += "\n  " + t.thinkingInd.Render()
	}
	t.charsRendered.Store(int64(len(msg)))
	t.Messages.SetText(msg)
	if !t.userScrolled {
		t.Messages.ScrollToEnd()
	}
}
