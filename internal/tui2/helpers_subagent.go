package tui2

import "github.com/buchenberg/yaah/internal/tui2/components/subagent"

// AddSubAgentStart logs the start of a sub-agent dispatch.
func (t *TUI2) AddSubAgentStart(agentType, description string) {
	t.appendMessage(subagent.Start(agentType, description))
}

// AddSubAgentEnd logs the completion of a sub-agent task.
func (t *TUI2) AddSubAgentEnd(agentType, description, result string) {
	t.appendMessage(subagent.End(agentType, description, result))
}
