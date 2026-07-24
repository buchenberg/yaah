package llm

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/buchenberg/yaah/internal/observability"
	"github.com/buchenberg/yaah/internal/types"
)

// runStream handles a streaming request and returns the assembled assistant
// message, usage, and any error.
func (c *Client) runStream(ctx context.Context, sp StreamProvider, req types.ChatRequest) (types.Message, types.Usage, error) {
	var streamSpan trace.Span
	if c.OtelEnabled {
		ctx, streamSpan = observability.StartStream(ctx, req.Model)
	}

	start := time.Now()
	chunks, errs := sp.SendStream(ctx, req)

	var content strings.Builder
	var reasoning strings.Builder
	toolCallMap := make(map[int]*types.ToolCall)
	var finishReason string
	var firstToken bool
	var tokenCount int
	var totalUsage types.Usage
	var usageCaptured bool

	recordVerbose := func(path string) {
		if !c.OtelVerbose || streamSpan == nil {
			return
		}
		msg := assembleStreamed(content.String(), toolCallMap, reasoning.String())
		observability.RecordAssistantResponse(streamSpan, msg, finishReason)
		observability.RecordStreamEnd(streamSpan, path, finishReason, usageCaptured, len(msg.Content), len(msg.ToolCalls))
	}

	for {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				recordVerbose("channel_closed")
				if streamSpan != nil {
					observability.FinishStream(streamSpan, 0, tokenCount, len(toolCallMap))
					streamSpan.End()
				}
				return checkTruncatedStream(content.String(), toolCallMap, finishReason, reasoning.String(), totalUsage)
			}

			if !firstToken {
				firstToken = true
				if streamSpan != nil {
					streamSpan.SetAttributes(attribute.Int64("llm.ttft_ms", time.Since(start).Milliseconds()))
				}
			}

			if len(chunk.Choices) == 0 {
				if chunk.Usage != nil {
					usageCaptured = true
					addStreamUsage(&totalUsage, chunk.Usage)
					if streamSpan != nil {
						setStreamUsageAttrs(streamSpan, chunk.Usage)
					}
				}
				continue
			}

			delta := chunk.Choices[0].Delta

			if delta.ReasoningContent != "" {
				reasoning.WriteString(delta.ReasoningContent)
				if c.OnThinking != nil {
					c.OnThinking(delta.ReasoningContent)
				}
			}

			if delta.Content != "" {
				content.WriteString(delta.Content)
				tokenCount++
				if c.OnToken != nil {
					c.OnToken(delta.Content)
				}
			}

			for _, tc := range delta.ToolCalls {
				idx := tc.Index
				if existing, ok := toolCallMap[idx]; ok {
					existing.Function.Arguments += tc.Function.Arguments
					if tc.ID != "" {
						existing.ID = tc.ID
					}
					if tc.Function.Name != "" {
						existing.Function.Name = tc.Function.Name
					}
				} else {
					newTC := types.ToolCall{
						Index: idx,
						ID:    tc.ID,
						Type:  tc.Type,
						Function: types.ToolCallFn{
							Name:      tc.Function.Name,
							Arguments: tc.Function.Arguments,
						},
					}
					toolCallMap[idx] = &newTC
				}
			}

			if chunk.Choices[0].FinishReason != nil {
				finishReason = *chunk.Choices[0].FinishReason
			}

			if finishReason != "" {
				if chunk.Usage != nil {
					usageCaptured = true
					addStreamUsage(&totalUsage, chunk.Usage)
					if streamSpan != nil {
						setStreamUsageAttrs(streamSpan, chunk.Usage)
					}
				}
				for chunk := range chunks {
					if chunk.Usage != nil {
						usageCaptured = true
						addStreamUsage(&totalUsage, chunk.Usage)
						if streamSpan != nil {
							setStreamUsageAttrs(streamSpan, chunk.Usage)
						}
					}
				}
				recordVerbose("finish_reason")
				if streamSpan != nil {
					observability.FinishStream(streamSpan, 0, tokenCount, len(toolCallMap))
					streamSpan.End()
				}
				return checkTruncatedStream(content.String(), toolCallMap, finishReason, reasoning.String(), totalUsage)
			}

		case err := <-errs:
			if err != nil {
				if streamSpan != nil {
					observability.RecordError(streamSpan, err)
					streamSpan.End()
				}
				return types.Message{}, totalUsage, err
			}
			recordVerbose("errs_nil")
			if streamSpan != nil {
				observability.FinishStream(streamSpan, 0, tokenCount, len(toolCallMap))
				streamSpan.End()
			}
			return checkTruncatedStream(content.String(), toolCallMap, finishReason, reasoning.String(), totalUsage)

		case <-ctx.Done():
			if streamSpan != nil {
				observability.RecordError(streamSpan, ctx.Err())
				streamSpan.End()
			}
			return types.Message{}, totalUsage, ctx.Err()
		}
	}
}

