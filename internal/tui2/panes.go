package tui2

import (
	"time"

	"github.com/buchenberg/yaah/internal/todo"
)

// Panes — right-pane update methods.

// UpdateTodos updates the TODO list in the right panel.
func (t *TUI2) UpdateTodos(items []todo.Item) {
	t.todoItems = items
	t.renderTodoPane()
}

// UpdateInfopane sets a specific infopane tab content.
func (t *TUI2) UpdateInfopane(tab, content string) {
	t.InfoPane.SetText(content)
}

// SetEphemeral shows a transient status message in the info pane for 3
// seconds, then clears it.
func (t *TUI2) SetEphemeral(msg string) {
	t.ephemeralMsg = msg
	t.renderInfoPane()
	go func() {
		time.Sleep(3 * time.Second)
		t.App.QueueUpdateDraw(func() {
			t.ephemeralMsg = ""
			t.renderInfoPane()
		})
	}()
}
