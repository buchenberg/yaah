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

func TestParseSSEChunk_marksOpenAIUsageInclusive(t *testing.T) {
	line := []byte(`data: {"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":1,"total_tokens":101,"prompt_tokens_details":{"cached_tokens":90}}}`)
	chunk, done, err := parseSSEChunk(line)
	if err != nil || done || chunk == nil || chunk.Usage == nil {
		t.Fatalf("parseSSEChunk() = %v, done=%v, err=%v", chunk, done, err)
	}
	if !chunk.Usage.PromptIncludesCache {
		t.Fatal("OpenAI usage must be marked PromptIncludesCache")
	}
	if got := chunk.Usage.InclusivePromptTokens(); got != 100 {
		t.Errorf("InclusivePromptTokens() = %d, want 100", got)
	}
}

// TestStreamDelta_reasoningDedup pins the de-duplication when an endpoint
// sends the same reasoning in reasoning_content and in content thinking
// blocks (review 616).
func TestStreamDelta_reasoningDedup(t *testing.T) {
	cases := []struct {
		name        string
		input       string
		wantContent string
		wantReason  string
	}{
		{name: "equal halves collapse", input: `{"reasoning_content":"same","content":[{"type":"thinking","thinking":"same"},{"type":"text","text":"hi"}]}`, wantContent: "hi", wantReason: "same"},
		{name: "distinct halves concatenate", input: `{"reasoning_content":"field","content":[{"type":"thinking","thinking":"blocks"}]}`, wantContent: "", wantReason: "fieldblocks"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var delta StreamDelta
			if err := delta.UnmarshalJSON([]byte(tc.input)); err != nil {
				t.Fatalf("UnmarshalJSON() error = %v", err)
			}
			if delta.Content != tc.wantContent {
				t.Errorf("Content = %q, want %q", delta.Content, tc.wantContent)
			}
			if delta.ReasoningContent != tc.wantReason {
				t.Errorf("ReasoningContent = %q, want %q", delta.ReasoningContent, tc.wantReason)
			}
		})
	}
}
