package observability

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"go.opentelemetry.io/otel"
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

// systemPromptLen caps the recorded system prompt. Larger than detailLen
// because assembled prompts (identity + directives + project instructions
// + skills) routinely exceed 8KB, and the diagnostic value is in seeing
// the full prompt the model actually received. Matches the tui.body cap.
const systemPromptLen = 32768

// tracer returns the current tracer from the global provider.
// It must be called lazily (not stored in a package-level var) because
// the global TracerProvider may change after package init via Setup().
func tracer() trace.Tracer {
	return otel.Tracer("yaah")
}

// StartPrompt creates the root span for a single user-visible question-
// to-answer interaction. All agent.turn spans for this prompt are
// children of this span. The prompt text is truncated to 200 chars as
// an attribute so traces are self-documenting. sessionID and turnID
// cross-link the span tree to `sessions`/`messages` rows and Shepherd
// turn facts carrying the same turn_id.
func StartPrompt(ctx context.Context, sessionID, turnID, prompt string) (context.Context, trace.Span) {
	ctx, span := tracer().Start(ctx, "prompt")
	attrs := []attribute.KeyValue{}
	if sessionID != "" {
		attrs = append(attrs, attribute.String("session.id", sessionID))
	}
	if turnID != "" {
		attrs = append(attrs, attribute.String("turn.id", turnID))
	}
	if prompt != "" {
		attrs = append(attrs, attribute.String("prompt.text", truncate(safeString(prompt), 200)))
	}
	span.SetAttributes(attrs...)
	return ctx, span
}

// StartTurn creates a span for one agent loop iteration. The returned
// context should flow into the LLM call and tool execution for that
// turn so nested spans are children. turnID is the stable per-prompt ID
// shared with the messages rows and Shepherd facts for this interaction.
func StartTurn(ctx context.Context, turnNum int, turnID, prompt string) (context.Context, trace.Span) {
	ctx, span := tracer().Start(ctx, "agent.turn")
	attrs := []attribute.KeyValue{
		attribute.Int("turn.number", turnNum),
	}
	if turnID != "" {
		attrs = append(attrs, attribute.String("turn.id", turnID))
	}
	if prompt != "" {
		attrs = append(attrs, attribute.String("turn.prompt", truncate(safeString(prompt), 200)))
	}
	span.SetAttributes(attrs...)
	return ctx, span
}

// TraceIDFromContext returns the hex W3C trace ID of the current span in
// ctx, or "" when tracing is disabled or no valid span context exists.
func TraceIDFromContext(ctx context.Context) string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return ""
	}
	return sc.TraceID().String()
}

