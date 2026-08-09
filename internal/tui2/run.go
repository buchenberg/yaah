package tui2

import (
	"time"
)

// Run starts the tview event loop.
func (t *TUI2) Run() error {
	t.startControlLoop()
	t.startDebounceTimer()
	t.App.SetFocus(t.Input)
	t.renderInfoPane()
	t.renderTodoPane()
	return t.App.Run()
}

// Stop gracefully shuts down the TUI.
func (t *TUI2) Stop() {
	t.App.Stop()
}

func (t *TUI2) startControlLoop() {
	go func() {
		for msg := range t.ControlCh {
			t.App.QueueUpdateDraw(func() {
				t.handleControlMsg(msg)
			})
		}
	}()
}

func (t *TUI2) startDebounceTimer() {
	go func() {
		for range time.Tick(100 * time.Millisecond) {
			t.App.QueueUpdateDraw(func() {
				if t.needsRefresh.Swap(false) {
					t.refreshMessages()
				}
			})
		}
	}()
}
