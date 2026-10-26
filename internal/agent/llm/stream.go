package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/buchenberg/yaah/internal/tools"
	"go.opentelemetry.io/otel/trace"

	"github.com/buchenberg/yaah/internal/observability"
	"github.com/buchenberg/yaah/internal/types"
)

// ErrPrematureStreamClose signals that the provider closed the stream
// before sending a finish_reason chunk. No well-formed stream ends this
// way (OpenAI-compatible and Anthropic both emit a terminal reason), so
// the assembled partial response is discarded and the request is replayed
// by Client.Call instead of being treated as a final answer.
var ErrPrematureStreamClose = errors.New("stream closed before finish_reason")

// runStream handles a streaming request and returns the assembled assistant
// message, finish reason, response model, usage, and any error.
func (c *Client) runStream(ctx context.Context, sp StreamProvider, req types.ChatRequest) (types.Message, string, string, types.Usage, error) {
	var streamSpan trace.Span
	if c.OtelEnabled {
		ctx, streamSpan = observability.StartStream(ctx, req.Model)
		if c.OtelVerbose {
			observability.RecordSystemPrompt(streamSpan, observability.SystemContent(req.Messages))
		}
	}

	start := time.Now()
	chunks, errs := sp.SendStream(ctx, req)

	// Beat while waiting for the first chunk: nothing drives the
	// chunk-based heartbeat yet, so a slow provider queue or model load
	// would starve the stuck-child watchdog (yaah-1ep). Stopped once the
	// first chunk arrives — from there, per-chunk liveness takes over so
	// a genuinely stalled stream is still detectable.
	stopWaitHB := c.startWaitHeartbeat(ctx)
	defer stopWaitHB()

	var content strings.Builder
	var reasoning strings.Builder
	var signature strings.Builder
	var dsmlFilter dsmlTokenFilter
	// Releasing the held tail is only correct for a stream whose message is
	// actually delivered: a failed attempt is retried, the same bytes stream
	// again, and the view would show the fragment twice.
	var streamDelivered bool
	defer func() {
		if !streamDelivered || c.OnToken == nil {
			return
		}
		if tail := dsmlFilter.flush(); tail != "" {
			c.OnToken(tail)
		}
	}()
	toolCallMap := make(map[int]*types.ToolCall)
	var finishReason string
	var responseModel string
	var firstToken bool
	var tokenCount int
	var totalUsage types.Usage
	var usageCaptured bool

	recordVerbose := func(path string) {
		if !c.OtelVerbose || streamSpan == nil {
			return
		}
		msg := assembleStreamed(content.String(), toolCallMap, reasoning.String(), signature.String())
		observability.RecordAssistantResponse(streamSpan, msg, finishReason)
		observability.RecordStreamEnd(streamSpan, path, finishReason, usageCaptured, len(msg.Content), len(msg.ToolCalls))
	}

	hbInterval := c.heartbeatInterval()
	var lastHeartbeat time.Time
	emitHeartbeat := func() {
		if time.Since(lastHeartbeat) > hbInterval {
			tools.SendHeartbeat(ctx)
			lastHeartbeat = time.Now()
		}
	}

	// drainPending consumes any chunks the producer buffered before it pushed
	// an error or the context ended. Both select arms can win while a content
	// chunk is still unread (Go picks uniformly among ready cases), which would
	// discard text the model already emitted. Non-blocking by construction.
	drainPending := func() {
	drain:
		// Bounded: a producer that keeps refilling the buffer must not turn a
		// failure path into an unbounded read loop that outlives the deadline.
		for drained := 0; drained < 1024; drained++ {
			select {
			case chunk, ok := <-chunks:
				if !ok {
					break drain
				}
				if chunk.Usage != nil {
					usageCaptured = true
					addStreamUsage(&totalUsage, chunk.Usage)
				}
				if len(chunk.Choices) == 0 {
					continue
				}
				delta := chunk.Choices[0].Delta
				if delta.ReasoningContent != "" {
					reasoning.WriteString(delta.ReasoningContent)
				}
				// Providers may deliver the signature in its own delta,
				// after the reasoning text has finished (DeepSeek's
				// Anthropic-compatible stream does exactly this), so it
				// cannot be captured inside the ReasoningContent guard.
				// Appended rather than reset: a signature split across
				// deltas must not keep only its last fragment.
				if delta.ReasoningSignature != "" {
					signature.WriteString(delta.ReasoningSignature)
				}
				if delta.Content != "" {
					content.WriteString(delta.Content)
					tokenCount++
				}
			default:
				break drain
			}
		}
	}

	for {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				// Provider goroutines push the real read/protocol error
				// onto errs before closing chunks, which makes both
				// select cases ready at once — and Go picks uniformly
				// at random. Drain errs first so a transport failure
				// (timeout, reset, scanner overflow) is not masked by
				// the generic premature-close synthesized below.
				select {
				case err := <-errs:
					if err != nil {
						if streamSpan != nil {
							observability.RecordError(streamSpan, err)
							streamSpan.End()
						}
						drainPending()
						return partialStreamedMessage(content.String()), "", responseModel, totalUsage, err
					}
				default:
				}
				recordVerbose("channel_closed")
				streamDelivered = true
				if streamSpan != nil {
					observability.FinishStream(streamSpan, 0, tokenCount, len(toolCallMap))
					streamSpan.End()
				}
				return checkTruncatedStream(content.String(), toolCallMap, finishReason, responseModel, reasoning.String(), signature.String(), totalUsage, &c.dialectSeq, req.Tools)
			}

			// Any chunk counts as liveness, not just content deltas:
			// reasoning-only and tool-call chunks prove the stream is
			// alive. Counting only content starves the stuck-child
			// watchdog during long thinking phases of reasoning models
			// (yaah-1ep). The first chunk also ends the TTFT wait
			// ticker.
			stopWaitHB()
			emitHeartbeat()

			if !firstToken {
				firstToken = true
				if streamSpan != nil {
					streamSpan.SetAttributes(attribute.Int64("llm.ttft_ms", time.Since(start).Milliseconds()))
				}
			}

			if responseModel == "" && chunk.Model != "" {
				responseModel = chunk.Model
			}

			if len(chunk.Choices) == 0 {
				if chunk.Usage != nil {
					usageCaptured = true
					addStreamUsage(&totalUsage, chunk.Usage)
					if streamSpan != nil {
						setStreamUsageAttrs(streamSpan, chunk.Usage, &totalUsage)
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
			// See the drain loop above: the signature may arrive on its
			// own, after the reasoning text, so it is captured
			// independently of ReasoningContent and appended for the
			// same split-delta reason.
			if delta.ReasoningSignature != "" {
				signature.WriteString(delta.ReasoningSignature)
			}

			if delta.Content != "" {
				content.WriteString(delta.Content)
				tokenCount++
				if c.OnToken != nil {
					if clean := dsmlFilter.filterToken(delta.Content); clean != "" {
						c.OnToken(clean)
					}
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
						setStreamUsageAttrs(streamSpan, chunk.Usage, &totalUsage)
					}
				}
				for chunk := range chunks {
					if chunk.Usage != nil {
						usageCaptured = true
						addStreamUsage(&totalUsage, chunk.Usage)
						if streamSpan != nil {
							setStreamUsageAttrs(streamSpan, chunk.Usage, &totalUsage)
						}
					}
				}
				recordVerbose("finish_reason")
				if streamSpan != nil {
					observability.FinishStream(streamSpan, 0, tokenCount, len(toolCallMap))
					streamSpan.End()
				}
				return checkTruncatedStream(content.String(), toolCallMap, finishReason, responseModel, reasoning.String(), signature.String(), totalUsage, &c.dialectSeq, req.Tools)
			}

		case err, ok := <-errs:
			if !ok {
				// errs closed without an error. Do NOT end the stream here: a
				// buffered chunk — possibly the finish chunk — may still be
				// pending, and Go picks uniformly among ready cases, so this arm
				// can win the race and discard it (a flaky "premature close" on
				// a perfectly good response). The chunks arm already drains errs
				// for a real error; this is the symmetric guard. Drop errs from
				// the select so the loop waits on chunks — the real end signal —
				// rather than busy-spinning on a closed channel. (A provider
				// that closes errs but never chunks violates the contract; the
				// wait then ends only on ctx cancellation, which is the correct
				// place to notice that.)
				errs = nil
				continue
			}
			if err != nil {
				if streamSpan != nil {
					observability.RecordError(streamSpan, err)
					streamSpan.End()
				}
				drainPending()
				return partialStreamedMessage(content.String()), "", "", totalUsage, err
			}
			recordVerbose("errs_nil")
			streamDelivered = true
			if streamSpan != nil {
				observability.FinishStream(streamSpan, 0, tokenCount, len(toolCallMap))
				streamSpan.End()
			}
			return checkTruncatedStream(content.String(), toolCallMap, finishReason, responseModel, reasoning.String(), signature.String(), totalUsage, &c.dialectSeq, req.Tools)

		case <-ctx.Done():
			if streamSpan != nil {
				observability.RecordError(streamSpan, ctx.Err())
				streamSpan.End()
			}
			drainPending()
			return partialStreamedMessage(content.String()), "", "", totalUsage, ctx.Err()
		}
	}
}

