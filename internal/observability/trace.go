package observability

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/buchenberg/yaah/internal/types"
)

// detailLen is the max length for full content/reasoning/summary/task
// attributes recorded for debugging. Large enough to capture a full model
// response (maxInnerSummaryLen and ToolResultMaxLen are both ~8000) while
// keeping Jaeger attribute payloads reasonable.
const detailLen = 8000

// StartPrompt creates the root span for a single user-visible question-
// to-answer interaction. All agent.turn spans for this prompt are
// children of this span. The prompt text is truncated to 200 chars as
// an attribute so traces are self-documenting.
func StartPrompt(ctx context.Context, prompt string) (context.Context, trace.Span) {
	ctx, span := tracer.Start(ctx, "prompt")
	if prompt != "" {
		span.SetAttributes(attribute.String("prompt.text", truncate(safeString(prompt), 200)))
	}
	return ctx, span
}

// StartTurn creates a span for one agent loop iteration. The returned
// context should flow into the LLM call and tool execution for that
// turn so nested spans are children.
func StartTurn(ctx context.Context, turnNum int, prompt string) (context.Context, trace.Span) {
	ctx, span := tracer.Start(ctx, "agent.turn")
	span.SetAttributes(
		attribute.Int("turn.number", turnNum),
	)
	if prompt != "" {
		span.SetAttributes(attribute.String("turn.prompt", truncate(safeString(prompt), 200)))
	}
	return ctx, span
}

// StartTool creates a span for a single tool execution. The operation
// name is the tool name; the full args are stored as an attribute so
// they are visible in the Jaeger detail panel without cluttering the
// waterfall bars.
func StartTool(ctx context.Context, name, argsJSON string) (context.Context, trace.Span) {
	ctx, span := tracer.Start(ctx, name)
	span.SetAttributes(
		attribute.String("tool.name", name),
		attribute.String("tool.args", truncate(safeString(argsJSON), 200)),
	)
	return ctx, span
}

// FinishTool adds a completion event with the abbreviated result and
// error (if any). It does NOT end the span — the caller owns End().
func FinishTool(span trace.Span, result string, err error) {
	if err != nil {
		RecordError(span, err)
	}
	if result != "" {
		span.AddEvent("result", trace.WithAttributes(
			attribute.String("tool.result", truncate(safeString(result), 200)),
		))
	}
}

// StartLLM creates a span for a single Chat Completions call. The
// model name is an attribute; a completion event carries the prompt
// size, response size, and duration.
func StartLLM(ctx context.Context, model string) (context.Context, trace.Span) {
	ctx, span := tracer.Start(ctx, "llm.chat")
	span.SetAttributes(
		attribute.String("llm.model", model),
	)
	return ctx, span
}

// FinishLLM records prompt/completion token counts and duration as an event.
func FinishLLM(span trace.Span, promptLen, systemLen int, usage types.Usage) {
	attrs := []attribute.KeyValue{
		attribute.Int("llm.messages", promptLen),
		attribute.Int("llm.system_len", systemLen),
		attribute.Int("llm.prompt_tokens", usage.PromptTokens),
		attribute.Int("llm.completion_tokens", usage.CompletionTokens),
		attribute.Int("llm.total_tokens", usage.TotalTokens),
	}
	if d := usage.CompletionTokensDetails; d != nil && d.ReasoningTokens > 0 {
		attrs = append(attrs, attribute.Int("llm.reasoning_tokens", d.ReasoningTokens))
	}
	if d := usage.PromptTokensDetails; d != nil && d.CachedTokens > 0 {
		attrs = append(attrs, attribute.Int("llm.cached_prompt_tokens", d.CachedTokens))
	}
	span.AddEvent("tokens", trace.WithAttributes(attrs...))
}

// StartStream creates a span for a streaming LLM call. The returned
// context should flow into SendStream so the provider wrapper inherits
// the span hierarchy.
func StartStream(ctx context.Context, model string) (context.Context, trace.Span) {
	ctx, span := tracer.Start(ctx, "llm.stream")
	span.SetAttributes(attribute.String("llm.model", model))
	return ctx, span
}

