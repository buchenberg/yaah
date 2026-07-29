package providers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

func TestChatRequestToAnthropic_SystemMessages(t *testing.T) {
	req := types.ChatRequest{
		Model:     "claude-3.5-sonnet",
		MaxTokens: 1000,
		Messages: []types.Message{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "Hello"},
		},
	}

	result := chatRequestToAnthropic(req)

	if len(result.System) != 1 {
		t.Fatalf("expected 1 system block, got %d", len(result.System))
	}
	if result.System[0].Text != "You are helpful." {
		t.Errorf("expected system text 'You are helpful.', got %q", result.System[0].Text)
	}
	if result.System[0].Type != "text" {
		t.Errorf("expected system block type 'text', got %q", result.System[0].Type)
	}

	if len(result.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result.Messages))
	}
	if result.Messages[0].Role != "user" {
		t.Errorf("expected message role 'user', got %q", result.Messages[0].Role)
	}
}

func TestChatRequestToAnthropic_Conversation(t *testing.T) {
	req := types.ChatRequest{
		Model:     "claude-3.5-sonnet",
		MaxTokens: 1000,
		Messages: []types.Message{
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi there!"},
			{Role: "user", Content: "How are you?"},
		},
	}

	result := chatRequestToAnthropic(req)

	if len(result.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result.Messages))
	}
	if result.Messages[0].Role != "user" {
		t.Errorf("expected first message role 'user', got %q", result.Messages[0].Role)
	}
	if result.Messages[1].Role != "assistant" {
		t.Errorf("expected second message role 'assistant', got %q", result.Messages[1].Role)
	}
	if result.Messages[2].Role != "user" {
		t.Errorf("expected third message role 'user', got %q", result.Messages[2].Role)
	}
}

func TestChatRequestToAnthropic_MergesConsecutiveUser(t *testing.T) {
	req := types.ChatRequest{
		Model:     "claude-3.5-sonnet",
		MaxTokens: 1000,
		Messages: []types.Message{
			{Role: "user", Content: "First"},
			{Role: "user", Content: "Second"},
		},
	}

	result := chatRequestToAnthropic(req)

	if len(result.Messages) != 1 {
		t.Fatalf("expected 1 merged message, got %d", len(result.Messages))
	}
	if len(result.Messages[0].Content) != 2 {
		t.Fatalf("expected 2 content blocks in merged message, got %d", len(result.Messages[0].Content))
	}
}

func TestChatRequestToAnthropic_ToolCalls(t *testing.T) {
	req := types.ChatRequest{
		Model:     "claude-3.5-sonnet",
		MaxTokens: 1000,
		Messages: []types.Message{
			{Role: "user", Content: "Get weather"},
			{Role: "assistant", Content: "", ToolCalls: []types.ToolCall{
				{ID: "call_1", Type: "function", Function: types.ToolCallFn{Name: "get_weather", Arguments: `{"city":"NYC"}`}},
			}},
			{Role: "tool", ToolCallID: "call_1", Content: "72F sunny"},
		},
	}

	result := chatRequestToAnthropic(req)

	if len(result.Messages) != 3 {
		t.Fatalf("expected 3 messages (user, assistant, tool_result_user), got %d", len(result.Messages))
	}

	user1 := result.Messages[0]
	if user1.Role != "user" {
		t.Errorf("expected first message role 'user', got %q", user1.Role)
	}

	assistant := result.Messages[1]
	if assistant.Role != "assistant" {
		t.Errorf("expected assistant role, got %q", assistant.Role)
	}
	hasToolUse := false
	for _, block := range assistant.Content {
		if block.Type == "tool_use" {
			hasToolUse = true
			if block.ID != "call_1" {
				t.Errorf("expected tool_use id 'call_1', got %q", block.ID)
			}
			if block.Name != "get_weather" {
				t.Errorf("expected tool_use name 'get_weather', got %q", block.Name)
			}
		}
	}
	if !hasToolUse {
		t.Error("expected tool_use block in assistant message")
	}

	user2 := result.Messages[2]
	if user2.Role != "user" {
		t.Errorf("expected user role for tool result, got %q", user2.Role)
	}
	hasToolResult := false
	for _, block := range user2.Content {
		if block.Type == "tool_result" {
			hasToolResult = true
			if block.ToolUseID != "call_1" {
				t.Errorf("expected tool_result tool_use_id 'call_1', got %q", block.ToolUseID)
			}
		}
	}
	if !hasToolResult {
		t.Error("expected tool_result block in user message")
	}
}

