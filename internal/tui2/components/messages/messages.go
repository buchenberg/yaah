// Package messages builds the conversation message area.
package messages

import (
	"github.com/rivo/tview"

	"github.com/buchenberg/yaah/internal/tui2/components/messages/assistant"
	"github.com/buchenberg/yaah/internal/tui2/components/messages/error"
	"github.com/buchenberg/yaah/internal/tui2/components/messages/subagent"
	"github.com/buchenberg/yaah/internal/tui2/components/messages/system"
	"github.com/buchenberg/yaah/internal/tui2/components/messages/tool"
	"github.com/buchenberg/yaah/internal/tui2/components/messages/user"
)

// Build creates the scrollable conversation view.
func Build() *tview.TextView {
	tv := tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetWrap(true).
		SetWordWrap(true)
	return tv
}

// AppendUser appends a styled user message to the TextView and scrolls.
func AppendUser(tv *tview.TextView, text string) {
	tv.SetText(tv.GetText(true) + user.Render(text))
	tv.ScrollToEnd()
}

// AppendAssistant appends a styled assistant message to the TextView and scrolls.
func AppendAssistant(tv *tview.TextView, text string) {
	tv.SetText(tv.GetText(true) + assistant.Render(text))
	tv.ScrollToEnd()
}

// AppendToolStart appends a tool start message to the TextView and scrolls.
func AppendToolStart(tv *tview.TextView, name, args string) {
	tv.SetText(tv.GetText(true) + tool.Render(name+" "+args))
	tv.ScrollToEnd()
}

// AppendToolEnd appends a tool end message to the TextView and scrolls.
func AppendToolEnd(tv *tview.TextView, name string) {
	tv.SetText(tv.GetText(true) + tool.Render(name+" done"))
	tv.ScrollToEnd()
}

// AppendToolSummary appends a tool summary message to the TextView and scrolls.
func AppendToolSummary(tv *tview.TextView, name, summary string) {
	tv.SetText(tv.GetText(true) + tool.Render(name+": "+summary))
	tv.ScrollToEnd()
}

// AppendError appends an error message to the TextView and scrolls.
func AppendError(tv *tview.TextView, text string) {
	tv.SetText(tv.GetText(true) + error.Render(text))
	tv.ScrollToEnd()
}

// AppendSystem appends a system message to the TextView and scrolls.
func AppendSystem(tv *tview.TextView, text string) {
	tv.SetText(tv.GetText(true) + system.Render(text))
	tv.ScrollToEnd()
}

// AppendSubAgentStart appends a sub-agent start message to the TextView and scrolls.
func AppendSubAgentStart(tv *tview.TextView, text string) {
	tv.SetText(tv.GetText(true) + subagent.Render(text))
	tv.ScrollToEnd()
}

// AppendSubAgentEnd appends a sub-agent end message to the TextView and scrolls.
func AppendSubAgentEnd(tv *tview.TextView, text string) {
	tv.SetText(tv.GetText(true) + subagent.Render(text))
	tv.ScrollToEnd()
}
