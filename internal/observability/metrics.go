package observability

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var meter = otel.Meter("yaah")

var (
	toolCalls    metric.Int64Counter
	toolErrors   metric.Int64Counter
	toolDuration metric.Int64Histogram

	llmCalls            metric.Int64Counter
	llmDuration         metric.Int64Histogram
	llmPromptTokens     metric.Int64Histogram
	llmCompletionTokens metric.Int64Histogram
	llmStreamTTFT       metric.Int64Histogram

	compactionCount         metric.Int64Counter
	compactionDuration      metric.Int64Histogram
	compactionSavingsTokens metric.Int64Histogram
	compactionSavingsPct    metric.Int64Histogram
	compactionBeforeTokens  metric.Int64Histogram
	compactionAfterTokens   metric.Int64Histogram

	agentTurns        metric.Int64Counter
	agentTurnDuration metric.Int64Histogram

	tuiQueueEvents  metric.Int64Counter
	tuiQueueDepth   metric.Int64Histogram
	tuiRefreshCount metric.Int64Counter
	tuiRefreshDur   metric.Int64Histogram
	tuiRefreshCad   metric.Int64Histogram
)

func initMetrics() {
	toolCalls, _ = meter.Int64Counter(
		"yaah.tool.calls",
		metric.WithDescription("Number of tool calls dispatched"),
	)
	toolErrors, _ = meter.Int64Counter(
		"yaah.tool.errors",
		metric.WithDescription("Number of tool calls that failed"),
	)
	toolDuration, _ = meter.Int64Histogram(
		"yaah.tool.duration_ms",
		metric.WithDescription("Tool execution duration in milliseconds"),
		metric.WithUnit("ms"),
	)

	llmCalls, _ = meter.Int64Counter(
		"yaah.llm.calls",
		metric.WithDescription("Number of LLM chat completions calls"),
	)
	llmDuration, _ = meter.Int64Histogram(
		"yaah.llm.duration_ms",
		metric.WithDescription("LLM call duration in milliseconds"),
		metric.WithUnit("ms"),
	)
	llmPromptTokens, _ = meter.Int64Histogram(
		"yaah.llm.prompt_tokens",
		metric.WithDescription("Prompt tokens per LLM call"),
	)
	llmCompletionTokens, _ = meter.Int64Histogram(
		"yaah.llm.completion_tokens",
		metric.WithDescription("Completion tokens per LLM call"),
	)
	llmStreamTTFT, _ = meter.Int64Histogram(
		"yaah.llm.stream_ttft_ms",
		metric.WithDescription("Time to first token in streaming LLM calls"),
		metric.WithUnit("ms"),
	)

	compactionCount, _ = meter.Int64Counter(
		"yaah.compaction.count",
		metric.WithDescription("Number of context compactions"),
	)
	compactionDuration, _ = meter.Int64Histogram(
		"yaah.compaction.duration_ms",
		metric.WithDescription("Compaction duration in milliseconds"),
		metric.WithUnit("ms"),
	)
	compactionSavingsTokens, _ = meter.Int64Histogram(
		"yaah.compaction.savings_tokens",
		metric.WithDescription("Tokens saved per compaction"),
	)
	compactionSavingsPct, _ = meter.Int64Histogram(
		"yaah.compaction.savings_pct",
		metric.WithDescription("Compaction savings as integer percentage (0-100)"),
	)
	compactionBeforeTokens, _ = meter.Int64Histogram(
		"yaah.compaction.before_tokens",
		metric.WithDescription("Estimated tokens before compaction"),
	)
	compactionAfterTokens, _ = meter.Int64Histogram(
		"yaah.compaction.after_tokens",
		metric.WithDescription("Estimated tokens after compaction"),
	)

	agentTurns, _ = meter.Int64Counter(
		"yaah.agent.turns",
		metric.WithDescription("Number of agent loop turns"),
	)
	agentTurnDuration, _ = meter.Int64Histogram(
		"yaah.agent.turn_duration_ms",
		metric.WithDescription("Agent turn duration in milliseconds"),
		metric.WithUnit("ms"),
	)

	tuiQueueEvents, _ = meter.Int64Counter(
		"yaah.tui2.ui_queue.events",
		metric.WithDescription("TUI2 UI queue events by outcome and type"),
	)
	tuiQueueDepth, _ = meter.Int64Histogram(
		"yaah.tui2.ui_queue.depth",
		metric.WithDescription("Sampled TUI2 UI queue depth"),
	)
	tuiRefreshCount, _ = meter.Int64Counter(
		"yaah.tui2.refresh.count",
		metric.WithDescription("Number of TUI2 refresh renders"),
	)
	tuiRefreshDur, _ = meter.Int64Histogram(
		"yaah.tui2.refresh.duration_ms",
		metric.WithDescription("TUI2 refresh duration in milliseconds"),
		metric.WithUnit("ms"),
	)
	tuiRefreshCad, _ = meter.Int64Histogram(
		"yaah.tui2.refresh.cadence_ms",
		metric.WithDescription("Milliseconds since previous TUI2 refresh"),
		metric.WithUnit("ms"),
	)
}

