package agent

import (
	"context"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/observability"
	"github.com/buchenberg/yaah/internal/prompts"
	"github.com/buchenberg/yaah/internal/types"
)

// buildTurnRequest runs PrepareStep middleware, builds the ChatRequest
// with MaxTurns/WrapUp logic and JSONMode, and records verbosely if asked.
func (l *Loop) buildTurnRequest(ctx context.Context, iter int, messages []types.Message, pipe *pipeline.Pipeline, turnSpan trace.Span) (*pipeline.Step, types.ChatRequest, error) {
	step := &pipeline.Step{
		Messages:      messages,
		Tools:         l.buildToolDefs(),
		Iteration:     iter,
		MaxToolTurns:  l.Config.MaxToolTurns,
		MaxLoopCycles: l.Config.MaxLoopCycles,
		Model:         l.Config.Model,
		SystemPrompt:  l.Config.SystemPrompt,
	}

	var err error
	step, err = pipe.RunPrepareStep(ctx, step)
	if err != nil {
		return nil, types.ChatRequest{}, err
	}
	messages = step.Messages

	req := types.ChatRequest{
		Model:    l.Config.Model,
		Messages: l.prepareRequestMessages(messages),
		Tools:    l.buildToolsForLevel(),
	}

	if l.Config.MaxToolTurns > 0 {
		effective := l.Config.MaxToolTurns
		if effective >= l.Config.MaxLoopCycles {
			effective = l.Config.MaxLoopCycles - 1
		}
		if iter >= effective {
			req.Tools = nil
			if l.Config.OtelEnabled && turnSpan != nil {
				turnSpan.AddEvent("maxturns.stripped", trace.WithAttributes(
					attribute.Int("maxturns.limit", l.Config.MaxToolTurns),
					attribute.Int("maxturns.iteration", iter),
				))
			}
		} else if l.Config.WrapUpThreshold > 0 && iter >= effective-l.Config.WrapUpThreshold {
			l.injectWrapUpNotice(&req, turnSpan, effective-iter)
		}
	} else if l.Config.WrapUpThreshold > 0 && iter >= l.Config.MaxLoopCycles-l.Config.WrapUpThreshold {
		l.injectWrapUpNotice(&req, turnSpan, l.Config.MaxLoopCycles-iter)
	}

	if l.Config.JSONMode {
		req.ResponseFormat = &types.ResponseFormat{Type: "json_object"}
	}

	return step, req, nil
}

// guardContextBeforeCall applies pre-flight context compaction when the
// estimated token count exceeds the context window or the serialized
// request exceeds the payload byte limit. Returns an error when the
// request ends up empty after compaction (unrecoverable).
func (l *Loop) guardContextBeforeCall(turnCtx context.Context, messages *[]types.Message, req *types.ChatRequest, turnSpan trace.Span) error {
	if l.Config.OtelVerbose && turnSpan != nil {
		observability.RecordConversation(turnSpan, *messages)
	}

	// Compaction is handled by the middleware pipeline (CompactionMiddleware.
	// PrepareStep). guardContextBeforeCall only validates that the request
	// is not empty — it does not trigger compaction.
	if len(req.Messages) == 0 {
		err := fmt.Errorf("refusing to send empty message list to provider — %d messages after prepare", len(req.Messages))
		if turnSpan != nil {
			observability.RecordError(turnSpan, err)
			turnSpan.End()
		}
		l.State.Messages = *messages
		return err
	}
	return nil
}

