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
			t.markDirty()
			return
		}
	}
}

// AddSubAgentStart adds a sub-agent start entry to conversationLog.
// Note: flushes any pending streaming tokens first and triggers background jobs pane update.
func (t *App) AddSubAgentStart(id, role, specialty, task, model string) {
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
func (t *App) AddSubAgentEnd(id string) {
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
func (t *App) AddSubAgentError(id, err string) {
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
func (t *App) CollapseAll() {
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

func (t *App) toggleAllReasoning() {
	for _, ci := range t.conversationLog {
		if ci.reasoningBlock != nil {
			ci.reasoningBlock.Toggle()
		}
	}
	t.refreshMessages()
}

func (t *App) toggleAllTools() {
	for _, ci := range t.conversationLog {
		if ci.toolBlock != nil {
			ci.toolBlock.Toggle()
		}
	}
	t.refreshMessages()
}

func (t *App) toggleAllSubAgents() {
	for _, ci := range t.conversationLog {
		if ci.subBlock != nil {
			ci.subBlock.Toggle()
		}
	}
	t.refreshMessages()
}