func TestChatRequestToAnthropic_Tools(t *testing.T) {
	params := json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`)
	req := types.ChatRequest{
		Model:     "claude-3.5-sonnet",
		MaxTokens: 1000,
		Tools: []types.ToolDef{
			{Type: "function", Function: types.ToolFn{Name: "get_weather", Description: "Get weather", Parameters: params}},
		},
		Messages: []types.Message{
			{Role: "user", Content: "Hello"},
		},
	}

	result := chatRequestToAnthropic(req)

	if len(result.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(result.Tools))
	}
	if result.Tools[0].Name != "get_weather" {
		t.Errorf("expected tool name 'get_weather', got %q", result.Tools[0].Name)
	}
	if result.Tools[0].Description != "Get weather" {
		t.Errorf("expected tool description 'Get weather', got %q", result.Tools[0].Description)
	}
}

func TestChatRequestToAnthropic_MaxTokensDefault(t *testing.T) {
	req := types.ChatRequest{
		Model:     "claude-3.5-sonnet",
		MaxTokens: 0,
		Messages: []types.Message{
			{Role: "user", Content: "Hello"},
		},
	}

	result := chatRequestToAnthropic(req)

	if result.MaxTokens != 4096 {
		t.Errorf("expected MaxTokens 4096, got %d", result.MaxTokens)
	}
}

func TestChatRequestToAnthropic_MaxTokensExplicit(t *testing.T) {
	req := types.ChatRequest{
		Model:     "claude-3.5-sonnet",
		MaxTokens: 8000,
		Messages: []types.Message{
			{Role: "user", Content: "Hello"},
		},
	}

	result := chatRequestToAnthropic(req)

	if result.MaxTokens != 8000 {
		t.Errorf("expected MaxTokens 8000, got %d", result.MaxTokens)
	}
}

func TestChatRequestToAnthropic_CacheControlPassthrough(t *testing.T) {
	req := types.ChatRequest{
		Model:     "claude-3.5-sonnet",
		MaxTokens: 1000,
		Messages: []types.Message{
			{Role: "system", Content: "You are helpful.", CacheControl: &types.CacheControl{Type: "ephemeral"}},
			{Role: "user", Content: "Hello", CacheControl: &types.CacheControl{Type: "ephemeral"}},
		},
	}

	result := chatRequestToAnthropic(req)

	if result.System[0].CacheControl == nil {
		t.Error("expected cache_control on system block")
	} else if result.System[0].CacheControl.Type != "ephemeral" {
		t.Errorf("expected cache_control type 'ephemeral', got %q", result.System[0].CacheControl.Type)
	}

	userBlock := result.Messages[0].Content[0]
	if userBlock.CacheControl == nil {
		t.Error("expected cache_control on user text block")
	} else if userBlock.CacheControl.Type != "ephemeral" {
		t.Errorf("expected cache_control type 'ephemeral', got %q", userBlock.CacheControl.Type)
	}
}

func TestChatRequestToAnthropic_ReasoningContent(t *testing.T) {
	req := types.ChatRequest{
		Model:     "claude-3.5-sonnet",
		MaxTokens: 1000,
		Messages: []types.Message{
			{Role: "assistant", ReasoningContent: "Let me think about this...", Content: "The answer is 42."},
		},
	}

	result := chatRequestToAnthropic(req)

	blocks := result.Messages[0].Content
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks (thinking + text), got %d", len(blocks))
	}
	if blocks[0].Type != "thinking" {
		t.Errorf("expected first block type 'thinking', got %q", blocks[0].Type)
	}
	if blocks[0].Thinking != "Let me think about this..." {
		t.Errorf("expected thinking content, got %q", blocks[0].Thinking)
	}
	if blocks[1].Type != "text" {
		t.Errorf("expected second block type 'text', got %q", blocks[1].Type)
	}
}

func TestAnthropicResponseToChat(t *testing.T) {
	resp := anthropicResponse{
		ID:    "msg_123",
		Model: "claude-3.5-sonnet",
		Role:  "assistant",
		Content: []anthropicBlock{
			{Type: "text", Text: "Hello "},
			{Type: "text", Text: "world!"},
			{Type: "tool_use", ID: "toolu_1", Name: "get_weather", Input: json.RawMessage(`{"city":"NYC"}`)},
		},
		StopReason: "end_turn",
		Usage: anthropicUsage{
			InputTokens:          50,
			OutputTokens:         30,
			CacheReadInputTokens: 20,
		},
	}

	result := anthropicResponseToChat(resp)

	if result.ID != "msg_123" {
		t.Errorf("expected ID 'msg_123', got %q", result.ID)
	}
	if result.Model != "claude-3.5-sonnet" {
		t.Errorf("expected Model 'claude-3.5-sonnet', got %q", result.Model)
	}
	if len(result.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(result.Choices))
	}

	choice := result.Choices[0]
	if choice.Message.Content != "Hello world!" {
		t.Errorf("expected content 'Hello world!', got %q", choice.Message.Content)
	}
	if choice.FinishReason != "stop" {
		t.Errorf("expected finish_reason 'stop', got %q", choice.FinishReason)
	}
	if len(choice.Message.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(choice.Message.ToolCalls))
	}
	if choice.Message.ToolCalls[0].Function.Name != "get_weather" {
		t.Errorf("expected tool call name 'get_weather', got %q", choice.Message.ToolCalls[0].Function.Name)
	}

	if result.Usage.PromptTokens != 50 {
		t.Errorf("expected PromptTokens 50, got %d", result.Usage.PromptTokens)
	}
	if result.Usage.PromptTokensDetails.CachedTokens != 20 {
		t.Errorf("expected CachedTokens 20, got %d", result.Usage.PromptTokensDetails.CachedTokens)
	}
}

func TestAnthropicResponseToChat_ToolUseStopReason(t *testing.T) {
	resp := anthropicResponse{
		ID:    "msg_456",
		Model: "claude-3.5-sonnet",
		Content: []anthropicBlock{
			{Type: "tool_use", ID: "toolu_2", Name: "search", Input: json.RawMessage(`{"query":"test"}`)},
		},
		StopReason: "tool_use",
		Usage:      anthropicUsage{InputTokens: 10, OutputTokens: 5},
	}

	result := anthropicResponseToChat(resp)

	if result.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("expected finish_reason 'tool_calls' for tool_use stop, got %q", result.Choices[0].FinishReason)
	}
}

func TestAnthropicResponseToChat_Thinking(t *testing.T) {
	resp := anthropicResponse{
		ID:    "msg_789",
		Model: "claude-3.5-sonnet",
		Content: []anthropicBlock{
			{Type: "thinking", Thinking: "Hmm, let me consider..."},
			{Type: "text", Text: "The answer is 42."},
		},
		StopReason: "end_turn",
		Usage:      anthropicUsage{InputTokens: 20, OutputTokens: 15},
	}

	result := anthropicResponseToChat(resp)

	if result.Choices[0].Message.ReasoningContent != "Hmm, let me consider..." {
		t.Errorf("expected reasoning content, got %q", result.Choices[0].Message.ReasoningContent)
	}
	if result.Choices[0].Message.Content != "The answer is 42." {
		t.Errorf("expected text content, got %q", result.Choices[0].Message.Content)
	}
}

func TestAnthropicResponseToChat_MaxTokensStop(t *testing.T) {
	resp := anthropicResponse{
		ID:    "msg_999",
		Model: "claude-3.5-sonnet",
		Content: []anthropicBlock{
			{Type: "text", Text: "truncated..."},
		},
		StopReason: "max_tokens",
		Usage:      anthropicUsage{InputTokens: 10, OutputTokens: 4096},
	}

	result := anthropicResponseToChat(resp)

	if result.Choices[0].FinishReason != "length" {
		t.Errorf("expected finish_reason 'length' for max_tokens stop, got %q", result.Choices[0].FinishReason)
	}
}

func TestProcessAnthropicSSEEvent_MessageStart(t *testing.T) {
	data := `{"type":"message_start","message":{"id":"msg_001","model":"claude-3.5-sonnet"}}`

	chunk := processAnthropicSSEEvent(data, nil)

	if chunk == nil {
		t.Fatal("expected non-nil chunk")
	}
	if chunk.ID != "msg_001" {
		t.Errorf("expected ID 'msg_001', got %q", chunk.ID)
	}
	if chunk.Model != "claude-3.5-sonnet" {
		t.Errorf("expected Model 'claude-3.5-sonnet', got %q", chunk.Model)
	}
}

func TestProcessAnthropicSSEEvent_TextStream(t *testing.T) {
	startData := `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`
	deltaData := `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`

	startChunk := processAnthropicSSEEvent(startData, nil)
	if startChunk == nil {
		t.Fatal("expected non-nil start chunk")
	}
	if startChunk.Choices[0].Delta.Role != "assistant" {
		t.Errorf("expected role 'assistant' on text block start, got %q", startChunk.Choices[0].Delta.Role)
	}

	deltaChunk := processAnthropicSSEEvent(deltaData, nil)
	if deltaChunk == nil {
		t.Fatal("expected non-nil delta chunk")
	}
	if deltaChunk.Choices[0].Delta.Content != "Hello" {
		t.Errorf("expected content 'Hello', got %q", deltaChunk.Choices[0].Delta.Content)
	}
}

func TestProcessAnthropicSSEEvent_ToolUseStream(t *testing.T) {
	startData := `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_abc","name":"get_weather"}}`
	deltaData := `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"NYC\"}"}}`

	toolArgs := make(map[int]string)

	startChunk := processAnthropicSSEEvent(startData, toolArgs)
	if startChunk == nil {
		t.Fatal("expected non-nil tool_use start chunk")
	}
	tcs := startChunk.Choices[0].Delta.ToolCalls
	if len(tcs) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(tcs))
	}
	if tcs[0].ID != "toolu_abc" {
		t.Errorf("expected tool call ID 'toolu_abc', got %q", tcs[0].ID)
	}
	if tcs[0].Function.Name != "get_weather" {
		t.Errorf("expected function name 'get_weather', got %q", tcs[0].Function.Name)
	}

	deltaChunk := processAnthropicSSEEvent(deltaData, toolArgs)
	if deltaChunk == nil {
		t.Fatal("expected non-nil tool_use delta chunk")
	}
	if toolArgs[0] != `{"city":"NYC"}` {
		t.Errorf("expected accumulated args '{\"city\":\"NYC\"}', got %q", toolArgs[0])
	}
}

