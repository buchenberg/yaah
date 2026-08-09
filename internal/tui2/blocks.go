package tui2

import (
	"github.com/buchenberg/yaah/internal/tui2/components/reasoning"
	"github.com/buchenberg/yaah/internal/tui2/components/subagent"
	"github.com/buchenberg/yaah/internal/tui2/components/toolblock"
)

// blocks.go — block operations (reasoning, tool, sub-agent).
// All blocks are now stored inline in conversationLog via convItem.

// AddReasoningBlock creates a reasoning block and adds it to conversationLog.
func (t *TUI2) AddReasoningBlock(id, content string) {
	rb := reasoning.New(id, content, 0, t.Theme)
	t.conversationLog = append(t.conversationLog, convItem{
		reasoningBlock: rb,
	})
	t.markDirty()
}

// AddToolError finds a tool block in conversationLog by ID and transitions it to Error.
func (t *TUI2) AddToolError(id, summary, err string) {
	for i := range t.conversationLog {
		ci := &t.conversationLog[i]
		if ci.toolBlock != nil && ci.toolBlock.ID() == id {
			ci.toolBlock.Fail(summary, err)
			t.markDirty()
			return
		}
	}
}

// AddToolStart adds a tool block to conversationLog.
// Note: flushes any pending streaming tokens first.
func (t *TUI2) AddToolStart(id, name, args string) {
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
func (t *TUI2) AddToolEnd(id, summary, result string) {
	for i := range t.conversationLog {
		ci := &t.conversationLog[i]
		if ci.toolBlock != nil && ci.toolBlock.ID() == id {
			ci.toolBlock.Complete(summary, result)
			t.markDirty()
			return
		}
	}
}

// AddSubAgentStart adds a sub-agent start entry to conversationLog.
// Note: flushes any pending streaming tokens first and triggers background jobs pane update.
func (t *TUI2) AddSubAgentStart(id, role, specialty, task, model string) {
	t.flushPendingTokens()
	sb := subagent.New(id, role, specialty, task, model, t.Theme)
	t.conversationLog = append(t.conversationLog, convItem{
		subBlock: sb,
	})
	t.markDirty()
	t.renderBackgroundJobsPane()
}

// AddSubAgentEnd marks a sub-agent block as completed.
// Note: triggers background jobs pane update.
func (t *TUI2) AddSubAgentEnd(id string) {
	for i := range t.conversationLog {
		ci := &t.conversationLog[i]
		if ci.subBlock != nil && ci.subBlock.ID() == id {
			ci.subBlock.Complete()
			t.markDirty()
			t.renderBackgroundJobsPane()
			return
		}
	}
}

// AddSubAgentError marks a sub-agent block as failed.
// Note: triggers background jobs pane update.
func (t *TUI2) AddSubAgentError(id, err string) {
	for i := range t.conversationLog {
		ci := &t.conversationLog[i]
		if ci.subBlock != nil && ci.subBlock.ID() == id {
			ci.subBlock.Fail(err)
			t.markDirty()
			t.renderBackgroundJobsPane()
			return
		}
	}
}

// ToggleBlockByIndex finds the nth block (of any type) and toggles it.
func (t *TUI2) ToggleBlockByIndex(n int) {
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
		if ci.subBlock != nil {
			blocks = append(blocks, ci.subBlock)
		}
	}

	if n >= 0 && n < len(blocks) {
		blocks[n].Toggle()
		t.refreshMessages()
	}
}

// CollapseAll collapses all expandable blocks.
func (t *TUI2) CollapseAll() {
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
		if ci.subBlock != nil {
			for ci.subBlock.IsExpanded() {
				ci.subBlock.Toggle()
			}
		}
	}
	t.refreshMessages()
}

func (t *TUI2) toggleAllReasoning() {
	for _, ci := range t.conversationLog {
		if ci.reasoningBlock != nil {
			ci.reasoningBlock.Toggle()
		}
	}
	t.refreshMessages()
}

func (t *TUI2) toggleAllTools() {
	for _, ci := range t.conversationLog {
		if ci.toolBlock != nil {
			ci.toolBlock.Toggle()
		}
	}
	t.refreshMessages()
}

func (t *TUI2) toggleAllSubAgents() {
	for _, ci := range t.conversationLog {
		if ci.subBlock != nil {
			ci.subBlock.Toggle()
		}
	}
	t.refreshMessages()
}

// BlinkSubAgents toggles blink visibility for all active sub-agent blocks.
func (t *TUI2) BlinkSubAgents() {
	needsRefresh := false
	for _, ci := range t.conversationLog {
		if ci.subBlock != nil && ci.subBlock.S() == subagent.Active {
			ci.subBlock.ToggleBlink()
			needsRefresh = true
		}
	}
	if needsRefresh {
		t.markDirty()
	}
}

// AdvanceReasoningSeeds advances lolcat seeds for all reasoning blocks.
func (t *TUI2) AdvanceReasoningSeeds(seed float64) {
	for _, ci := range t.conversationLog {
		if ci.reasoningBlock != nil {
			ci.reasoningBlock.SetSeed(seed)
		}
	}
	t.markDirty()
}
