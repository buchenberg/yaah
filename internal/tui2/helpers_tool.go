package tui2

import "github.com/buchenberg/yaah/internal/tui2/components/tool"

// AddToolStart logs the start of a tool execution to the conversation.
func (t *TUI2) AddToolStart(name, args string) {
	t.appendMessage(tool.Start(name, args))
}

// AddToolEnd logs the completion of a tool execution.
func (t *TUI2) AddToolEnd(name, result string) {
	t.appendMessage(tool.End(name, result))
}
