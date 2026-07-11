package providers

import (
	"encoding/json"
	"testing"
)

func TestParseSSEChunk_extractsDelta(t *testing.T) {
	chunk := `data: {"id":"chat-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`

	result, done, err := parseSSEChunk([]byte(chunk))
	if err != nil {
		t.Fatalf("parseSSEChunk() error: %v", err)
	}
	if done {
		t.Error("expected not done")
	}
	if result == nil {
		t.Fatal("expected result")
	}
	if len(result.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(result.Choices))
	}
	if result.Choices[0].Delta.Content != "Hello" {
		t.Errorf("delta content = %q, want %q", result.Choices[0].Delta.Content, "Hello")
	}
}

func TestParseSSEChunk_handlesDoneSignal(t *testing.T) {
	chunk := `data: [DONE]`

	_, done, err := parseSSEChunk([]byte(chunk))
	if err != nil {
		t.Fatalf("parseSSEChunk() error: %v", err)
	}
	if !done {
		t.Error("expected done")
	}
}

func TestParseSSEChunk_handlesToolCalls(t *testing.T) {
	chunk := `data: {"id":"chat-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":""}}]},"finish_reason":null}]}`

	result, done, err := parseSSEChunk([]byte(chunk))
	if err != nil {
		t.Fatalf("parseSSEChunk() error: %v", err)
	}
	if done {
		t.Error("expected not done")
	}
	if len(result.Choices[0].Delta.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(result.Choices[0].Delta.ToolCalls))
	}
	if result.Choices[0].Delta.ToolCalls[0].Function.Name != "read" {
		t.Errorf("tool name = %q", result.Choices[0].Delta.ToolCalls[0].Function.Name)
	}
}

func TestParseSSEChunk_skipsEmptyLines(t *testing.T) {
	_, _, err := parseSSEChunk([]byte(""))
	if err != nil {
		t.Fatalf("expected no error for empty chunk, got %v", err)
	}
}

func TestStreamChunk_serializesFromJSON(t *testing.T) {
	data := `{"id":"chat-1","choices":[{"index":0,"delta":{"content":"world"},"finish_reason":null}]}`
	var chunk StreamChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if chunk.Choices[0].Delta.Content != "world" {
		t.Errorf("content = %q", chunk.Choices[0].Delta.Content)
	}
}
