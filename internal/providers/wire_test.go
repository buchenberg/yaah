package providers

import (
	"encoding/json"
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

func TestLowerMessage_AssistantContentNull(t *testing.T) {
	msg := types.Message{
		Role:      "assistant",
		Content:   "",
		ToolCalls: []types.ToolCall{{ID: "c1", Type: "function", Function: types.ToolCallFn{Name: "read", Arguments: "{}"}}},
	}

	w := lowerMessage(msg, false)
	if w.Content != nil {
		t.Errorf("assistant with only tool_calls should have nil content, got %q", *w.Content)
	}

	data, _ := json.Marshal(w)
	var raw map[string]any
	json.Unmarshal(data, &raw)
	if raw["content"] != nil {
		t.Errorf("JSON content should be null, got %v", raw["content"])
	}
}

func TestLowerMessage_AssistantContentPresent(t *testing.T) {
	msg := types.Message{Role: "assistant", Content: "hello"}

	w := lowerMessage(msg, false)
	if w.Content == nil || *w.Content != "hello" {
		t.Errorf("assistant with text should have content, got %v", w.Content)
	}
}

func TestLowerMessage_AssistantEmptyContentNoToolCalls(t *testing.T) {
	msg := types.Message{Role: "assistant", Content: ""}

	w := lowerMessage(msg, false)
	if w.Content == nil || *w.Content != "" {
		t.Errorf("assistant with no tool_calls should have empty-string content, got %v", w.Content)
	}
}

func TestLowerMessage_ThinkingModeAlwaysSetsReasoning(t *testing.T) {
	msg := types.Message{Role: "assistant", Content: "hi", ReasoningContent: ""}

	w := lowerMessage(msg, true)
	if w.ReasoningContent == nil {
		t.Fatal("thinking mode should always set reasoning_content on assistant")
	}
	if *w.ReasoningContent != "" {
		t.Errorf("expected empty reasoning_content, got %q", *w.ReasoningContent)
	}

	data, _ := json.Marshal(w)
	var raw map[string]any
	json.Unmarshal(data, &raw)
	if _, ok := raw["reasoning_content"]; !ok {
		t.Error("reasoning_content must be present in JSON for thinking mode")
	}
}

func TestLowerMessage_NonThinkingOmitsEmptyReasoning(t *testing.T) {
	msg := types.Message{Role: "assistant", Content: "hi", ReasoningContent: ""}

	w := lowerMessage(msg, false)
	if w.ReasoningContent != nil {
		t.Errorf("non-thinking mode should omit empty reasoning_content, got %q", *w.ReasoningContent)
	}
}

func TestLowerMessage_NonThinkingPreservesReasoning(t *testing.T) {
	msg := types.Message{Role: "assistant", Content: "hi", ReasoningContent: "let me think"}

	w := lowerMessage(msg, false)
	if w.ReasoningContent == nil || *w.ReasoningContent != "let me think" {
		t.Errorf("non-thinking mode should preserve non-empty reasoning_content")
	}
}

func TestLowerMessage_UserContentAlwaysPresent(t *testing.T) {
	msg := types.Message{Role: "user", Content: ""}

	w := lowerMessage(msg, false)
	if w.Content == nil {
		t.Fatal("user message should always have content set")
	}
}

func TestLowerRequest_ThinkingMode(t *testing.T) {
	req := types.ChatRequest{
		Model: "deepseek-r1",
		Messages: []types.Message{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "hi", ReasoningContent: "thinking..."},
			{Role: "assistant", Content: "", ToolCalls: []types.ToolCall{{ID: "c1", Type: "function", Function: types.ToolCallFn{Name: "x", Arguments: "{}"}}}},
		},
	}

	wire := lowerRequest(req, true)

	if wire.Messages[1].ReasoningContent == nil || *wire.Messages[1].ReasoningContent != "thinking..." {
		t.Error("first assistant should preserve reasoning")
	}
	if wire.Messages[2].ReasoningContent == nil {
		t.Error("second assistant should have empty reasoning_content in thinking mode")
	}
	if wire.Messages[2].Content != nil {
		t.Error("second assistant should have null content (tool_calls only)")
	}
}
