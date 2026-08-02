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

	wire := lowerRequest(req, true, false)

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

func TestMergeSystemMessages(t *testing.T) {
	msgs := []types.Message{
		{Role: "system", Content: "sys1"},
		{Role: "system", Content: "sys2"},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
		{Role: "system", Content: "sys3"},
		{Role: "user", Content: "next"},
	}

	out := mergeSystemMessages(msgs)

	if len(out) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(out))
	}
	if out[0].Role != "user" || out[0].Content != "sys1\n\nsys2\n\nhello" {
		t.Errorf("first user should have merged content, got role=%s content=%q", out[0].Role, out[0].Content)
	}
	if out[1].Role != "assistant" || out[1].Content != "hi" {
		t.Errorf("second should be assistant, got role=%s", out[1].Role)
	}
	if out[2].Role != "user" || out[2].Content != "sys3\n\nnext" {
		t.Errorf("third should be merged user, got role=%s content=%q", out[2].Role, out[2].Content)
	}
}

func TestMergeSystemMessages_OnlySystem(t *testing.T) {
	msgs := []types.Message{
		{Role: "system", Content: "sys1"},
		{Role: "system", Content: "sys2"},
	}

	out := mergeSystemMessages(msgs)

	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}
	if out[0].Role != "user" {
		t.Errorf("trailing system should become user, got %s", out[0].Role)
	}
}

func TestMergeSystemMessages_NoSystem(t *testing.T) {
	msgs := []types.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}

	out := mergeSystemMessages(msgs)

	if len(out) != len(msgs) {
		t.Fatalf("no merge needed, expected %d got %d", len(msgs), len(out))
	}
}

func TestCopilotMode_LowerMessages(t *testing.T) {
	msgs := []types.Message{
		{Role: "system", Content: "you are helpful"},
		{Role: "user", Content: "hello"},
	}

	out := lowerMessages(msgs, false, true)

	if len(out) != 1 {
		t.Fatalf("Copilot mode should merge system into user, expected 1 msg got %d", len(out))
	}
	if out[0].Role != "user" {
		t.Errorf("expected user role, got %s", out[0].Role)
	}
	want := "you are helpful\n\nhello"
	if out[0].Content == nil || *out[0].Content != want {
		t.Errorf("expected %q, got %v", want, out[0].Content)
	}
}