// StartTool creates a span for a single tool execution. The operation
// name is the tool name; the full args are stored as an attribute so
// they are visible in the Jaeger detail panel without cluttering the
// waterfall bars.
func StartTool(ctx context.Context, name, argsJSON string) (context.Context, trace.Span) {
	ctx, span := tracer().Start(ctx, name)
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

// StartPrune creates a span for a soft-prune pass — the Tier-0 context
// reclaim that elides stale tool-result content without an LLM call. The
// reason ("post_tool" | "post_compaction" | "payload_limit") is recorded as
// an attribute so Jaeger shows why the pass ran. FinishPrune ends the span.
func StartPrune(ctx context.Context, reason string) (context.Context, trace.Span) {
	ctx, span := tracer().Start(ctx, "prune")
	span.SetAttributes(
		attribute.String("prune.reason", safeString(reason)),
	)
	return ctx, span
}

// FinishPrune records the prune outcome as span attributes and ends the span.
// It takes primitive fields rather than a pipeline.PruneStats so the
// observability package stays free of an import on internal/agent/pipeline.
func FinishPrune(span trace.Span, reason string, candidates, marked, reclaimed, protectedSkipped, totalMarked int, committed bool) {
	if span == nil {
		return
	}
	span.SetAttributes(
		attribute.String("prune.reason", safeString(reason)),
		attribute.Int("prune.candidates", candidates),
		attribute.Int("prune.marked", marked),
		attribute.Int("prune.reclaimed_tokens", reclaimed),
		attribute.Int("prune.protected_skipped", protectedSkipped),
		attribute.Bool("prune.committed", committed),
		attribute.Int("prune.total_marked", totalMarked),
	)
	span.End()
}

// StartLLM creates a span for a single Chat Completions call. The
// model name is an attribute; a completion event carries the prompt
// size, response size, and duration.
func StartLLM(ctx context.Context, model string) (context.Context, trace.Span) {
	ctx, span := tracer().Start(ctx, "llm.chat")
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
	ctx, span := tracer().Start(ctx, "llm.stream")
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
	ctx, span := tracer().Start(ctx, name)
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
		span.SetStatus(codes.Ok, "")
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
// tool-call payloads, and conversation context as span attributes/events.
// They are gated on the Loop's OtelVerbose flag at the call sites — when
// verbose tracing is off, the loop never calls them, so there is zero
// overhead and Jaeger only sees the lightweight span tree
// (turn/stream/tool) with token counts.
// ────────────────────────────────────────────────────────────────────

// RecordAssistantResponse records the full model response on a span:
// content, reasoning, refusal, and every tool call's name + arguments.
// Pass the finish_reason if known (empty string to omit).
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

// SystemContent joins the content of all system messages in a request,
// in order. Returns "" when there are none.
func SystemContent(msgs []types.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		if m.Role != "system" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(m.Content)
	}
	return b.String()
}

// RecordSystemPrompt records the exact system prompt the model sees on
// this call as a span event, so Jaeger shows what the agent (or sub-agent)
// actually received — identity, directives, role guidance, and all.
// Verbose-only; capped at systemPromptLen.
func RecordSystemPrompt(span trace.Span, system string) {
	if span == nil || system == "" {
		return
	}
	span.AddEvent("system_prompt", trace.WithAttributes(
		attribute.String("llm.system", truncate(safeString(system), systemPromptLen)),
		attribute.Int("llm.system_len", len(system)),
	))
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

// RecordTUIView records the rendered TUI view as a span event. It creates
// a short-lived span so the view is visible in Jaeger traces for debugging
// rendering issues. The body is truncated to 32k to avoid overwhelming the
// collector. If preScan and postScan differ, the delta helps pinpoint zone
// marker corruption.
func RecordTUIView(preScan, postScan string) {
	ctx := context.Background()
	tracer := otel.Tracer("yaah.tui")
	_, span := tracer.Start(ctx, "tui.render")
	defer span.End()

	if len(preScan) > 32768 {
		preScan = preScan[:32768] + "...[truncated]"
	}
	span.SetAttributes(attribute.String("tui.body", safeString(preScan)))

	if postScan != "" && postScan != preScan {
		if len(postScan) > 32768 {
			postScan = postScan[:32768] + "...[truncated]"
		}
		span.SetAttributes(attribute.String("tui.body_postscan", safeString(postScan)))
	}
}

// RecordTurnResponse records the LLM response on a short-lived child span
// that is ended immediately. This span survives parent crashes — even if
// the parent turn span is never ended, the OTel SDK exports independently
// ended child spans, making them visible in traces for crash diagnostics.
func RecordTurnResponse(ctx context.Context, contentLen, toolCallCount int, toolNames []string, finishReason string) {
	_, span := tracer().Start(ctx, "turn.response")
	defer span.End()
	span.SetAttributes(
		attribute.Int("response.content_len", contentLen),
		attribute.Int("response.tool_calls", toolCallCount),
		attribute.String("response.finish_reason", finishReason),
	)
	if len(toolNames) > 0 {
		span.SetAttributes(attribute.StringSlice("response.tool_names", toolNames))
	}
}

// RecordToolDispatch records tool execution start on a short-lived child
// span. The span ends immediately so it is exported regardless of whether
// the parent turn span or individual tool spans are later lost.
func RecordToolDispatch(ctx context.Context, toolCount int, toolNames []string) {
	_, span := tracer().Start(ctx, "turn.dispatch_tools")
	defer span.End()
	span.SetAttributes(
		attribute.Int("dispatch.count", toolCount),
	)
	if len(toolNames) > 0 {
		span.SetAttributes(attribute.StringSlice("dispatch.tool_names", toolNames))
	}
}

// RecordToolDispatchDone records tool execution completion on a short-lived
// child span. It captures the number of results and any error encountered
// during the dispatch phase (not individual tool errors).
func RecordToolDispatchDone(ctx context.Context, resultCount int, dispatchErr error) {
	_, span := tracer().Start(ctx, "turn.dispatch_done")
	defer span.End()
	span.SetAttributes(
		attribute.Int("dispatch.results", resultCount),
	)
	if dispatchErr != nil {
		RecordError(span, dispatchErr)
	}
}

// RecordToolGoroutine records a checkpoint inside the tool execution
// goroutine. The span is ended immediately (no defer) so it is exported
// even if the goroutine later blocks.  Without this, a blocked goroutine
// would leave the span dangling and invisible in traces.
// phase is one of: "spawned", "acquire_concurrency", "publish_start", "published".
func RecordToolGoroutine(ctx context.Context, toolName, phase string) {
	_, span := tracer().Start(ctx, "tool.goroutine")
	span.SetAttributes(
		attribute.String("tool.name", toolName),
		attribute.String("tool.phase", phase),
	)
	span.End()
}

// safeString replaces invalid UTF-8 byte sequences with the Unicode
// replacement character so the result is safe for protobuf string fields
// (which require valid UTF-8).
func safeString(s string) string {
	return strings.ToValidUTF8(s, "\uFFFD")
}