// RecordToolCall records a tool execution outcome.
func RecordToolCall(ctx context.Context, toolName string, duration time.Duration, hasError bool) {
	if toolCalls == nil {
		return
	}
	attr := attribute.String("tool_name", toolName)
	ms := duration.Milliseconds()
	toolCalls.Add(ctx, 1, metric.WithAttributes(attr))
	toolDuration.Record(ctx, ms, metric.WithAttributes(attr))
	if hasError {
		toolErrors.Add(ctx, 1, metric.WithAttributes(attr))
	}
}

// RecordLLMCall records an LLM chat completions call.
func RecordLLMCall(ctx context.Context, duration time.Duration, promptTokens, completionTokens int) {
	if llmCalls == nil {
		return
	}
	llmCalls.Add(ctx, 1)
	llmDuration.Record(ctx, duration.Milliseconds())
	llmPromptTokens.Record(ctx, int64(promptTokens))
	llmCompletionTokens.Record(ctx, int64(completionTokens))
}

// RecordLLMStreamTTFT records the time-to-first-token for a streaming call.
func RecordLLMStreamTTFT(ctx context.Context, ttft time.Duration) {
	if llmStreamTTFT == nil {
		return
	}
	llmStreamTTFT.Record(ctx, ttft.Milliseconds())
}

// RecordCompaction records a context compaction outcome.
func RecordCompaction(ctx context.Context, reason string, duration time.Duration, beforeTokens, afterTokens int, savingsPct float64) {
	if compactionCount == nil {
		return
	}
	attr := attribute.String("compaction_reason", reason)
	compactionCount.Add(ctx, 1, metric.WithAttributes(attr))
	compactionDuration.Record(ctx, duration.Milliseconds(), metric.WithAttributes(attr))
	compactionSavingsTokens.Record(ctx, int64(beforeTokens-afterTokens), metric.WithAttributes(attr))
	compactionSavingsPct.Record(ctx, int64(savingsPct*100), metric.WithAttributes(attr))
	compactionBeforeTokens.Record(ctx, int64(beforeTokens), metric.WithAttributes(attr))
	compactionAfterTokens.Record(ctx, int64(afterTokens), metric.WithAttributes(attr))
}

// RecordAgentTurn records an agent loop turn.
func RecordAgentTurn(ctx context.Context, duration time.Duration) {
	if agentTurns == nil {
		return
	}
	agentTurns.Add(ctx, 1)
	agentTurnDuration.Record(ctx, duration.Milliseconds())
}

// RecordTUIQueueEvent records a queue event outcome and queue depth sample.
func RecordTUIQueueEvent(ctx context.Context, eventType, outcome string, depth int) {
	if tuiQueueEvents == nil {
		return
	}
	attrs := metric.WithAttributes(
		attribute.String("event_type", eventType),
		attribute.String("outcome", outcome),
	)
	tuiQueueEvents.Add(ctx, 1, attrs)
	if depth >= 0 {
		tuiQueueDepth.Record(ctx, int64(depth), attrs)
	}
}

// RecordTUIRefresh records refresh count, duration, cadence, and queue depth.
func RecordTUIRefresh(ctx context.Context, duration time.Duration, cadence time.Duration, queueDepth int) {
	if tuiRefreshCount == nil {
		return
	}
	tuiRefreshCount.Add(ctx, 1)
	tuiRefreshDur.Record(ctx, duration.Milliseconds())
	if cadence > 0 {
		tuiRefreshCad.Record(ctx, cadence.Milliseconds())
	}
	if queueDepth >= 0 {
		tuiQueueDepth.Record(ctx, int64(queueDepth), metric.WithAttributes(attribute.String("event_type", "refresh"), attribute.String("outcome", "sample")))
	}
}