// FinishStream adds first-token latency and token count events to a
// streaming span. ttft is the time until the first content or tool-call
// delta arrived; totalTokens is the count of content deltas emitted.
func FinishStream(span trace.Span, ttftMs int64, totalTokens int, toolCalls int) {
	span.AddEvent("stream", trace.WithAttributes(
		attribute.Int64("llm.ttft_ms", ttftMs),
		attribute.Int("llm.stream_tokens", totalTokens),
		attribute.Int("llm.tool_calls", toolCalls),
	))
}

// StartSubAgent creates a span for a sub-agent dispatched by the task
// tool. The operation name includes role and description so the
// waterfall bar itself is readable.
func StartSubAgent(ctx context.Context, role, description string) (context.Context, trace.Span) {
	name := "subagent: " + role
	if description != "" {
		name += " — " + truncate(description, 60)
	}
	ctx, span := tracer.Start(ctx, name)
	span.SetAttributes(
		attribute.String("subagent.role", role),
		attribute.String("subagent.description", safeString(description)),
	)
	span.AddEvent("dispatched", trace.WithAttributes(
		attribute.String("subagent.role", role),
		attribute.String("subagent.task", safeString(description)),
	))
	return ctx, span
}

// FinishSubAgent adds a completion event with status and duration.
func FinishSubAgent(span trace.Span, err error) {
	if err != nil {
		span.AddEvent("failed", trace.WithAttributes(
			attribute.String("subagent.error", safeString(err.Error())),
		))
		RecordError(span, err)
	} else {
		span.AddEvent("completed")
	}
}

// StartInnerLoop creates a span for the dual-loop inner executor. The
// task prompt is included as an attribute so traces show what the inner
// loop was asked to do.
func StartInnerLoop(ctx context.Context, taskPrompt string) (context.Context, trace.Span) {
	ctx, span := tracer.Start(ctx, "inner.loop")
	span.SetAttributes(
		attribute.String("inner.task", truncate(safeString(taskPrompt), 200)),
	)
	return ctx, span
}

// FinishInnerLoop records the inner loop outcome. iterations is the number
// of rounds the inner loop ran (including the final text-only round).
// exhausted is true if it hit MaxInnerIterations.
func FinishInnerLoop(span trace.Span, iterations int, exhausted bool, err error) {
	attrs := []attribute.KeyValue{
		attribute.Int("inner.iterations", iterations),
		attribute.Bool("inner.exhausted", exhausted),
	}
	if err != nil {
		attrs = append(attrs, attribute.String("inner.error", safeString(err.Error())))
		span.AddEvent("error", trace.WithAttributes(attrs...))
		RecordError(span, err)
	} else if exhausted {
		span.AddEvent("exhausted", trace.WithAttributes(attrs...))
	} else {
		span.AddEvent("completed", trace.WithAttributes(attrs...))
	}
}

// RecordError marks the span as errored and records the error string.
func RecordError(span trace.Span, err error) {
	if err != nil {
		span.SetStatus(codes.Error, safeString(err.Error()))
		span.RecordError(err)
	}
}

// ────────────────────────────────────────────────────────────────────
// Verbose tracing helpers. These record full model content, reasoning,
// tool-call payloads, conversation context, and dual-loop handoffs as
// span attributes/events. They are gated on the Loop's OtelVerbose flag
// at the call sites — when verbose tracing is off, the loop never calls
// them, so there is zero overhead and Jaeger only sees the lightweight
// span tree (turn/stream/tool/inner.loop) with token counts.
// ────────────────────────────────────────────────────────────────────

// RecordAssistantResponse records the full model response on a span:
// content, reasoning, refusal, and every tool call's name + arguments.
// Used for both outer agent-turn responses and inner-loop iterations so
// the dual-loop conversation is visible end-to-end in Jaeger. Pass the
// finish_reason if known (empty string to omit).
func RecordAssistantResponse(span trace.Span, msg types.Message, finishReason string) {
	if span == nil {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String("llm.content", truncate(safeString(msg.Content), detailLen)),
		attribute.Int("llm.content_len", len(msg.Content)),
		attribute.Int("llm.tool_calls", len(msg.ToolCalls)),
	}
	if finishReason != "" {
		attrs = append(attrs, attribute.String("llm.finish_reason", finishReason))
	}
	if msg.ReasoningContent != "" {
		attrs = append(attrs,
			attribute.String("llm.reasoning", truncate(safeString(msg.ReasoningContent), detailLen)),
			attribute.Int("llm.reasoning_len", len(msg.ReasoningContent)),
		)
	}
	if msg.Refusal != "" {
		attrs = append(attrs, attribute.String("llm.refusal", truncate(safeString(msg.Refusal), detailLen)))
	}
	for i, tc := range msg.ToolCalls {
		attrs = append(attrs,
			attribute.String(fmt.Sprintf("llm.tool_call.%d.name", i), tc.Function.Name),
			attribute.String(fmt.Sprintf("llm.tool_call.%d.args", i), truncate(safeString(tc.Function.Arguments), detailLen)),
		)
	}
	span.AddEvent("assistant.response", trace.WithAttributes(attrs...))
}

