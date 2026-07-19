package observability

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// StartPrompt creates the root span for a single user-visible question-
// to-answer interaction. All agent.turn spans for this prompt are
// children of this span. The prompt text is truncated to 200 chars as
// an attribute so traces are self-documenting.
func StartPrompt(ctx context.Context, prompt string) (context.Context, trace.Span) {
	ctx, span := tracer.Start(ctx, "prompt")
	if prompt != "" {
		span.SetAttributes(attribute.String("prompt.text", truncate(prompt, 200)))
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
		span.SetAttributes(attribute.String("turn.prompt", truncate(prompt, 200)))
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
		attribute.String("tool.args", truncate(argsJSON, 200)),
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
			attribute.String("tool.result", truncate(result, 200)),
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
func FinishLLM(span trace.Span, promptLen, systemLen int, promptTokens, completionTokens int) {
	span.AddEvent("tokens", trace.WithAttributes(
		attribute.Int("llm.messages", promptLen),
		attribute.Int("llm.system_len", systemLen),
		attribute.Int("llm.prompt_tokens", promptTokens),
		attribute.Int("llm.completion_tokens", completionTokens),
		attribute.Int("llm.total_tokens", promptTokens+completionTokens),
	))
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
		attribute.String("subagent.description", description),
	)
	span.AddEvent("dispatched", trace.WithAttributes(
		attribute.String("subagent.role", role),
		attribute.String("subagent.task", description),
	))
	return ctx, span
}

// FinishSubAgent adds a completion event with status and duration.
func FinishSubAgent(span trace.Span, err error) {
	if err != nil {
		span.AddEvent("failed", trace.WithAttributes(
			attribute.String("subagent.error", err.Error()),
		))
		RecordError(span, err)
	} else {
		span.AddEvent("completed")
	}
}

// RecordError marks the span as errored and records the error string.
func RecordError(span trace.Span, err error) {
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

// StartConflictCheck creates a span for the conflict detection phase
// that runs after tool execution to check for parallel-worker file conflicts.
func StartConflictCheck(ctx context.Context) (context.Context, trace.Span) {
	return tracer.Start(ctx, "conflict.check")
}

// FinishConflictCheck records the conflict count and files as span events.
func FinishConflictCheck(span trace.Span, fileCount int) {
	span.SetAttributes(attribute.Int("conflict.files", fileCount))
	if fileCount > 0 {
		span.AddEvent("conflict.detected", trace.WithAttributes(
			attribute.Int("conflict.files", fileCount),
		))
	}
}