func TestProcessAnthropicSSEEvent_ToolUseMultiFragment(t *testing.T) {
	toolArgs := make(map[int]string)

	startData := `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_xyz","name":"search"}}`
	processAnthropicSSEEvent(startData, toolArgs)

	frag1 := `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"qu"}}`
	processAnthropicSSEEvent(frag1, toolArgs)

	frag2 := `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"ery\":\"hello\"}"}}`
	processAnthropicSSEEvent(frag2, toolArgs)

	expected := `{"query":"hello"}`
	if toolArgs[0] != expected {
		t.Errorf("expected accumulated args %q, got %q", expected, toolArgs[0])
	}
}

func TestProcessAnthropicSSEEvent_MessageDelta(t *testing.T) {
	data := `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":42}}`

	chunk := processAnthropicSSEEvent(data, nil)

	if chunk == nil {
		t.Fatal("expected non-nil chunk")
	}
	if chunk.Choices[0].FinishReason == nil {
		t.Fatal("expected non-nil finish_reason")
	}
	if *chunk.Choices[0].FinishReason != "stop" {
		t.Errorf("expected finish_reason 'stop', got %q", *chunk.Choices[0].FinishReason)
	}
	if chunk.Usage.CompletionTokens != 42 {
		t.Errorf("expected CompletionTokens 42, got %d", chunk.Usage.CompletionTokens)
	}
}

