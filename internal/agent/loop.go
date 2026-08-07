package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/buchenberg/yaah/internal/agent/events"
	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/observability"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// ErrMaxIterations is returned when the agent loop exhausts its configured
// iteration budget without producing a text answer.
var ErrMaxIterations = errors.New("max iterations reached")

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
	l.CtxMgr.Messages = messages
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
		Compactor:              l.CtxMgr,
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

	// Register the loop's broker event hooks on the session-scoped
	// background manager so background sub-agents emit live
	// SubAgentStart/End events while this loop is active. Cleared on
	// return so jobs that finish between Runs do not publish to a
	// retired broker. Usage attribution lives on the manager (session
	// scope) and is therefore never lost.
	l.wireBackgroundHooks()
	defer l.unwireBackgroundHooks()

	l.applyDefaults()
	l.initMessages(userInput)

	messages := l.State.Messages
	pipe := l.buildPipeline()

	for {
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
				Event:  events.TurnStart,
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
		// max iterations reached — ask whether to continue
		if l.ContinueAfterMaxIter == nil || !l.ContinueAfterMaxIter() {
			l.State.Messages = messages
			return "", fmt.Errorf("%w (%d)", ErrMaxIterations, l.Config.MaxLoopCycles)
		}
	}
}

// wireBackgroundHooks registers the loop's broker as the event sink for
// background sub-agent start/end events while this loop is active. It is
// a no-op when no background manager is attached or no broker exists.
func (l *Loop) wireBackgroundHooks() {
	if l.BackgroundJobs == nil {
		return
	}
	l.BackgroundJobs.OnStart = func(id, role, model, prompt string) {
		if l.broker != nil {
			l.broker.PublishMustDeliver(&SubAgentStartEvent{SubAgentID: id, Role: role, Model: model, Prompt: prompt})
		}
	}
	l.BackgroundJobs.OnEnd = func(id, role, model, prompt, result string, dur time.Duration, err string) {
		if l.broker != nil {
			l.broker.PublishMustDeliver(&SubAgentEndEvent{SubAgentID: id, Role: role, Model: model, Prompt: prompt, Duration: dur, Error: err, Result: result})
		}
	}
}

// unwireBackgroundHooks clears the loop-scoped event hooks so background
// jobs finishing between Runs do not publish to a retired broker. Usage
// attribution (OnUsage) is session-scoped on the manager and is left
// intact.
func (l *Loop) unwireBackgroundHooks() {
	if l.BackgroundJobs == nil {
		return
	}
	l.BackgroundJobs.OnStart = nil
	l.BackgroundJobs.OnEnd = nil
}
