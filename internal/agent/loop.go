package agent

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/observability"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// buildPipeline assembles the middleware pipeline from config.
func (l *Loop) buildPipeline() *pipeline.Pipeline {
	if len(l.Middleware) > 0 {
		return pipeline.NewPipeline(l.Middleware...)
	}
	return pipeline.NewFromConfig(l.toPipelineConfig())
}

// Compact satisfies the pipeline.Compactor interface by delegating to
// the Loop's context compaction machinery. It syncs step messages into
// l.State.Messages, compacts, and returns the result.
func (l *Loop) Compact(ctx context.Context, messages []types.Message, threshold float64) []types.Message {
	l.State.Messages = messages
	l.compactContext(ctx, threshold)
	return l.State.Messages
}

// toPipelineConfig builds a PipelineConfig from the Loop's current settings.
func (l *Loop) toPipelineConfig() pipeline.PipelineConfig {
	return pipeline.PipelineConfig{
		Steer:                  l.Steer,
		FollowUps:              l.FollowUps,
		ContextWindow:          l.Config.ContextWindow,
		CompactionThreshold:    l.Config.CompactionThreshold,
		Compactor:              l,
		ApprovalMode:           l.Config.ApprovalMode,
		PermissionRules:        l.Config.PermissionRules,
		LoopDetectCount:        l.Config.LoopDetectCount,
		LoopDetectWindow:       l.Config.LoopDetectWindow,
		MaxToolConcurrency:     l.Config.MaxToolConcurrency,
		MaxSubAgentConcurrency: l.Config.MaxSubAgentConcurrency,
		PromptCaching:          l.Config.PromptCaching,
		Pruner:                 l.CtxMgr.Pruner,
		PruneHooks:             l.pruneHooks(),
		PipelineNames:          l.Config.PipelineNames,
		PipelineDisabled:       l.Config.PipelineDisabled,
	}
}

// Run executes the full conversation loop for a single user message
// using the middleware pipeline.
func (l *Loop) Run(ctx context.Context, userInput string) (response string, runErr error) {
	if l.Config.OtelEnabled {
		var rootSpan trace.Span
		ctx, rootSpan = observability.StartPrompt(ctx, userInput)
		defer func() {
			if runErr != nil {
				observability.RecordError(rootSpan, runErr)
			}
			rootSpan.End()
		}()
	}
	return l.runMiddleware(ctx, userInput)
}

// runMiddleware executes the agent loop using the middleware pipeline.
func (l *Loop) runMiddleware(ctx context.Context, userInput string) (response string, runErr error) {
	defer l.publishDone(&response, &runErr)
	defer l.teardown(&runErr)

	l.applyDefaults()
	l.initMessages(userInput)

	messages := l.State.Messages
	pipe := l.buildPipeline()

	for iter := 0; iter < l.Config.MaxLoopCycles; iter++ {
		turnStart := time.Now()

		select {
		case <-ctx.Done():
			l.State.Messages = messages
			return "", ctx.Err()
		default:
		}

		tools.SendHeartbeat(ctx)

		l.Hooks.Emit(HookEvent{
			Event:  TurnStart,
			Prompt: userInput,
			Turn:   iter,
			Model:  l.Config.Model,
		})

		var turnSpan trace.Span
		turnCtx := ctx
		if l.Config.OtelEnabled {
			turnCtx, turnSpan = observability.StartTurn(ctx, iter, userInput)
		}

		step, req, err := l.buildTurnRequest(ctx, iter, messages, pipe, turnSpan)
		if err != nil {
			l.State.Messages = messages
			return "", err
		}
		messages = step.Messages

		if err := l.guardContextBeforeCall(turnCtx, &messages, &req, turnSpan); err != nil {
			return "", err
		}

		tokensBeforeTurn := l.State.TotalTokens
		llmStart := time.Now()

		result, err := l.LLM.Call(turnCtx, req)
		if err != nil {
			if turnSpan != nil {
				observability.RecordError(turnSpan, err)
				turnSpan.End()
			}
			l.State.Messages = messages
			return "", fmt.Errorf("provider error: %w", err)
		}
		msg := result.Message
		l.Provider = l.LLM.Provider
		l.Config.Model = l.LLM.Model
		l.FallbackProvider = l.LLM.FallbackProvider
		l.FallbackModel = l.LLM.FallbackModel
		l.addUsage(result.Usage)
		l.State.LastFinishReason = result.FinishReason
		l.State.LastResponseModel = result.ResponseModel
		messages = append(messages, msg)
		l.Persister.Persist(msg)

		observability.RecordLLMCall(turnCtx, time.Since(llmStart), result.Usage.PromptTokens, result.Usage.CompletionTokens)

		if turnSpan != nil {
			l.recordTurnSpanAttrs(turnSpan, messages, msg, tokensBeforeTurn, iter, result.Streamed)
		}

		if len(msg.ToolCalls) == 0 {
			if turnSpan != nil {
				turnSpan.End()
			}
			l.State.Messages = messages
			observability.RecordAgentTurn(turnCtx, time.Since(turnStart))
			return msg.Content, nil
		}

		if result.Streamed && msg.Content != "" && l.broker != nil {
			l.broker.PublishMustDeliver(&FlushEvent{Content: msg.Content})
		}

		step, err = pipe.RunPostModel(ctx, &msg, step)
		if err != nil {
			l.State.Messages = messages
			return "", err
		}

		err = l.executeToolPhase(turnCtx, iter, msg, &messages, &step, pipe, turnSpan)
		if err != nil {
			return "", err
		}

		if turnSpan != nil {
			turnSpan.End()
		}

		observability.RecordAgentTurn(turnCtx, time.Since(turnStart))

		messages = step.Messages
		for i := l.Persister.MsgIdx(); i < len(messages); i++ {
			l.Persister.Persist(messages[i])
		}
	}

	l.State.Messages = messages
	return "", fmt.Errorf("max iterations (%d) reached", l.Config.MaxLoopCycles)
}