func TestProcessAnthropicSSEEvent_MessageDeltaToolUse(t *testing.T) {
	data := `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":10}}`

	chunk := processAnthropicSSEEvent(data, nil)

	if chunk.Choices[0].FinishReason == nil {
		t.Fatal("expected non-nil finish_reason")
	}
	if *chunk.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("expected finish_reason 'tool_calls', got %q", *chunk.Choices[0].FinishReason)
	}
}

func TestProcessAnthropicSSEEvent_ThinkingStream(t *testing.T) {
	deltaData := `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me think"}}`

	chunk := processAnthropicSSEEvent(deltaData, nil)

	if chunk == nil {
		t.Fatal("expected non-nil chunk")
	}
	if chunk.Choices[0].Delta.ReasoningContent != "Let me think" {
		t.Errorf("expected reasoning content 'Let me think', got %q", chunk.Choices[0].Delta.ReasoningContent)
	}
}

func TestProcessAnthropicSSEEvent_CachedTokensInUsage(t *testing.T) {
	data := `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":80}}`

	chunk := processAnthropicSSEEvent(data, nil)

	if chunk.Usage.PromptTokensDetails == nil {
		t.Fatal("expected non-nil PromptTokensDetails")
	}
	if chunk.Usage.PromptTokensDetails.CachedTokens != 80 {
		t.Errorf("expected CachedTokens 80, got %d", chunk.Usage.PromptTokensDetails.CachedTokens)
	}
}

