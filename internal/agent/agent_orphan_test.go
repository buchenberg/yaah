package agent

import (
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

func TestRepairOrphans_NoOrphans(t *testing.T) {
	messages := []types.Message{
		types.UserMsg("hello"),
		{Role: "assistant", Content: "hi there"},
	}
	result := repairOrphans(messages)
	if len(result) != len(messages) {
		t.Fatalf("expected %d messages, got %d", len(messages), len(result))
	}
}

func TestRepairOrphans_OrphanedResult(t *testing.T) {
	messages := []types.Message{
		types.UserMsg("hello"),
		types.ToolResultMsg("call-1", "echo", "result from echo"),
	}
	result := repairOrphans(messages)
	if len(result) != 1 {
		t.Fatalf("expected 1 message after dropping orphaned result, got %d", len(result))
	}
	if result[0].Role != "user" {
		t.Errorf("expected user message to remain, got role=%s", result[0].Role)
	}
}

func TestRepairOrphans_OrphanedCall(t *testing.T) {
	messages := []types.Message{
		types.UserMsg("run tool"),
		{
			Role: "assistant",
			ToolCalls: []types.ToolCall{
				{ID: "call-1", Type: "function", Function: types.ToolCallFn{Name: "echo", Arguments: `{}`}},
			},
		},
	}
	result := repairOrphans(messages)
	if len(result) != 3 {
		t.Fatalf("expected 3 messages (user + assistant + synthetic error), got %d", len(result))
	}
	last := result[len(result)-1]
	if last.Role != "tool" {
		t.Errorf("expected synthetic tool result, got role=%s", last.Role)
	}
	if last.ToolCallID != "call-1" {
		t.Errorf("expected tool call ID call-1, got %s", last.ToolCallID)
	}
	if last.Content == "" {
		t.Error("expected non-empty error content in synthetic result")
	}
}

// TestRepairOrphans_SyntheticBeforeFollowingMessages verifies that the
// synthesized result for an interrupted call is inserted directly after
// the assistant message that owns the call, not at the end of the
// history. Providers require tool messages to immediately follow the
// tool_calls message; appending at the end would leave a user message
// between them when the session continued after the interruption.
func TestRepairOrphans_SyntheticBeforeFollowingMessages(t *testing.T) {
	messages := []types.Message{
		types.UserMsg("run tool"),
		{
			Role: "assistant",
			ToolCalls: []types.ToolCall{
				{ID: "call-1", Type: "function", Function: types.ToolCallFn{Name: "echo", Arguments: `{}`}},
			},
		},
		types.UserMsg("next prompt after interruption"),
		{Role: "assistant", Content: "continuing"},
	}
	result := repairOrphans(messages)
	if len(result) != 5 {
		t.Fatalf("expected 5 messages (user + assistant + synthetic + user + assistant), got %d", len(result))
	}
	if result[2].Role != "tool" || result[2].ToolCallID != "call-1" {
		t.Errorf("expected synthetic result for call-1 at index 2, got role=%s id=%s", result[2].Role, result[2].ToolCallID)
	}
	if result[3].Role != "user" {
		t.Errorf("expected following user message at index 3, got role=%s", result[3].Role)
	}
}

func TestRepairOrphans_BothOrphaned(t *testing.T) {
	messages := []types.Message{
		types.UserMsg("run tool"),
		{
			Role: "assistant",
			ToolCalls: []types.ToolCall{
				{ID: "good-call", Type: "function", Function: types.ToolCallFn{Name: "echo", Arguments: `{}`}},
				{ID: "orphan-call", Type: "function", Function: types.ToolCallFn{Name: "grep", Arguments: `{}`}},
			},
		},
		types.ToolResultMsg("good-call", "echo", "result"),
		types.ToolResultMsg("bad-result", "ls", "orphaned"),
	}
	result := repairOrphans(messages)

	toolCallIDs := make(map[string]bool)
	for _, m := range result {
		if m.Role == "tool" {
			toolCallIDs[m.ToolCallID] = true
		}
	}
	if toolCallIDs["bad-result"] {
		t.Error("orphaned result (bad-result) should have been removed")
	}
	if !toolCallIDs["good-call"] {
		t.Error("good-call result should remain")
	}
	if !toolCallIDs["orphan-call"] {
		t.Error("orphan-call should have synthetic error result")
	}
}