// recordTurnSpanAttrs populates OTel span attributes for the current
// turn: tool call counts, token usage, and model info.
func (l *Loop) recordTurnSpanAttrs(turnSpan trace.Span, messages []types.Message, msg types.Message, tokensBeforeTurn types.Usage, iter int, streamed bool) {
	toolNames := make([]string, 0, len(msg.ToolCalls))
	for _, tc := range msg.ToolCalls {
		toolNames = append(toolNames, tc.Function.Name)
	}
	turnAttrs := []attribute.KeyValue{
		attribute.Bool("turn.streamed", streamed),
		attribute.Int("turn.iteration", iter),
		attribute.Int("turn.tool_calls", len(msg.ToolCalls)),
		attribute.String("turn.tool_call_names", strings.Join(toolNames, ",")),
		attribute.Int("turn.messages", len(messages)),
		attribute.String("llm.model", l.Config.Model),
		attribute.Int("context.window", l.Config.ContextWindow),
		attribute.Int("llm.total_prompt_tokens", l.State.TotalTokens.PromptTokens),
		attribute.Int("llm.total_completion_tokens", l.State.TotalTokens.CompletionTokens),
		attribute.Int("turn.prompt_tokens", l.State.TotalTokens.PromptTokens-tokensBeforeTurn.PromptTokens),
		attribute.Int("turn.completion_tokens", l.State.TotalTokens.CompletionTokens-tokensBeforeTurn.CompletionTokens),
	}
	if l.State.TotalReasoningTokens > 0 {
		turnAttrs = append(turnAttrs, attribute.Int("llm.total_reasoning_tokens", l.State.TotalReasoningTokens))
	}
	if l.State.TotalCachedPromptTokens > 0 {
		turnAttrs = append(turnAttrs, attribute.Int("llm.total_cached_prompt_tokens", l.State.TotalCachedPromptTokens))
	}
	turnSpan.SetAttributes(turnAttrs...)
}

// executeToolPhase merges middleware-synthesized tool results (approval
// denials, inline-limit drops), executes the remaining calls via
// executeAndCollect, and invokes the PostTool middleware step.
// Call filtering itself lives in the pipeline (approval / inline_limit).
func (l *Loop) executeToolPhase(turnCtx context.Context, iter int, msg types.Message, messages *[]types.Message, step **pipeline.Step, pipe *pipeline.Pipeline, turnSpan trace.Span) error {
	// MaxInlineToolsPerTurn truncation and approval denials are applied
	// by the pipeline's PostModel middleware (inline_limit / approval),
	// which synthesizes results for stripped calls on step.SynthesizedResults.
	calls := msg.ToolCalls

	if l.Config.OtelVerbose && turnSpan != nil && (*step).InlineDropped > 0 {
		turnSpan.AddEvent("inline.truncated", trace.WithAttributes(
			attribute.Int("inline.dropped", (*step).InlineDropped),
		))
	}

	if l.Config.OtelVerbose && turnSpan != nil {
		names := make([]string, len(calls))
		for i, tc := range calls {
			names[i] = tc.Function.Name
		}
		turnSpan.AddEvent("dispatch.inline", trace.WithAttributes(
			attribute.Int("inline.count", len(calls)),
			attribute.String("inline.tool_names", strings.Join(names, ",")),
		))
	}

	// Middleware (approval, inline_limit) may have stripped tool calls
	// before dispatch; their synthesized results must enter the
	// conversation ahead of the executed-call results so every
	// tool_call_id on the assistant message is answered.
	for _, m := range (*step).SynthesizedResults {
		*messages = append(*messages, m)
		l.Persister.Persist(m)
	}
	(*step).SynthesizedResults = nil

	toolResults := l.executeAndCollect(turnCtx, calls, messages)
	(*step).Messages = *messages

	var err error
	*step, err = pipe.RunPostTool(turnCtx, toolResults, *step)
	if err != nil {
		if turnSpan != nil {
			turnSpan.End()
		}
		l.State.Messages = *messages
		return err
	}

	return nil
}

// injectWrapUpNotice appends a transient wrap-up notice to the request,
// warning the model that its iteration budget is nearly exhausted so it
// finishes and summarizes before tools are stripped or the run ends.
// The notice lives only in the request — it is never persisted to the
// conversation history, and the countdown updates on each iteration.
func (l *Loop) injectWrapUpNotice(req *types.ChatRequest, turnSpan trace.Span, remaining int) {
	req.Messages = append(req.Messages, types.UserMsg(prompts.WrapUpMessage(remaining)))
	if l.Config.OtelEnabled && turnSpan != nil {
		turnSpan.AddEvent("maxturns.wrap_up", trace.WithAttributes(
			attribute.Int("maxturns.remaining", remaining),
		))
	}
}