func TestProcessAnthropicSSEEvent_EmptyData(t *testing.T) {
	chunk := processAnthropicSSEEvent("", nil)
	if chunk != nil {
		t.Error("expected nil chunk for empty data")
	}
}

func TestProcessAnthropicSSEEvent_InvalidJSON(t *testing.T) {
	chunk := processAnthropicSSEEvent("not json", nil)
	if chunk != nil {
		t.Error("expected nil chunk for invalid JSON")
	}
}

func TestChatRequestToAnthropic_TemperaturePassthrough(t *testing.T) {
	req := types.ChatRequest{
		Model:       "claude-3.5-sonnet",
		MaxTokens:   1000,
		Temperature: 0.7,
		Messages: []types.Message{
			{Role: "user", Content: "Hello"},
		},
	}

	result := chatRequestToAnthropic(req)

	if result.Temperature != 0.7 {
		t.Errorf("expected Temperature 0.7, got %f", result.Temperature)
	}
}

func TestChatRequestToAnthropic_EmptySystem(t *testing.T) {
	req := types.ChatRequest{
		Model:     "claude-3.5-sonnet",
		MaxTokens: 1000,
		Messages: []types.Message{
			{Role: "user", Content: "Hello"},
		},
	}

	result := chatRequestToAnthropic(req)

	if len(result.System) != 0 {
		t.Errorf("expected 0 system blocks, got %d", len(result.System))
	}
}

func TestAnthropicClientInterface(t *testing.T) {
	client := NewAnthropicClient("https://api.anthropic.com", "sk-test", 30)
	if client == nil {
		t.Fatal("expected non-nil client")
	}

	_, err := client.ListModels(context.Background())
	if err == nil {
		t.Error("expected error from ListModels (not supported by Anthropic API)")
	}
	if !strings.Contains(err.Error(), "not support") {
		t.Errorf("expected 'not support' in error, got %q", err.Error())
	}
}

func TestChatRequestToAnthropic_PreservesModel(t *testing.T) {
	req := types.ChatRequest{
		Model:     "claude-3-opus",
		MaxTokens: 1000,
		Messages: []types.Message{
			{Role: "user", Content: "Hello"},
		},
	}

	result := chatRequestToAnthropic(req)

	if result.Model != "claude-3-opus" {
		t.Errorf("expected model 'claude-3-opus', got %q", result.Model)
	}
}