// checkTruncatedStream validates the assembled streamed message and returns it
// with the accumulated usage, or an error if the stream was truncated or blocked.
func checkTruncatedStream(content string, toolCallMap map[int]*types.ToolCall, finishReason string, reasoningContent string, usage types.Usage) (types.Message, types.Usage, error) {
	msg := assembleStreamed(content, toolCallMap, reasoningContent)

	if msg.Content != "" {
		if cleaned, dsmlCalls, ok := parseDSMLToolCalls(msg.Content); ok {
			msg.Content = cleaned
			msg.ToolCalls = append(msg.ToolCalls, dsmlCalls...)
		}
	}

	if finishReason == "content_filter" && content == "" && len(msg.ToolCalls) == 0 {
		return types.Message{}, usage, fmt.Errorf("streamed response blocked by content filter")
	}
	if finishReason == "length" && len(msg.ToolCalls) > 0 {
		return types.Message{}, usage, fmt.Errorf("streamed response truncated (finish_reason=length), discarding %d tool calls", len(msg.ToolCalls))
	}
	if msg.Content == "" && len(msg.ToolCalls) == 0 {
		return types.Message{}, usage, fmt.Errorf("streamed response produced no content (finish_reason=%s)", finishReason)
	}
	return msg, usage, nil
}

// assembleStreamed builds the assistant message from accumulated stream state.
func assembleStreamed(content string, toolCalls map[int]*types.ToolCall, reasoningContent string) types.Message {
	msg := types.Message{
		Role:             "assistant",
		Content:          content,
		ReasoningContent: reasoningContent,
	}
	if len(toolCalls) > 0 {
		indices := make([]int, 0, len(toolCalls))
		for idx := range toolCalls {
			indices = append(indices, idx)
		}
		sort.Ints(indices)
		for _, idx := range indices {
			msg.ToolCalls = append(msg.ToolCalls, *toolCalls[idx])
		}
	}
	return msg
}

// addStreamUsage accumulates streaming usage into a running total.
func addStreamUsage(total *types.Usage, u *types.Usage) {
	total.PromptTokens += u.PromptTokens
	total.CompletionTokens += u.CompletionTokens
	total.TotalTokens += u.TotalTokens
}

// setStreamUsageAttrs enriches a stream span with token detail attributes.
func setStreamUsageAttrs(span trace.Span, u *types.Usage) {
	span.SetAttributes(
		attribute.Int("llm.prompt_tokens", u.PromptTokens),
		attribute.Int("llm.completion_tokens", u.CompletionTokens),
		attribute.Int("llm.total_tokens", u.TotalTokens),
	)
	if d := u.CompletionTokensDetails; d != nil && d.ReasoningTokens > 0 {
		span.SetAttributes(attribute.Int("llm.reasoning_tokens", d.ReasoningTokens))
	}
	if d := u.PromptTokensDetails; d != nil && d.CachedTokens > 0 {
		span.SetAttributes(attribute.Int("llm.cached_prompt_tokens", d.CachedTokens))
	}
}
