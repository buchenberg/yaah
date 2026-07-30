package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/types"
)

type anthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	System      []anthropicBlock   `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
	Stream      bool               `json:"stream"`
	Temperature float64            `json:"temperature,omitempty"`
}

type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

type anthropicBlock struct {
	Type         string              `json:"type"`
	Text         string              `json:"text,omitempty"`
	ID           string              `json:"id,omitempty"`
	Name         string              `json:"name,omitempty"`
	Input        json.RawMessage     `json:"input,omitempty"`
	ToolUseID    string              `json:"tool_use_id,omitempty"`
	Content      string              `json:"content,omitempty"`
	Thinking     string              `json:"thinking,omitempty"`
	CacheControl *types.CacheControl `json:"cache_control,omitempty"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthropicResponse struct {
	ID         string           `json:"id"`
	Model      string           `json:"model"`
	Role       string           `json:"role"`
	Content    []anthropicBlock `json:"content"`
	StopReason string           `json:"stop_reason"`
	Usage      anthropicUsage   `json:"usage"`
}

type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// AnthropicClient sends requests to the Anthropic Messages API.
type AnthropicClient struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

// NewAnthropicClient creates a new client targeting the Anthropic Messages API.
func NewAnthropicClient(baseURL, apiKey string, timeoutSeconds int) *AnthropicClient {
	to := time.Duration(timeoutSeconds) * time.Second
	if timeoutSeconds < 0 {
		to = 120 * time.Second
	}
	return &AnthropicClient{
		baseURL: baseURL,
		apiKey:  apiKey,
		client: &http.Client{
			Timeout: to,
		},
	}
}

// Send translates a ChatRequest to the Anthropic Messages API format and returns the response.
func (c *AnthropicClient) Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	antReq := lowerAnthropicRequest(req)
	body, err := json.Marshal(antReq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := c.baseURL + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	setSessionHeaders(httpReq, SessionIDFromContext(ctx))

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("provider returned %d: %s", resp.StatusCode, string(respBody))
	}

	var antResp anthropicResponse
	if err := json.Unmarshal(respBody, &antResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return raiseAnthropicResponse(antResp), nil
}

// ListModels is not supported by the Anthropic Messages API.
func (c *AnthropicClient) ListModels(ctx context.Context) ([]string, error) {
	return nil, fmt.Errorf("anthropic Messages API does not support model listing")
}

func lowerAnthropicRequest(req types.ChatRequest) anthropicRequest {
	var sysBlocks []anthropicBlock
	var convMessages []types.Message

	for _, m := range req.Messages {
		if m.Role == "system" {
			block := anthropicBlock{Type: "text", Text: m.Content}
			if m.CacheControl != nil {
				block.CacheControl = m.CacheControl
			}
			sysBlocks = append(sysBlocks, block)
		} else {
			convMessages = append(convMessages, m)
		}
	}

	var msgs []anthropicMessage
	var pendingToolResults []anthropicBlock

	for _, m := range convMessages {
		switch m.Role {
		case "user":
			blocks := make([]anthropicBlock, 0, 1+len(pendingToolResults))
			blocks = append(blocks, pendingToolResults...)
			pendingToolResults = nil

			if m.Content != "" {
				block := anthropicBlock{Type: "text", Text: m.Content}
				if m.CacheControl != nil {
					block.CacheControl = m.CacheControl
				}
				blocks = append(blocks, block)
			}

			if len(msgs) > 0 && msgs[len(msgs)-1].Role == "user" {
				msgs[len(msgs)-1].Content = append(msgs[len(msgs)-1].Content, blocks...)
			} else if len(blocks) > 0 {
				msgs = append(msgs, anthropicMessage{Role: "user", Content: blocks})
			}

		case "assistant":
			blocks := make([]anthropicBlock, 0, 1+len(m.ToolCalls))

			if m.ReasoningContent != "" {
				blocks = append(blocks, anthropicBlock{Type: "thinking", Thinking: m.ReasoningContent})
			}
			if m.Content != "" {
				blocks = append(blocks, anthropicBlock{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, anthropicBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Function.Name,
					Input: json.RawMessage(tc.Function.Arguments),
				})
			}

			if len(blocks) > 0 {
				msgs = append(msgs, anthropicMessage{Role: "assistant", Content: blocks})
			}

		case "tool":
			pendingToolResults = append(pendingToolResults, anthropicBlock{
				Type:      "tool_result",
				ToolUseID: m.ToolCallID,
				Content:   m.Content,
			})
		}
	}

	if len(pendingToolResults) > 0 {
		msgs = append(msgs, anthropicMessage{Role: "user", Content: pendingToolResults})
	}

	var antTools []anthropicTool
	for _, t := range req.Tools {
		antTools = append(antTools, anthropicTool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			InputSchema: t.Function.Parameters,
		})
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	return anthropicRequest{
		Model:       req.Model,
		MaxTokens:   maxTokens,
		System:      sysBlocks,
		Messages:    msgs,
		Tools:       antTools,
		Stream:      req.Stream,
		Temperature: req.Temperature,
	}
}

func raiseAnthropicResponse(resp anthropicResponse) *types.ChatResponse {
	var content strings.Builder
	var reasoning strings.Builder
	var toolCalls []types.ToolCall

	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			content.WriteString(block.Text)
		case "thinking":
			reasoning.WriteString(block.Thinking)
		case "tool_use":
			toolCalls = append(toolCalls, types.ToolCall{
				ID:   block.ID,
				Type: "function",
				Function: types.ToolCallFn{
					Name:      block.Name,
					Arguments: string(block.Input),
				},
			})
		}
	}

	finishReason := resp.StopReason
	switch finishReason {
	case "end_turn":
		finishReason = "stop"
	case "tool_use":
		finishReason = "tool_calls"
	case "max_tokens":
		finishReason = "length"
	case "stop_sequence":
		finishReason = "stop"
	}

	return &types.ChatResponse{
		ID:    resp.ID,
		Model: resp.Model,
		Choices: []types.Choice{{
			Index: 0,
			Message: types.Message{
				Role:             "assistant",
				Content:          content.String(),
				ReasoningContent: reasoning.String(),
				ToolCalls:        toolCalls,
			},
			FinishReason: finishReason,
		}},
		Usage: types.Usage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
			PromptTokensDetails: &types.PromptTokensDetails{
				CachedTokens: resp.Usage.CacheReadInputTokens,
			},
		},
	}
}
