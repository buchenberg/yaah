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
