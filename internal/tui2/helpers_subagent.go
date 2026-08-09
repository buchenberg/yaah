package tui2

import "github.com/buchenberg/yaah/internal/tui2/components/subagent"

// AddSubAgentStart creates a new sub-agent block in Active state and
// appends it to the conversation log at the current position.
func (t *TUI2) AddSubAgentStart(id, agentType, specialty, task, model string) {
	t.flushPendingTokens()
	block := subagent.New(id, agentType, specialty, task, model, t.Theme)
	t.subagentBlocks = append(t.subagentBlocks, block)
	t.conversationLog = append(t.conversationLog, convItem{subBlock: block})
	t.refreshMessages()
	t.renderBackgroundJobsPane()
}

// AddSubAgentEnd transitions the sub-agent block with the given id to Done.
func (t *TUI2) AddSubAgentEnd(id string) {
	for _, b := range t.subagentBlocks {
		if b.ID() == id {
			b.Complete()
			t.refreshMessages()
			t.renderBackgroundJobsPane()
			return
		}
	}
}

// AddSubAgentError transitions the sub-agent block with the given id to Error.
func (t *TUI2) AddSubAgentError(id, err string) {
	for _, b := range t.subagentBlocks {
		if b.ID() == id {
			b.Fail(err)
			t.refreshMessages()
			t.renderBackgroundJobsPane()
			return
		}
	}
}
