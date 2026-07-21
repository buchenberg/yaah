package agent

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/buchenberg/yaah/internal/types"
)

// captureUsage adds response token usage to the running total,
// including detailed breakdowns (reasoning, cached) when provided.
func (l *Loop) captureUsage(resp *types.ChatResponse) {
	l.TotalTokens.PromptTokens += resp.Usage.PromptTokens
	l.TotalTokens.CompletionTokens += resp.Usage.CompletionTokens
	l.TotalTokens.TotalTokens += resp.Usage.TotalTokens
	l.LastPromptTokens = resp.Usage.PromptTokens

	if d := resp.Usage.CompletionTokensDetails; d != nil {
		l.TotalReasoningTokens += d.ReasoningTokens
	}
	if d := resp.Usage.PromptTokensDetails; d != nil {
		l.TotalCachedPromptTokens += d.CachedTokens
	}
}

// captureStreamUsage accumulates token details from a streaming usage report
// (sent by providers in the final SSE chunk when stream_options.include_usage is set).
func (l *Loop) captureStreamUsage(u *types.Usage) {
	l.TotalTokens.PromptTokens += u.PromptTokens
	l.TotalTokens.CompletionTokens += u.CompletionTokens
	l.TotalTokens.TotalTokens += u.TotalTokens
	l.LastPromptTokens = u.PromptTokens
	if d := u.CompletionTokensDetails; d != nil {
		l.TotalReasoningTokens += d.ReasoningTokens
	}
	if d := u.PromptTokensDetails; d != nil {
		l.TotalCachedPromptTokens += d.CachedTokens
	}
}

// setStreamUsageAttrs enriches a stream span with token detail attributes.
func (l *Loop) setStreamUsageAttrs(span trace.Span, u *types.Usage) {
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
