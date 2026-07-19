package observability

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// StartTurn creates a span for one agent loop iteration (one user prompt
// + all tool calls + assistant response). The returned context should
// flow into getAssistantMessage and executeAndCollect so tool and LLM
// spans appear as children of the turn span in Jaeger.
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
