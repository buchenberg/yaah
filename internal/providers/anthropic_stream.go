package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/buchenberg/yaah/internal/types"
)

type anthropicSSEEvent struct {
	Type         string                    `json:"type"`
	Index        *int                      `json:"index,omitempty"`
	Delta        *anthropicSSEDelta        `json:"delta,omitempty"`
	Message      *anthropicSSEMessage      `json:"message,omitempty"`
	ContentBlock *anthropicSSEContentBlock `json:"content_block,omitempty"`
	Usage        *anthropicUsage           `json:"usage,omitempty"`
}

type anthropicSSEDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	Thinking    string `json:"thinking,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	StopReason  string `json:"stop_reason,omitempty"`
}

type anthropicSSEMessage struct {
	ID    string               `json:"id"`
	Model string               `json:"model"`
	Usage *anthropicUsageInput `json:"usage,omitempty"`
}

type anthropicUsageInput struct {
	InputTokens              int `json:"input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

type anthropicSSEContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

func (c *AnthropicClient) SendStream(ctx context.Context, req types.ChatRequest) (<-chan StreamChunk, <-chan error) {
	antReq := chatRequestToAnthropic(req)
	antReq.Stream = true

	chunks := make(chan StreamChunk, 64)
	errs := make(chan error, 1)

	go func() {
		defer close(chunks)
		defer close(errs)

		body, err := json.Marshal(antReq)
		if err != nil {
			errs <- fmt.Errorf("marshal request: %w", err)
			return
		}

		url := c.baseURL + "/v1/messages"
		httpReq, err := newAnthropicHTTPReq(c, ctx, url, body)
		if err != nil {
			errs <- err
			return
		}

		resp, err := c.client.Do(httpReq)
		if err != nil {
			errs <- fmt.Errorf("send request: %w", err)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			errs <- fmt.Errorf("provider returned %d: %s", resp.StatusCode, string(respBody))
			return
		}

		toolArgs := make(map[int]string)

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

		var dataLines []string

		for scanner.Scan() {
			line := scanner.Text()

			if strings.HasPrefix(line, "event: ") {
			} else if strings.HasPrefix(line, "data: ") {
				dataLines = append(dataLines, strings.TrimPrefix(line, "data: "))
			} else if line == "" && len(dataLines) > 0 {
				chunk := processAnthropicSSEEvent(strings.Join(dataLines, "\n"), toolArgs)
				if chunk != nil {
					select {
					case chunks <- *chunk:
					case <-ctx.Done():
						return
					}
				}
				dataLines = nil
			}
		}

		if len(dataLines) > 0 {
			chunk := processAnthropicSSEEvent(strings.Join(dataLines, "\n"), toolArgs)
			if chunk != nil {
				select {
				case chunks <- *chunk:
				case <-ctx.Done():
				}
			}
		}

		if err := scanner.Err(); err != nil {
			errs <- fmt.Errorf("read stream: %w", err)
		}
	}()

	return chunks, errs
}

func newAnthropicHTTPReq(c *AnthropicClient, ctx context.Context, url string, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Accept", "text/event-stream")
	setSessionHeaders(req, SessionIDFromContext(ctx))
	return req, nil
}

func processAnthropicSSEEvent(data string, toolArgs map[int]string) *StreamChunk {
	if data == "" {
		return nil
	}

	var evt anthropicSSEEvent
	if err := json.Unmarshal([]byte(data), &evt); err != nil {
		return nil
	}

	switch evt.Type {
	case "message_start":
		if evt.Message != nil {
			chunk := &StreamChunk{
				ID:    evt.Message.ID,
				Model: evt.Message.Model,
			}
			if evt.Message.Usage != nil {
				chunk.Usage = &types.Usage{
					PromptTokens: evt.Message.Usage.InputTokens,
					PromptTokensDetails: &types.PromptTokensDetails{
						CachedTokens: evt.Message.Usage.CacheReadInputTokens,
					},
				}
			}
			return chunk
		}

	case "content_block_start":
		if evt.ContentBlock == nil || evt.Index == nil {
			return nil
		}
		idx := *evt.Index
		switch evt.ContentBlock.Type {
		case "text":
			return &StreamChunk{
				Choices: []StreamChoice{{
					Index: idx,
					Delta: StreamDelta{Role: "assistant"},
				}},
			}
		case "thinking":
			return &StreamChunk{
				Choices: []StreamChoice{{
					Index: idx,
					Delta: StreamDelta{},
				}},
			}
		case "tool_use":
			toolArgs[idx] = ""
			return &StreamChunk{
				Choices: []StreamChoice{{
					Index: idx,
					Delta: StreamDelta{
						ToolCalls: []types.ToolCall{{
							Index: idx,
							ID:    evt.ContentBlock.ID,
							Type:  "function",
							Function: types.ToolCallFn{
								Name: evt.ContentBlock.Name,
							},
						}},
					},
				}},
			}
		}

	case "content_block_delta":
		if evt.Index == nil || evt.Delta == nil {
			return nil
		}
		idx := *evt.Index
		switch evt.Delta.Type {
		case "text_delta":
			return &StreamChunk{
				Choices: []StreamChoice{{
					Index: idx,
					Delta: StreamDelta{Content: evt.Delta.Text},
				}},
			}
		case "thinking_delta":
			return &StreamChunk{
				Choices: []StreamChoice{{
					Index: idx,
					Delta: StreamDelta{ReasoningContent: evt.Delta.Thinking},
				}},
			}
		case "input_json_delta":
			toolArgs[idx] += evt.Delta.PartialJSON
			return &StreamChunk{
				Choices: []StreamChoice{{
					Index: idx,
					Delta: StreamDelta{
						ToolCalls: []types.ToolCall{{
							Index: idx,
							Function: types.ToolCallFn{
								Arguments: evt.Delta.PartialJSON,
							},
						}},
					},
				}},
			}
		}

	case "message_delta":
		finishReason := "stop"
		if evt.Delta != nil && evt.Delta.StopReason == "tool_use" {
			finishReason = "tool_calls"
		}
		chunk := &StreamChunk{
			Choices: []StreamChoice{{
				Delta:        StreamDelta{},
				FinishReason: &finishReason,
			}},
		}
		if evt.Usage != nil {
			chunk.Usage = &types.Usage{
				PromptTokens:     evt.Usage.InputTokens,
				CompletionTokens: evt.Usage.OutputTokens,
				TotalTokens:      evt.Usage.InputTokens + evt.Usage.OutputTokens,
				PromptTokensDetails: &types.PromptTokensDetails{
					CachedTokens: evt.Usage.CacheReadInputTokens,
				},
			}
		}
		return chunk
	}

	return nil
}