// RecordInnerTask records the full task prompt handed to the inner
// executor loop. This is the payload that reveals the dual-loop
// confusion: when the outer model emits prose alongside its tool calls,
// that prose is fed to the inner model as "## Instructions" and the inner
// model re-interprets it as a new question.
func RecordInnerTask(span trace.Span, taskPrompt string) {
	if span == nil {
		return
	}
	span.AddEvent("inner.task", trace.WithAttributes(
		attribute.String("inner.task_full", truncate(safeString(taskPrompt), detailLen)),
		attribute.Int("inner.task_len", len(taskPrompt)),
	))
}

// RecordInnerSummary records the summary the inner loop produced and that
// is injected back into the outer conversation as an assistant message.
// Surfacing this in traces is what makes the "outer model responds to its
// own inner summary" failure mode diagnosable.
func RecordInnerSummary(span trace.Span, summary string, outerMsgCount int) {
	if span == nil {
		return
	}
	span.AddEvent("inner.summary_injected", trace.WithAttributes(
		attribute.String("inner.summary_full", truncate(safeString(summary), detailLen)),
		attribute.Int("inner.summary_len", len(summary)),
		attribute.Int("outer.messages", outerMsgCount),
	))
}

// RecordConversation records a compact overview of the message history on
// a span so the full context the model is about to see is visible in
// Jaeger — one event per message carrying role, content length, a short
// preview, and tool-call names. Use at the start of a turn to capture the
// conversation the model is responding to.
func RecordConversation(span trace.Span, messages []types.Message) {
	if span == nil || len(messages) == 0 {
		return
	}
	for i, m := range messages {
		attrs := []attribute.KeyValue{
			attribute.String(fmt.Sprintf("msg.%d.role", i), m.Role),
			attribute.Int(fmt.Sprintf("msg.%d.content_len", i), len(m.Content)),
		}
		if m.Content != "" {
			attrs = append(attrs, attribute.String(fmt.Sprintf("msg.%d.preview", i), truncate(safeString(m.Content), 200)))
		}
		if len(m.ToolCalls) > 0 {
			names := make([]string, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				names = append(names, tc.Function.Name)
			}
			attrs = append(attrs, attribute.StringSlice(fmt.Sprintf("msg.%d.tools", i), names))
		}
		span.AddEvent("msg", trace.WithAttributes(attrs...))
	}
}

// RecordStreamEnd records how a streaming LLM call terminated and whether
// usage metadata was captured. Surfaces the degenerate streams that end
// without a usage chunk (the spans that show no token attributes) so the
// close path is visible: "channel_closed", "finish_reason", "errs_nil",
// or "ctx_done".
func RecordStreamEnd(span trace.Span, path string, finishReason string, usageCaptured bool, contentLen, toolCalls int) {
	if span == nil {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String("stream.end_path", path),
		attribute.Bool("llm.usage_captured", usageCaptured),
		attribute.Int("llm.content_len", contentLen),
		attribute.Int("llm.tool_calls", toolCalls),
	}
	if finishReason != "" {
		attrs = append(attrs, attribute.String("llm.finish_reason", finishReason))
	}
	span.AddEvent("stream_end", trace.WithAttributes(attrs...))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n - 3
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

// safeString replaces invalid UTF-8 byte sequences with the Unicode
// replacement character so the result is safe for protobuf string fields
// (which require valid UTF-8).
func safeString(s string) string {
	return strings.ToValidUTF8(s, "\uFFFD")
}
