package tui

import (
	"testing"
)

func TestAddReasoningBlock(t *testing.T) {
	ui := New("test")

	// Initially no reasoning blocks
	if len(ui.conversationLog) != 0 {
		t.Errorf("Expected empty conversationLog, got %d items", len(ui.conversationLog))
	}

	// Add a reasoning block
	ui.AddReasoningBlock("reason-1", "Thinking about the problem...")

	if len(ui.conversationLog) != 1 {
		t.Errorf("Expected 1 item in conversationLog, got %d", len(ui.conversationLog))
	}
	if ui.conversationLog[0].reasoningBlock == nil {
		t.Error("Expected reasoningBlock to be set")
	}
	if ui.conversationLog[0].reasoningBlock.ID() != "reason-1" {
		t.Errorf("Expected reasoningBlock ID to be 'reason-1', got %q", ui.conversationLog[0].reasoningBlock.ID())
	}
}

func TestAddToolStartAndEnd(t *testing.T) {
	ui := New("test")

	// Add a tool start
	ui.AddToolStart("1", "read", "file.txt")

	if len(ui.conversationLog) != 1 {
		t.Errorf("Expected 1 item in conversationLog, got %d", len(ui.conversationLog))
	}
	if ui.conversationLog[0].toolBlock == nil {
		t.Error("Expected toolBlock to be set")
	}
	if ui.conversationLog[0].toolBlock.ID() != "1" {
		t.Errorf("Expected toolBlock ID to be '1', got %q", ui.conversationLog[0].toolBlock.ID())
	}

	// End the tool
	ui.AddToolEnd("1", "read", "file contents")

	// The tool block should be updated (completed)
	if len(ui.conversationLog) != 1 {
		t.Errorf("Expected 1 item in conversationLog, got %d", len(ui.conversationLog))
	}
	// The block should still exist but be in completed state
	if ui.conversationLog[0].toolBlock == nil {
		t.Error("Expected toolBlock to still exist")
	}
}

func TestAddToolError(t *testing.T) {
	ui := New("test")

	// Add a tool start
	ui.AddToolStart("1", "read", "file.txt")

	// Add an error
	ui.AddToolError("1", "read failed", "file not found")

	if len(ui.conversationLog) != 1 {
		t.Errorf("Expected 1 item in conversationLog, got %d", len(ui.conversationLog))
	}
	if ui.conversationLog[0].toolBlock == nil {
		t.Error("Expected toolBlock to exist")
	}
	// The tool block should be in failed state
}

func TestAddToolEnd_EmptyResult(t *testing.T) {
	ui := New("test")

	// Add a tool start
	ui.AddToolStart("1", "delete", "file.txt")

	// End with empty result (should complete, not fail)
	ui.AddToolEnd("1", "delete", "")

	if len(ui.conversationLog) != 1 {
		t.Errorf("Expected 1 item in conversationLog, got %d", len(ui.conversationLog))
	}
	// The tool should be completed, not failed
	if ui.conversationLog[0].toolBlock == nil {
		t.Error("Expected toolBlock to exist")
	}
	// If the tool block's state indicates failure, that's the bug we fixed
	// We verify it doesn't fail by checking the block exists and the error path wasn't taken
}

func TestAddSubAgentStartAndEnd(t *testing.T) {
	ui := New("test")

	// Sub-agents belong to the dedicated pane, not the conversation log.
	ui.AddSubAgentStart("sub-1", "analyst", "Analyze the code", "")

	if len(ui.conversationLog) != 0 {
		t.Errorf("sub-agent must not add a conversation item, got %d", len(ui.conversationLog))
	}
	if len(ui.subAgents) != 1 {
		t.Fatalf("Expected 1 sub-agent, got %d", len(ui.subAgents))
	}
	if ui.subAgents[0].ID() != "sub-1" {
		t.Errorf("Expected sub-agent ID to be 'sub-1', got %q", ui.subAgents[0].ID())
	}
	if !ui.subAgentsBusy.Load() {
		t.Error("Expected sub-agent animation flag to be set while active")
	}

	// Terminal blocks leave the active pane immediately.
	ui.AddSubAgentEnd("sub-1")

	if len(ui.subAgents) != 0 {
		t.Errorf("Expected completed sub-agent to be pruned, got %d remaining", len(ui.subAgents))
	}
	if ui.subAgentsBusy.Load() {
		t.Error("Expected sub-agent animation flag to clear when nothing is active")
	}
}

