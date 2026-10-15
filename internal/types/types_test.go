package types

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestChatRequest_serializesToOpenAIFormat(t *testing.T) {
	req := ChatRequest{
		Model: "gpt-4o-mini",
		Messages: []Message{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "Hello"},
		},
		Tools: []ToolDef{
			{
				Type: "function",
				Function: ToolFn{
					Name:        "read_file",
					Description: "Read a file",
					Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
				},
			},
		},
		Temperature: 0.7,
		Stream:      false,
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Verify key fields are present
	s := string(data)
	for _, want := range []string{
		`"model":"gpt-4o-mini"`,
		`"role":"system"`,
		`"content":"You are helpful."`,
		`"name":"read_file"`,
		`"temperature":0.7`,
		`"stream":false`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in output:\n%s", want, s)
		}
	}
}

func TestChatResponse_parsesToolCalls(t *testing.T) {
	body := `{
		"id": "chat-123",
		"object": "chat.completion",
		"model": "gpt-4o-mini",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": null,
				"tool_calls": [{
					"id": "call_1",
					"type": "function",
					"function": {
						"name": "read_file",
						"arguments": "{\"path\":\"/tmp/test.txt\"}"
					}
				}]
			},
			"finish_reason": "tool_calls"
		}],
		"usage": {
			"prompt_tokens": 10,
			"completion_tokens": 20,
			"total_tokens": 30
		}
	}`

	var resp ChatResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(resp.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(resp.Choices))
	}

	msg := resp.Choices[0].Message
	if msg.Role != "assistant" {
		t.Errorf("role = %q, want assistant", msg.Role)
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(msg.ToolCalls))
	}
	// Content may be empty string when the response contains tool_calls
	if msg.Content != "" {
		t.Errorf("expected empty content for tool_calls response, got %q", msg.Content)
	}
	if msg.ToolCalls[0].Function.Name != "read_file" {
		t.Errorf("tool name = %q, want read_file", msg.ToolCalls[0].Function.Name)
	}
	if msg.ToolCalls[0].Function.Arguments != `{"path":"/tmp/test.txt"}` {
		t.Errorf("tool args = %q", msg.ToolCalls[0].Function.Arguments)
	}

	if resp.Usage.TotalTokens != 30 {
		t.Errorf("total_tokens = %d, want 30", resp.Usage.TotalTokens)
	}
}

func TestChatResponse_parsesPlainTextResponse(t *testing.T) {
	body := `{
		"id": "chat-456",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": "Hello! How can I help?"
			},
			"finish_reason": "stop"
		}]
	}`

	var resp ChatResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	msg := resp.Choices[0].Message
	if msg.Content != "Hello! How can I help?" {
		t.Errorf("content = %q", msg.Content)
	}
	if len(msg.ToolCalls) > 0 {
		t.Errorf("expected no tool calls for plain text response")
	}
}

func TestUsageGenAIInputTokens(t *testing.T) {
	t.Run("openai inclusive wire shape", func(t *testing.T) {
		// OpenAI reports PromptTokens that ALREADY INCLUDE the cached
		// subset in PromptTokensDetails — must not be double-counted.
		u := Usage{
			PromptTokens:        100,
			CompletionTokens:    42,
			TotalTokens:         142,
			PromptTokensDetails: &PromptTokensDetails{CachedTokens: 90},
		}
		if got := u.GenAIInputTokens(); got != 100 {
			t.Errorf("GenAIInputTokens() = %d, want 100", got)
		}
		if got := u.CacheTokens(); got != 90 {
			t.Errorf("CacheTokens() = %d, want 90 (cache READ portion only)", got)
		}
	})

	t.Run("anthropic exclusive stream aggregate", func(t *testing.T) {
		// Anthropic reports UNCACHED InputTokens separately from cache
		// reads; addStreamUsage folds the reads into the top-level
		// CachedPromptTokens and leaves PromptTokensDetails nil. The
		// GenAI semconv total must be the cache-inclusive sum.
		u := Usage{
			PromptTokens:       10,
			CompletionTokens:   42,
			TotalTokens:        42,
			CachedPromptTokens: 90,
		}
		if got := u.GenAIInputTokens(); got != 100 {
			t.Errorf("GenAIInputTokens() = %d, want 100", got)
		}
		if got := u.CacheTokens(); got != 90 {
			t.Errorf("CacheTokens() = %d, want 90 (cache READ portion only)", got)
		}
	})

	t.Run("uncached usage", func(t *testing.T) {
		u := Usage{PromptTokens: 42, CompletionTokens: 7, TotalTokens: 49}
		if got := u.GenAIInputTokens(); got != 42 {
			t.Errorf("GenAIInputTokens() = %d, want 42", got)
		}
	})

	t.Run("details without cached tokens", func(t *testing.T) {
		u := Usage{
			PromptTokens:        55,
			PromptTokensDetails: &PromptTokensDetails{CachedTokens: 0},
		}
		if got := u.GenAIInputTokens(); got != 55 {
			t.Errorf("GenAIInputTokens() = %d, want 55", got)
		}
	})

	t.Run("zero usage", func(t *testing.T) {
		if got := (Usage{}).GenAIInputTokens(); got != 0 {
			t.Errorf("GenAIInputTokens() = %d, want 0", got)
		}
	})
}