func TestChatRequestToAnthropic_ToolResultWithoutUser(t *testing.T) {
	req := types.ChatRequest{
		Model:     "claude-3.5-sonnet",
		MaxTokens: 1000,
		Messages: []types.Message{
			{Role: "user", Content: "Do something"},
			{Role: "assistant", ToolCalls: []types.ToolCall{
				{ID: "call_1", Type: "function", Function: types.ToolCallFn{Name: "search", Arguments: `{"q":"test"}`}},
			}},
			{Role: "tool", ToolCallID: "call_1", Content: "result"},
		},
	}

	result := chatRequestToAnthropic(req)

	lastMsg := result.Messages[len(result.Messages)-1]
	if lastMsg.Role != "user" {
		t.Errorf("expected last message to be user (wrap orphan tool results), got %q", lastMsg.Role)
	}
	hasToolResult := false
	for _, block := range lastMsg.Content {
		if block.Type == "tool_result" {
			hasToolResult = true
		}
	}
	if !hasToolResult {
		t.Error("expected tool_result block in orphan tool result message")
	}
}

func TestAnthropicResponseToChat_Usage(t *testing.T) {
	resp := anthropicResponse{
		ID:    "msg_xyz",
		Model: "claude-3.5-sonnet",
		Content: []anthropicBlock{
			{Type: "text", Text: "ok"},
		},
		StopReason: "end_turn",
		Usage: anthropicUsage{
			InputTokens:          100,
			OutputTokens:         50,
			CacheReadInputTokens: 75,
		},
	}

	result := anthropicResponseToChat(resp)

	if result.Usage.PromptTokens != 100 {
		t.Errorf("expected PromptTokens 100, got %d", result.Usage.PromptTokens)
	}
	if result.Usage.CompletionTokens != 50 {
		t.Errorf("expected CompletionTokens 50, got %d", result.Usage.CompletionTokens)
	}
	if result.Usage.TotalTokens != 150 {
		t.Errorf("expected TotalTokens 150, got %d", result.Usage.TotalTokens)
	}
	if result.Usage.PromptTokensDetails.CachedTokens != 75 {
		t.Errorf("expected CachedTokens 75, got %d", result.Usage.PromptTokensDetails.CachedTokens)
	}
}

func TestChatRequestToAnthropic_UserWithToolResults(t *testing.T) {
	req := types.ChatRequest{
		Model:     "claude-3.5-sonnet",
		MaxTokens: 1000,
		Messages: []types.Message{
			{Role: "user", Content: "Initial question"},
			{Role: "assistant", ToolCalls: []types.ToolCall{
				{ID: "c1", Type: "function", Function: types.ToolCallFn{Name: "read", Arguments: `{}`}},
			}},
			{Role: "tool", ToolCallID: "c1", Content: "file content"},
			{Role: "user", Content: "Now analyze it"},
		},
	}

	result := chatRequestToAnthropic(req)

	secondUser := result.Messages[2]
	if secondUser.Role != "user" {
		t.Fatalf("expected third message role 'user', got %q", secondUser.Role)
	}
	hasToolResult := false
	hasText := false
	for _, block := range secondUser.Content {
		if block.Type == "tool_result" {
			hasToolResult = true
		}
		if block.Type == "text" && block.Text == "Now analyze it" {
			hasText = true
		}
	}
	if !hasToolResult {
		t.Error("expected tool_result block in user message")
	}
	if !hasText {
		t.Error("expected text block in user message")
	}
}

func TestChatRequestToAnthropic_StreamFlag(t *testing.T) {
	req := types.ChatRequest{
		Model:     "claude-3.5-sonnet",
		MaxTokens: 1000,
		Stream:    true,
		Messages: []types.Message{
			{Role: "user", Content: "Hello"},
		},
	}

	result := chatRequestToAnthropic(req)

	if !result.Stream {
		t.Error("expected Stream to be true")
	}
}