// checkTruncatedStream validates the assembled streamed message and returns it
// with finish reason, response model, and accumulated usage, or an error if the
// stream was truncated or blocked.
func checkTruncatedStream(content string, toolCallMap map[int]*types.ToolCall, finishReason string, responseModel string, reasoningContent string, reasoningSignature string, usage types.Usage, dialectSeq *int, toolDefs []types.ToolDef) (types.Message, string, string, types.Usage, error) {
	msg := assembleStreamed(content, toolCallMap, reasoningContent, reasoningSignature)

	// Filter out tool calls with invalid JSON arguments. Anthropic will
	// reject requests containing tool calls with invalid JSON in their
	// arguments field.
	var validToolCalls []types.ToolCall
	for _, tc := range msg.ToolCalls {
		if json.Valid([]byte(tc.Function.Arguments)) {
			validToolCalls = append(validToolCalls, tc)
		}
	}
	msg.ToolCalls = validToolCalls

	// A stream that ends without a finish_reason was cut by the provider
	// mid-response. The partial content (and any half-assembled tool
	// calls) must not be treated as a final answer — surface a retryable
	// error instead of silently dropping whatever the model was about to
	// do next.
	if finishReason == "" {
		return types.Message{}, "", responseModel, usage, fmt.Errorf(
			"%w: %d content chars and %d tool calls discarded",
			ErrPrematureStreamClose, len(content), len(msg.ToolCalls))
	}

	// Dialect recovery: tool-call markup leaked into content (DSML or the
	// attribute style) is re-parsed into structured tool calls so it
	// executes instead of surfacing as the answer (issue #314). Markup
	// that cannot be safely synthesized (cut mid-parameter, missing a
	// required argument) fails the turn explicitly instead of executing
	// a partial call or silently dropping one.
	if msg.Content != "" {
		if cleaned, recoveredCalls, rejected, ok := recoverDialectToolCalls(msg.Content, dialectSeq, toolDefs); ok {
			if len(rejected) > 0 {
				return types.Message{}, finishReason, responseModel, usage, fmt.Errorf("streamed tool-call markup could not be safely recovered: %s", strings.Join(rejected, "; "))
			}
			msg.Content = cleaned
			msg.ToolCalls = append(msg.ToolCalls, recoveredCalls...)
		}
	}

	if finishReason == "content_filter" && content == "" && len(msg.ToolCalls) == 0 {
		return types.Message{}, finishReason, responseModel, usage, fmt.Errorf("streamed response blocked by content filter")
	}
	if finishReason == "length" && len(msg.ToolCalls) > 0 {
		return types.Message{}, finishReason, responseModel, usage, fmt.Errorf("streamed response truncated (finish_reason=length), discarding %d tool calls", len(msg.ToolCalls))
	}
	if msg.Content == "" && len(msg.ToolCalls) == 0 {
		if finishReason == "length" {
			return types.Message{}, finishReason, responseModel, usage, fmt.Errorf("streamed response produced no content (finish_reason=length): output token budget exhausted before any content — the model may have spent max_tokens on reasoning; raise max_tokens for this request (provider default or caller cap)")
		}
		return types.Message{}, finishReason, responseModel, usage, fmt.Errorf("streamed response produced no content (finish_reason=%s)", finishReason)
	}
	return msg, finishReason, responseModel, usage, nil
}

