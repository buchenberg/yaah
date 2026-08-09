package tui2

import (
	"github.com/buchenberg/yaah/internal/tui2/components/reasoning"
	"github.com/buchenberg/yaah/internal/tui2/components/subagent"
)

// blocks.go — block operations (reasoning, tool, sub-agent).

// AddReasoningBlock creates a reasoning block.
func (t *TUI2) AddReasoningBlock(id, content string) {
	rb := reasoning.New(id, content, 0, t.Theme)
	t.reasoningBlocks = append(t.reasoningBlocks, rb)
	t.refreshMessages()
}

// AddToolError transitions a tool block to Error.
func (t *TUI2) AddToolError(id, summary, err string) {
	for _, tb := range t.toolBlocks {
		if tb.ID() == id {
			tb.Fail(summary, err)
			t.refreshMessages()
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
	for _, rb := range t.reasoningBlocks {
		blocks = append(blocks, rb)
	}
	for _, tb := range t.toolBlocks {
		blocks = append(blocks, tb)
	}
	for _, sb := range t.subagentBlocks {
		blocks = append(blocks, sb)
	}

	if n >= 0 && n < len(blocks) {
		blocks[n].Toggle()
		t.refreshMessages()
	}
}

// CollapseAll collapses all expandable blocks.
func (t *TUI2) CollapseAll() {
	for _, rb := range t.reasoningBlocks {
		for rb.IsExpanded() {
			rb.Toggle()
		}
	}
	for _, tb := range t.toolBlocks {
		for tb.IsExpanded() {
			tb.Toggle()
		}
	}
	for _, sb := range t.subagentBlocks {
		for sb.IsExpanded() {
			sb.Toggle()
		}
	}
	t.refreshMessages()
}

func (t *TUI2) toggleAllReasoning() {
	for _, rb := range t.reasoningBlocks {
		rb.Toggle()
	}
	t.refreshMessages()
}

func (t *TUI2) toggleAllTools() {
	for _, tb := range t.toolBlocks {
		tb.Toggle()
	}
	t.refreshMessages()
}

func (t *TUI2) toggleAllSubAgents() {
	for _, sb := range t.subagentBlocks {
		sb.Toggle()
	}
	t.refreshMessages()
}

// BlinkSubAgents toggles blink visibility for all active sub-agent blocks.
func (t *TUI2) BlinkSubAgents() {
	needsRefresh := false
	for _, sb := range t.subagentBlocks {
		if sb.S() == subagent.Active {
			sb.ToggleBlink()
			needsRefresh = true
		}
	}
	if needsRefresh {
		t.refreshMessages()
	}
}

// AdvanceReasoningSeeds advances lolcat seeds for all reasoning blocks.
func (t *TUI2) AdvanceReasoningSeeds(seed float64) {
	for _, rb := range t.reasoningBlocks {
		rb.SetSeed(seed)
	}
	t.refreshMessages()
}
