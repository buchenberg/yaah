// Package types defines the core data structures for yaah's provider
// communication layer: chat messages, tool definitions, and provider
// request/response shapes. These follow the OpenAI Chat Completions
// format, which is the de-facto standard across providers.
package types

import "encoding/json"

// Message represents a single chat message in OpenAI format.
type Message struct {
	Role             string        `json:"role"`
	Content          string        `json:"content"`
	Refusal          string        `json:"refusal,omitempty"`
	ReasoningContent string        `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall    `json:"tool_calls,omitempty"`
	ToolCallID       string        `json:"tool_call_id,omitempty"`
	Name             string        `json:"name,omitempty"`
	CacheControl     *CacheControl `json:"cache_control,omitempty"`
	FinishReason     string        `json:"finish_reason,omitempty"`
	ResponseModel    string        `json:"response_model,omitempty"`
}

// CacheControl marks a message for Anthropic prompt caching.
type CacheControl struct {
	Type string `json:"type"`
}

// ToolCall represents a single tool call requested by the model.
type ToolCall struct {
	ID       string     `json:"id"`
	Index    int        `json:"index,omitempty"`
	Type     string     `json:"type"`
	Function ToolCallFn `json:"function"`
}

// ToolCallFn is the function within a tool call.
type ToolCallFn struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolFn defines the JSON Schema for a tool's parameters.
type ToolFn struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ToolDef wraps a tool function definition.
type ToolDef struct {
	Type     string `json:"type"`
	Function ToolFn `json:"function"`
}

// ChatRequest is the request body sent to /chat/completions.
type ChatRequest struct {
	Model          string          `json:"model"`
	Messages       []Message       `json:"messages"`
	Tools          []ToolDef       `json:"tools,omitempty"`
	Temperature    float64         `json:"temperature,omitempty"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
	Stream         bool            `json:"stream"`
	StreamOptions  *StreamOptions  `json:"stream_options,omitempty"`
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
}

// ResponseFormat controls structured output mode.
type ResponseFormat struct {
	Type string `json:"type"`
}

// StreamOptions configures streaming behaviour (e.g. include_usage).
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// ChatResponse is the response from /chat/completions.
type ChatResponse struct {
	ID      string   `json:"id"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage,omitempty"`
}

// Choice is a single completion choice.
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// Usage holds token usage statistics from the provider.
type Usage struct {
	PromptTokens            int                      `json:"prompt_tokens"`
	CompletionTokens        int                      `json:"completion_tokens"`
	TotalTokens             int                      `json:"total_tokens"`
	CompletionTokensDetails *CompletionTokensDetails `json:"completion_tokens_details,omitempty"`
	PromptTokensDetails     *PromptTokensDetails     `json:"prompt_tokens_details,omitempty"`
}

// CompletionTokensDetails breaks down where completion tokens were spent.
type CompletionTokensDetails struct {
	ReasoningTokens          int `json:"reasoning_tokens"`
	AudioTokens              int `json:"audio_tokens,omitempty"`
	AcceptedPredictionTokens int `json:"accepted_prediction_tokens,omitempty"`
	RejectedPredictionTokens int `json:"rejected_prediction_tokens,omitempty"`
}

// PromptTokensDetails breaks down prompt token usage.
type PromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
	AudioTokens  int `json:"audio_tokens,omitempty"`
}

// ToolResultMsg creates a tool result message to append to the conversation.
func ToolResultMsg(callID, name, content string) Message {
	return Message{
		Role:       "tool",
		ToolCallID: callID,
		Name:       name,
		Content:    content,
	}
}

// UserMsg creates a user message.
func UserMsg(content string) Message {
	return Message{Role: "user", Content: content}
}

// SystemMsg creates a system message.
func SystemMsg(content string) Message {
	return Message{Role: "system", Content: content}
}

// AssistantMsg creates an assistant message with optional tool calls.
func AssistantMsg(content string, toolCalls []ToolCall) Message {
	return Message{
		Role:      "assistant",
		Content:   content,
		ToolCalls: toolCalls,
	}
}
