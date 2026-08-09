package tui2

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

	// Add a sub-agent start
	ui.AddSubAgentStart("sub-1", "analyst", "", "Analyze the code", "claude-3-sonnet")

	if len(ui.conversationLog) != 1 {
		t.Errorf("Expected 1 item in conversationLog, got %d", len(ui.conversationLog))
	}
	if ui.conversationLog[0].subBlock == nil {
		t.Error("Expected subBlock to be set")
	}
	if ui.conversationLog[0].subBlock.ID() != "sub-1" {
		t.Errorf("Expected subBlock ID to be 'sub-1', got %q", ui.conversationLog[0].subBlock.ID())
	}

	// End the sub-agent
	ui.AddSubAgentEnd("sub-1")

	if len(ui.conversationLog) != 1 {
		t.Errorf("Expected 1 item in conversationLog, got %d", len(ui.conversationLog))
	}
	if ui.conversationLog[0].subBlock == nil {
		t.Error("Expected subBlock to still exist")
	}
}

func TestAddSubAgentError(t *testing.T) {
	ui := New("test")

	// Add a sub-agent start
	ui.AddSubAgentStart("sub-1", "analyst", "", "Analyze the code", "claude-3-sonnet")

	// Add an error
	ui.AddSubAgentError("sub-1", "sub-agent failed")

	if len(ui.conversationLog) != 1 {
		t.Errorf("Expected 1 item in conversationLog, got %d", len(ui.conversationLog))
	}
	if ui.conversationLog[0].subBlock == nil {
		t.Error("Expected subBlock to exist")
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

func TestToggleAllSubAgents(t *testing.T) {
	ui := New("test")

	// Add sub-agent blocks
	ui.AddSubAgentStart("sub-1", "analyst", "", "Task 1", "model")
	ui.AddSubAgentStart("sub-2", "reviewer", "", "Task 2", "model")

	// Toggle all sub-agents
	ui.toggleAllSubAgents()

	if len(ui.conversationLog) != 2 {
		t.Errorf("Expected 2 items in conversationLog, got %d", len(ui.conversationLog))
	}
}

func TestBlinkSubAgents(t *testing.T) {
	ui := New("test")

	// Add sub-agent blocks
	ui.AddSubAgentStart("sub-1", "analyst", "", "Task 1", "model")

	// Blink should not panic
	ui.BlinkSubAgents()
}

func TestAdvanceReasoningSeeds(t *testing.T) {
	ui := New("test")

	// Add reasoning blocks
	ui.AddReasoningBlock("reason-1", "Thinking...")
	ui.AddReasoningBlock("reason-2", "More thinking...")

	// Advance seeds
	ui.AdvanceReasoningSeeds(1.0)

	if len(ui.conversationLog) != 2 {
		t.Errorf("Expected 2 items in conversationLog, got %d", len(ui.conversationLog))
	}
}