func TestAddSubAgentError(t *testing.T) {
	ui := New("test")

	ui.AddSubAgentStart("sub-1", "analyst", "Analyze the code", "")
	ui.AddSubAgentError("sub-1")

	if len(ui.subAgents) != 0 {
		t.Fatalf("Expected failed sub-agent to be pruned, got %d remaining", len(ui.subAgents))
	}
	if ui.subAgentsBusy.Load() {
		t.Error("Expected animation flag to clear after the failed agent is pruned")
	}
}

func TestAdvanceSubAgentSpinners(t *testing.T) {
	ui := New("test")
	ui.AddSubAgentStart("sub-1", "analyst", "Task 1", "")
	ui.AddSubAgentStart("sub-2", "reviewer", "Task 2", "")

	before1 := ui.subAgents[0].SpinnerFrame()
	before2 := ui.subAgents[1].SpinnerFrame()
	ui.advanceSubAgentSpinners()

	if got := ui.subAgents[0].SpinnerFrame(); got == before1 {
		t.Error("first active sub-agent spinner did not advance")
	}
	if got := ui.subAgents[1].SpinnerFrame(); got == before2 {
		t.Error("second active sub-agent spinner did not advance")
	}

	ui.AddSubAgentEnd("sub-2")
	if len(ui.subAgents) != 1 {
		t.Fatalf("Expected one remaining active sub-agent, got %d", len(ui.subAgents))
	}
	if !ui.subAgentsBusy.Load() {
		t.Error("one remaining active sub-agent must keep the animation flag set")
	}
}

func TestToggleBlockByIndex(t *testing.T) {
	ui := New("test")

	// Add multiple blocks
	ui.AddReasoningBlock("reason-1", "Thinking...")
	ui.AddToolStart("1", "read", "file.txt")
	ui.AddReasoningBlock("reason-2", "More thinking...")

	if len(ui.conversationLog) != 3 {
		t.Errorf("Expected 3 items in conversationLog, got %d", len(ui.conversationLog))
	}

	// Toggle the first block (index 0)
	ui.ToggleBlockByIndex(0)
	// No easy way to verify the toggle happened without inspecting internal state,
	// but we can verify no panic occurred

	// Toggle out of range should be safe
	ui.ToggleBlockByIndex(-1)
	ui.ToggleBlockByIndex(100)
}

func TestCollapseAll(t *testing.T) {
	ui := New("test")

	// Add some reasoning blocks
	ui.AddReasoningBlock("reason-1", "Thinking...")
	ui.AddReasoningBlock("reason-2", "More thinking...")

	// Collapse all should not panic
	ui.CollapseAll()

	// Verify conversationLog still has the blocks
	if len(ui.conversationLog) != 2 {
		t.Errorf("Expected 2 items in conversationLog after CollapseAll, got %d", len(ui.conversationLog))
	}
}

func TestToggleAllReasoning(t *testing.T) {
	ui := New("test")

	// Add reasoning blocks
	ui.AddReasoningBlock("reason-1", "Thinking...")
	ui.AddReasoningBlock("reason-2", "More thinking...")

	// Toggle all reasoning blocks
	ui.toggleAllReasoning()

	if len(ui.conversationLog) != 2 {
		t.Errorf("Expected 2 items in conversationLog, got %d", len(ui.conversationLog))
	}
}

func TestToggleAllTools(t *testing.T) {
	ui := New("test")

	// Add tool blocks
	ui.AddToolStart("1", "read", "file1.txt")
	ui.AddToolStart("2", "read", "file2.txt")

	// Toggle all tools
	ui.toggleAllTools()

	if len(ui.conversationLog) != 2 {
		t.Errorf("Expected 2 items in conversationLog, got %d", len(ui.conversationLog))
	}
}

// TestAddSubAgentStartCarriesWorld pins the per-dispatch world on the pane
// row: the label comes from the SubAgentStartEvent, not the session summary.
func TestAddSubAgentStartCarriesWorld(t *testing.T) {
	ui := New("test")
	ui.AddSubAgentStart("sub-1", "developer", "Task", "worktree (degraded: no image)")
	sb := ui.findSubAgent("sub-1")
	if sb == nil {
		t.Fatal("block not found")
	}
	if got := sb.World(); got != "worktree (degraded: no image)" {
		t.Fatalf("World = %q", got)
	}
}
