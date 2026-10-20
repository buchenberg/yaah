package providers

import (
	"context"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/buchenberg/yaah/internal/types"
)

// anthropicStreamState carries translation state across the events of one
// stream: whether a message_delta was seen. A message_delta always carries
// the finish chunk for the stream, so the message_stop fallback synthesizes
// a finish reason only when the provider (or an Anthropic-compatible proxy)
// omitted the delta entirely.
type anthropicStreamState struct {
	sawMessageDelta bool
}

// SendStream sends a streaming chat request and returns a channel of
// chunks in yaah's StreamChunk shape. The SDK owns SSE decoding — including
// mid-stream `error` events, which surface from the stream as typed
// *anthropic.Error and are converted by anthropicAPIError so the retry
// ladder never replays blind. The channel is closed when the stream ends
// or an error occurs.
func (c *AnthropicClient) SendStream(ctx context.Context, req types.ChatRequest) (<-chan StreamChunk, <-chan error) {
	chunks := make(chan StreamChunk, 64)
	errs := make(chan error, 1)

	go func() {
		defer close(chunks)
		defer close(errs)

		stream := c.client.Messages.NewStreaming(ctx, c.lower(req), c.requestOpts(ctx)...)
		defer stream.Close()

		st := &anthropicStreamState{}
		for stream.Next() {
			chunk, err := translateAnthropicEvent(stream.Current(), st)
			if err != nil {
				errs <- err
				return
			}
			if chunk != nil {
				select {
				case chunks <- *chunk:
				case <-ctx.Done():
					return
				}
			}
		}
		if err := stream.Err(); err != nil {
			errs <- anthropicAPIError(err)
		}
	}()

	return chunks, errs
}

// translateAnthropicEvent converts one SDK stream event into a StreamChunk.
// A nil chunk means the event carries no yaah-visible payload. Mid-stream
// failures never reach this function — the SDK decoder turns `error`
// events into stream errors before they surface here.
func translateAnthropicEvent(event anthropic.MessageStreamEventUnion, st *anthropicStreamState) (*StreamChunk, error) {
	switch e := event.AsAny().(type) {
	case anthropic.MessageStartEvent:
		chunk := &StreamChunk{
			ID:    e.Message.ID,
			Model: e.Message.Model,
		}
		if u := e.Message.Usage; u.InputTokens > 0 || u.CacheReadInputTokens > 0 {
			chunk.Usage = &types.Usage{
				PromptTokens:        int(u.InputTokens), // uncached input only
				PromptTokensDetails: &types.PromptTokensDetails{CachedTokens: int(u.CacheReadInputTokens)},
				PromptIncludesCache: false,
			}
		}
		return chunk, nil

	case anthropic.ContentBlockStartEvent:
		idx := int(e.Index)
		switch e.ContentBlock.Type {
		case "text":
			return &StreamChunk{
				Choices: []StreamChoice{{Index: idx, Delta: StreamDelta{Role: "assistant"}}},
			}, nil
		case "thinking":
			return &StreamChunk{
				Choices: []StreamChoice{{Index: idx, Delta: StreamDelta{}}},
			}, nil
		case "tool_use":
			return &StreamChunk{
				Choices: []StreamChoice{{Index: idx, Delta: StreamDelta{
					ToolCalls: []types.ToolCall{{
						Index: idx,
						ID:    e.ContentBlock.ID,
						Type:  "function",
						Function: types.ToolCallFn{
							Name: e.ContentBlock.Name,
						},
					}},
				}}},
			}, nil
		}

	case anthropic.ContentBlockDeltaEvent:
		idx := int(e.Index)
		switch e.Delta.Type {
		case "text_delta":
			return &StreamChunk{
				Choices: []StreamChoice{{Index: idx, Delta: StreamDelta{Content: e.Delta.Text}}},
			}, nil
		case "thinking_delta":
			return &StreamChunk{
				Choices: []StreamChoice{{Index: idx, Delta: StreamDelta{ReasoningContent: e.Delta.Thinking}}},
			}, nil
		case "signature_delta":
			// Anthropic delivers the thinking-block signature AFTER the
			// thinking text. Losing it makes every streamed turn
			// unreplayable — the signature is opaque and cannot be rebuilt.
			return &StreamChunk{
				Choices: []StreamChoice{{Index: idx, Delta: StreamDelta{ReasoningSignature: e.Delta.Signature}}},
			}, nil
		case "input_json_delta":
			return &StreamChunk{
				Choices: []StreamChoice{{Index: idx, Delta: StreamDelta{
					ToolCalls: []types.ToolCall{{
						Index: idx,
						Function: types.ToolCallFn{
							Arguments: e.Delta.PartialJSON,
						},
					}},
				}}},
			}, nil
		}

	case anthropic.MessageDeltaEvent:
		finishReason := "stop"
		st.sawMessageDelta = true
		if e.Delta.StopReason != "" {
			switch e.Delta.StopReason {
			case "tool_use":
				finishReason = "tool_calls"
			case "max_tokens":
				// Match the non-streaming path (raiseAnthropicResponse):
				// a truncated response must stay visible as "length"
				// so downstream guards can discard partial tool calls.
				finishReason = "length"
			}
		}
		chunk := &StreamChunk{
			Choices: []StreamChoice{{
				Delta:        StreamDelta{},
				FinishReason: &finishReason,
			}},
		}
		if u := e.Usage; u.InputTokens > 0 || u.OutputTokens > 0 || u.CacheReadInputTokens > 0 {
			chunk.Usage = &types.Usage{
				PromptTokens:        int(u.InputTokens), // uncached input only
				CompletionTokens:    int(u.OutputTokens),
				TotalTokens:         int(u.InputTokens + u.OutputTokens),
				PromptTokensDetails: &types.PromptTokensDetails{CachedTokens: int(u.CacheReadInputTokens)},
				PromptIncludesCache: false,
			}
		}
		return chunk, nil

	case anthropic.MessageStopEvent:
		// Well-formed Anthropic streams always carry a message_delta
		// with stop_reason before message_stop. Some Anthropic-compatible
		// proxies skip it; without this fallback a fully assembled
		// response would be discarded as a premature stream close.
		if !st.sawMessageDelta {
			finishReason := "stop"
			return &StreamChunk{
				Choices: []StreamChoice{{
					Delta:        StreamDelta{},
					FinishReason: &finishReason,
				}},
			}, nil
		}
	}

	return nil, nil
}
