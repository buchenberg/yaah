package tui

// activity_state.go — activity line state management.
//
// ShowThinking/HideThinking keep their exported names so
// cmd/yaah/tui.go needs no changes.

import (
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

// SetCurrentPrompt shows the user's prompt in the prompt echo area.
func (t *App) SetCurrentPrompt(text string) {
	t.promptEcho.SetText(t.Theme.Tag(t.Theme.User, text))
	t.resizePromptEcho(len(tview.WordWrap(text, t.echoWidth())))
	t.App.SetFocus(t.Input)
}