// partialStreamedMessage preserves the text the model produced before a stream
// failed. A deadline that lands mid-completion would otherwise erase work that
// the user (or the orchestrator judging the sub-agent) can still read, and the
// caller could not tell a lost partial from a model that produced nothing.
//
// Content only: tool calls cut mid-stream have incomplete arguments and must
// never look executable.
func partialStreamedMessage(content string) types.Message {
	if content == "" {
		return types.Message{}
	}
	return types.Message{Role: "assistant", Content: content}
}

// assembleStreamed builds the assistant message from accumulated stream state.
func assembleStreamed(content string, toolCalls map[int]*types.ToolCall, reasoningContent string, reasoningSignature string) types.Message {
	msg := types.Message{
		Role:               "assistant",
		Content:            content,
		ReasoningContent:   reasoningContent,
		ReasoningSignature: reasoningSignature,
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

// addStreamUsage accumulates streaming usage into a running total,
// folding the per-chunk cache hits into the aggregate's top-level
// CachedPromptTokens (the aggregate's PromptTokensDetails stays nil so
// downstream accumulators cannot double-count via Usage.CacheTokens).
func addStreamUsage(total *types.Usage, u *types.Usage) {
	total.PromptTokens += u.PromptTokens
	total.CompletionTokens += u.CompletionTokens
	total.TotalTokens += u.TotalTokens
	if u.PromptIncludesCache {
		total.PromptIncludesCache = true
	}
	if cached := u.CacheTokens(); cached > 0 {
		total.CachedPromptTokens += cached
	}
}

// setStreamUsageAttrs enriches a stream span with token detail attributes.
// The llm.* attributes describe the CURRENT chunk exactly as the provider
// reported it. The GenAI semconv attributes SigNoz's signozllmpricing
// processor keys off mirror the ACCUMULATED total instead: native
// Anthropic streams report input tokens in the message_start chunk and
// output tokens in a later message_delta chunk, so mirroring per-chunk
// usage would let the output-only chunk overwrite
// gen_ai.usage.input_tokens with 0. Callers must pass the totalUsage
// snapshot taken AFTER addStreamUsage folded the chunk in.
func setStreamUsageAttrs(span trace.Span, chunkUsage, totalUsage *types.Usage) {
	span.SetAttributes(
		attribute.Int("llm.prompt_tokens", chunkUsage.PromptTokens),
		attribute.Int("llm.completion_tokens", chunkUsage.CompletionTokens),
		attribute.Int("llm.total_tokens", chunkUsage.TotalTokens),
	)
	if d := chunkUsage.CompletionTokensDetails; d != nil && d.ReasoningTokens > 0 {
		span.SetAttributes(attribute.Int("llm.reasoning_tokens", d.ReasoningTokens))
	}
	if d := chunkUsage.PromptTokensDetails; d != nil && d.CachedTokens > 0 {
		span.SetAttributes(attribute.Int("llm.cached_prompt_tokens", d.CachedTokens))
	}
	observability.MirrorGenAIUsage(span, *totalUsage)
}
