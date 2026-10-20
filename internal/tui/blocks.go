package tui

import (
	"github.com/buchenberg/yaah/internal/tui/components/reasoning"
	"github.com/buchenberg/yaah/internal/tui/components/subagent"
	"github.com/buchenberg/yaah/internal/tui/components/toolblock"
)

// blocks.go — block operations (reasoning, tool, sub-agent).
// All blocks are now stored inline in conversationLog via convItem.

// AddReasoningBlock creates a reasoning block and adds it to conversationLog.
func (t *App) AddReasoningBlock(id, content string) {
	rb := reasoning.New(id, content, 0, t.Theme)
	t.conversationLog = append(t.conversationLog, convItem{
		reasoningBlock: rb,
	})
	t.markDirty()
}

// AddToolError finds a tool block in conversationLog by ID and transitions it to Error.
func (t *App) AddToolError(id, summary, err string) {
	for i := range t.conversationLog {
		ci := &t.conversationLog[i]
		if ci.toolBlock != nil && ci.toolBlock.ID() == id {
			ci.toolBlock.Fail(summary, err)
			t.needsFullRender.Store(true)
			t.markDirty()
			return
		}
	}
}

// AddToolStart adds a tool block to conversationLog.
// Note: flushes any pending streaming tokens first.
func (t *App) AddToolStart(id, name, args string) {
	t.flushPendingTokens()
	tb := toolblock.New(id, name, args, t.Theme)
	t.conversationLog = append(t.conversationLog, convItem{
		toolBlock: tb,
	})
	t.markDirty()
}

// AddToolEnd updates a tool block in conversationLog with result.
// The caller (proxy.go) is responsible for routing errors via AddToolError.
// Tools may legitimately return empty results (e.g., successful delete with no output).
func (t *App) AddToolEnd(id, summary, result string) {
	for i := range t.conversationLog {
		ci := &t.conversationLog[i]
		if ci.toolBlock != nil && ci.toolBlock.ID() == id {
			ci.toolBlock.Complete(summary, result)
			t.needsFullRender.Store(true)
			t.markDirty()
			return
		}
	}
}

// AddSubAgentStart adds a sub-agent to the dedicated Subagents pane.
// Sub-agents are not part of the conversation log; the pane is their only
// display surface.
func (t *App) AddSubAgentStart(id, role, specialty, task, model string) {
	sb := subagent.New(id, role, specialty, task, model, t.Theme)
	t.subAgents = append(t.subAgents, sb)
	t.subAgentsBusy.Store(true)
	t.renderBackgroundJobsPane()
}

// AddSubAgentEnd marks a sub-agent as completed and refreshes the pane.
func (t *App) AddSubAgentEnd(id string) {
	if sb := t.findSubAgent(id); sb != nil {
		sb.Complete()
		t.syncSubAgentBusy()
		t.renderBackgroundJobsPane()
	}
}

// AddSubAgentError marks a sub-agent as failed and refreshes the pane.
func (t *App) AddSubAgentError(id, err string) {
	if sb := t.findSubAgent(id); sb != nil {
		sb.Fail(err)
		t.syncSubAgentBusy()
		t.renderBackgroundJobsPane()
	}
}

// findSubAgent returns the sub-agent block with the given id, if any.
func (t *App) findSubAgent(id string) *subagent.Block {
	for _, sb := range t.subAgents {
		if sb.ID() == id {
			return sb
		}
	}
	return nil
}

// syncSubAgentBusy updates the atomic flag that keeps the animation ticker
// alive while sub-agents run, including background dispatches that outlive
// the turn's activity-line state.
func (t *App) syncSubAgentBusy() {
	for _, sb := range t.subAgents {
		if sb.S() == subagent.Active {
			t.subAgentsBusy.Store(true)
			return
		}
	}
	t.subAgentsBusy.Store(false)
}

// advanceSubAgentSpinners advances every active sub-agent's spinner and
// repaints the pane. Driven by the activity ticker while agents are running.
func (t *App) advanceSubAgentSpinners() {
	advanced := false
	for _, sb := range t.subAgents {
		if sb.S() == subagent.Active {
			sb.AdvanceSpinner()
			advanced = true
		}
	}
	if advanced {
		t.renderBackgroundJobsPane()
	}
}

// ToggleBlockByIndex finds the nth block (of any type) and toggles it.
func (t *App) ToggleBlockByIndex(n int) {
	type block interface {
		Toggle()
		IsExpanded() bool
	}

	blocks := []block{}
	for _, ci := range t.conversationLog {
		if ci.reasoningBlock != nil {
			blocks = append(blocks, ci.reasoningBlock)
		}
		if ci.toolBlock != nil {
			blocks = append(blocks, ci.toolBlock)
		}
	}

	if n >= 0 && n < len(blocks) {
		blocks[n].Toggle()
		t.needsFullRender.Store(true)
		t.refreshMessages()
	}
}

// CollapseAll collapses all expandable blocks.
func (t *App) CollapseAll() {
	t.needsFullRender.Store(true)
	for _, ci := range t.conversationLog {
		if ci.reasoningBlock != nil {
			for ci.reasoningBlock.IsExpanded() {
				ci.reasoningBlock.Toggle()
			}
		}
		if ci.toolBlock != nil {
			for ci.toolBlock.IsExpanded() {
				ci.toolBlock.Toggle()
			}
		}
	}
	t.refreshMessages()
}

func (t *App) toggleAllReasoning() {
	t.needsFullRender.Store(true)
	for _, ci := range t.conversationLog {
		if ci.reasoningBlock != nil {
			ci.reasoningBlock.Toggle()
		}
	}
	t.refreshMessages()
}

func (t *App) toggleAllTools() {
	t.needsFullRender.Store(true)
	for _, ci := range t.conversationLog {
		if ci.toolBlock != nil {
			ci.toolBlock.Toggle()
		}
	}
	t.refreshMessages()
}
