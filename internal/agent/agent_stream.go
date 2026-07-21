package agent

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
// message (content + any tool calls). Tool calls accumulated from the stream
// are returned to Run, which executes them exactly like the non-streaming
// path. Content deltas are emitted via OnToken as they arrive.
// If finish_reason is "length" and tool calls are present, returns an error
// to prevent executing truncated tool calls.
func (l *Loop) runStream(ctx context.Context, sp StreamProvider, req types.ChatRequest) (types.Message, error) {
	var streamSpan trace.Span
	if l.OtelEnabled {
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
	var usageCaptured bool

	// recordVerbose captures the full streamed response (content +
	// reasoning + tool calls) and the stream-termination path on the
	// llm.stream span so the dual-loop conversation is visible in Jaeger.
	// Gated on OtelVerbose — no work when verbose tracing is off.
	recordVerbose := func(path string) {
		if !l.OtelVerbose || streamSpan == nil {
			return
		}
		msg := l.assembleStreamed(content.String(), toolCallMap, reasoning.String())
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
				return l.checkTruncatedStream(content.String(), toolCallMap, finishReason, reasoning.String())
			}

			if !firstToken {
				firstToken = true
				if streamSpan != nil {
					streamSpan.SetAttributes(attribute.Int64("llm.ttft_ms", time.Since(start).Milliseconds()))
				}
			}

			if len(chunk.Choices) == 0 {
				// The final chunk may carry usage with no choices (pure usage marker).
				if chunk.Usage != nil {
					usageCaptured = true
					l.captureStreamUsage(chunk.Usage)
					if streamSpan != nil {
						l.setStreamUsageAttrs(streamSpan, chunk.Usage)
					}
				}
				continue
			}

			delta := chunk.Choices[0].Delta

			if delta.ReasoningContent != "" {
				reasoning.WriteString(delta.ReasoningContent)
				if l.OnThinking != nil {
					l.OnThinking(delta.ReasoningContent)
				}
			}

			if delta.Content != "" {
				content.WriteString(delta.Content)
				tokenCount++
				if l.OnToken != nil {
					l.OnToken(delta.Content)
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
				// finish_reason received — capture usage from this chunk
				// (providers like DeepSeek send usage in the same chunk as
				// the finish_reason), then drain any remaining chunks.
				if chunk.Usage != nil {
					usageCaptured = true
					l.captureStreamUsage(chunk.Usage)
					if streamSpan != nil {
						l.setStreamUsageAttrs(streamSpan, chunk.Usage)
					}
				}
				for chunk := range chunks {
					if chunk.Usage != nil {
						usageCaptured = true
						l.captureStreamUsage(chunk.Usage)
						if streamSpan != nil {
							l.setStreamUsageAttrs(streamSpan, chunk.Usage)
						}
					}
				}
				recordVerbose("finish_reason")
				if streamSpan != nil {
					observability.FinishStream(streamSpan, 0, tokenCount, len(toolCallMap))
					streamSpan.End()
				}
				return l.checkTruncatedStream(content.String(), toolCallMap, finishReason, reasoning.String())
			}

		case err := <-errs:
			if err != nil {
				if streamSpan != nil {
					observability.RecordError(streamSpan, err)
					streamSpan.End()
				}
				return types.Message{}, err
			}
			recordVerbose("errs_nil")
			if streamSpan != nil {
				observability.FinishStream(streamSpan, 0, tokenCount, len(toolCallMap))
				streamSpan.End()
			}
			return l.checkTruncatedStream(content.String(), toolCallMap, finishReason, reasoning.String())

		case <-ctx.Done():
			if streamSpan != nil {
				observability.RecordError(streamSpan, ctx.Err())
				streamSpan.End()
			}
			return types.Message{}, ctx.Err()
		}
	}
}

// checkTruncatedStream returns the assembled message or an error if the
// stream was truncated (finish_reason=length) with pending tool calls,
// or if the response was blocked by a content filter with no usable output.
func (l *Loop) checkTruncatedStream(content string, toolCallMap map[int]*types.ToolCall, finishReason string, reasoningContent string) (types.Message, error) {
	msg := l.assembleStreamed(content, toolCallMap, reasoningContent)
	if finishReason == "content_filter" && content == "" && len(msg.ToolCalls) == 0 {
		return types.Message{}, fmt.Errorf("streamed response blocked by content filter")
	}
	if finishReason == "length" && len(msg.ToolCalls) > 0 {
		return types.Message{}, fmt.Errorf("streamed response truncated (finish_reason=length), discarding %d tool calls", len(msg.ToolCalls))
	}
	if content == "" && len(msg.ToolCalls) == 0 {
		return types.Message{}, fmt.Errorf("streamed response produced no content (finish_reason=%s)", finishReason)
	}
	return msg, nil
}

// assembleStreamed builds the assistant message from accumulated stream state,
// ordering tool calls by their delta index.
func (l *Loop) assembleStreamed(content string, toolCalls map[int]*types.ToolCall, reasoningContent string) types.Message {
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
