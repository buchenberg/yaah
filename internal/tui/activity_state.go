package tui

// activity_state.go — activity line state management.
//
// ShowThinking/HideThinking keep their exported names so
// cmd/yaah/tui.go needs no changes.

import (
	"strings"

	"github.com/buchenberg/yaah/internal/tui/components/activity"
	"github.com/rivo/tview"
)

// setActivity transitions the activity line to a new state and updates
// the agentActive flag. Must be called inside QueueUpdateDraw.
func (t *App) setActivity(s activity.State, detail string) {
	t.activityLine.SetState(s, detail)
	busy := s != activity.Idle
	t.activityBusy.Store(busy)
	t.agentActive = busy
	t.renderInfoPane()
}

// restoreActivity pops the activity line's restore stack.
func (t *App) restoreActivity() {
	t.activityLine.Restore()
	t.activityBusy.Store(t.activityLine.Busy())
	t.agentActive = t.activityLine.Busy()
	t.renderInfoPane()
}

// ShowThinking shows the animated thinking indicator.
func (t *App) ShowThinking() {
	t.setActivity(activity.Thinking, "")
}

// HideThinking hides the animated thinking indicator.
func (t *App) HideThinking() {
	t.setActivity(activity.Idle, "")
}

// ActivityState returns the current activity state (test hook).
func (t *App) ActivityState() activity.State {
	return t.activityLine.State()
}

// SetCurrentPrompt shows the user's prompt in the sticky header echo,
// pinned as the top line of the messages pane. Called by
// reconcilePromptPin when the scroll-derived pin changes; it no longer
// runs on submit (AddPrompt renders the prompt in the stream instead).
// The prompt is prefixed with a marker glyph and wraps to at most
// promptEchoMaxLines rows; a longer prompt is truncated with an
// ellipsis. An empty prompt hides the echo entirely.
//
// Draw-safe: it must not touch app-level state (no SetFocus, no
// redraw requests) because reconcilePromptPin runs from the AfterDraw
// hook while tview holds the application lock — taking that lock again
// deadlocks the UI. Widget mutations (SetText, flex ResizeItem) are
// plain data writes and become visible on the next frame.
func (t *App) SetCurrentPrompt(text string) {
	if strings.TrimSpace(text) == "" {
		t.promptEcho.SetText("")
		t.messagesCol.ResizeItem(t.promptEcho, 0, 0)
		return
	}
	full := promptDisplay(text)

	// Literal brackets in user input must not parse as tview color
	// directives (the echo is dynamic-colors enabled).
	full = tview.Escape(full)

	w := t.echoWidth()
	lines := tview.WordWrap(full, w)
	if len(lines) > promptEchoMaxLines {
		full = t.truncateEcho(promptEchoPrefix, tview.Escape(strings.ReplaceAll(text, "\n\n", " ")), w)
	}

	t.promptEcho.SetText(t.Theme.Tag(t.Theme.User, full))
	t.resizePromptEcho(len(tview.WordWrap(full, w)))
}
