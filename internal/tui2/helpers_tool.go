package tui2

import "github.com/buchenberg/yaah/internal/tui2/components/toolblock"

// AddToolStart creates a tool block in Running state and appends it to
// the conversation.
func (t *TUI2) AddToolStart(id, name, args string) {
	tb := toolblock.New(id, name, args)
	t.toolBlocks = append(t.toolBlocks, tb)
	t.conversation.AppendTool(tb)
	t.refreshMessages()
}

// AddToolEnd transitions a tool block to Done.
func (t *TUI2) AddToolEnd(id, summary, result string) {
	for _, tb := range t.toolBlocks {
		if tb.ID() == id {
			tb.Complete(summary, result)
			t.refreshMessages()
			return
		}
	}
}
