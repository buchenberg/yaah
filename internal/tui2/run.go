package tui2

import (
	"time"

	"github.com/buchenberg/yaah/internal/tui2/components/subagent"
)

// Run starts the tview event loop.
func (t *TUI2) Run() error {
	t.startControlLoop()
	t.startSpinnerTicker()
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

func (t *TUI2) startSpinnerTicker() {
	go func() {
		for range time.Tick(200 * time.Millisecond) {
			t.App.QueueUpdateDraw(func() {
				anyActive := t.thinkingInd.Visible() || t.isStreaming.Load()
				if !anyActive {
					for _, sb := range t.subagentBlocks {
						if sb.S() == subagent.Active {
							anyActive = true
							break
						}
					}
				}
				if !anyActive {
					return
				}
				if t.thinkingInd.Visible() {
					t.thinkingInd.Advance()
				}
				for _, sb := range t.subagentBlocks {
					sb.AdvanceSpinner()
				}
				t.renderInfoPane()
				if t.thinkingInd.Visible() && t.conversationCache != "" {
					t.Messages.SetText(t.conversationCache + "\n  " + t.thinkingInd.Render())
				}
			})
		}
	}()
}
