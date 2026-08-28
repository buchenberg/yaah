package agent

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/buchenberg/yaah/internal/agent/events"
	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/observability"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// MaxIterationsError is returned when the agent loop exhausts its configured
// iteration budget without producing a text answer. Use errors.Is with a zero
// value to match:
//
//	errors.Is(err, MaxIterationsError{})
type MaxIterationsError struct {
	MaxIter int
}

func (e MaxIterationsError) Error() string {
	return fmt.Sprintf("max iterations (%d) reached", e.MaxIter)
}

func (e MaxIterationsError) Is(target error) bool {
	_, ok := target.(MaxIterationsError)
	return ok
}

// buildPipeline assembles the middleware pipeline from config. Sub-agent
// loops get the curated sub-agent pipeline; the orchestrator gets the
// full default pipeline.
func (l *Loop) buildPipeline() *pipeline.Pipeline {
	if l.Config.IsSubAgent {
		return pipeline.NewSubAgentPipeline(l.toPipelineConfig())
	}
	return pipeline.NewFromConfig(l.toPipelineConfig())
}

// Compact satisfies the pipeline.Compactor interface (steer-drain
// path). It runs compaction against an isolated state view and returns
// the compacted slice; the middleware assigns it to step.Messages and
// runMiddleware adopts it — l.State.Messages is never written here
// (single-writer invariant, review finding B6).
func (l *Loop) Compact(ctx context.Context, messages []types.Message, threshold float64) []types.Message {
	return l.compactMessagesIsolated(ctx, messages, threshold)
}

// toPipelineConfig builds a PipelineConfig from the Loop's current settings.
func (l *Loop) toPipelineConfig() pipeline.PipelineConfig {
	return pipeline.PipelineConfig{
		Steer:               l.Steer,
		FollowUps:           l.FollowUps,
		ContextWindow:       l.Config.ContextWindow,
		CompactionThreshold: l.Config.CompactionThreshold,
		Compactor:           l.CtxMgr,
		SteerDrain: func(ctx context.Context, messages []types.Message) []types.Message {
			return l.Compact(ctx, messages, 0)
		},
		ApprovalMode:     l.Config.ApprovalMode,
		ApprovalClassify: l.classifyGate,
		ApprovalApprove:  func(name, args string) bool { return l.approveTool(name, abbreviateArgs(args, 120)) },
		ApprovalEmitDeny: func(name, args, errMsg string) {
			if l.Hooks == nil {
				return
			}
			l.Hooks.Emit(HookEvent{Event: events.ToolStart, ToolName: name, ToolArgs: args})
			l.Hooks.Emit(HookEvent{Event: events.ToolEnd, ToolName: name, ToolArgs: args, ToolError: errMsg, ToolResult: errMsg})
		},
		PermissionRules:        l.Config.PermissionRules,
		LoopDetectCount:        l.Config.LoopDetectCount,
		LoopDetectWindow:       l.Config.LoopDetectWindow,
		MaxToolConcurrency:     l.Config.MaxToolConcurrency,
		MaxInlineToolsPerTurn:  l.Config.MaxInlineToolsPerTurn,
		ToolConc:               l.toolConcurrency,
		MaxSubAgentConcurrency: l.Config.MaxSubAgentConcurrency,
		PromptCaching:          l.Config.PromptCaching,
		Pruner:                 l.CtxMgr.Pruner,
		PruneHooks:             l.pruneHooks(),
		PipelineNames:          l.Config.PipelineNames,
		PipelineDisabled:       l.Config.PipelineDisabled,
		ConflictTracker:        l.ConflictTracker,
		ConflictOnCheck: func(ctx context.Context, model string, turn int) {
			if l.Hooks == nil {
				return
			}
			l.Hooks.Emit(HookEvent{Event: events.ConflictCheck, Turn: turn, Model: model})
		},
		ConflictOnFound: func(ctx context.Context, model string, turn int, report string, fileCount int) {
			if l.Hooks != nil {
				l.Hooks.Emit(HookEvent{Event: events.ConflictDetect, Turn: turn, Model: model, ConflictFiles: fileCount})
			}
			if span := trace.SpanFromContext(ctx); span.IsRecording() {
				span.SetAttributes(attribute.Int("conflict.files", fileCount))
				span.AddEvent("conflict.detected", trace.WithAttributes(
					attribute.Int("conflict.files", fileCount),
				))
			}
		},
		ConflictPersist: func(msg types.Message) {
			if l.Persister != nil {
				l.Persister.Persist(msg)
			}
		},

		SessionID: l.Config.SessionID,
	}
}

// Run executes the full conversation loop for a single user message
// using the middleware pipeline.
func (l *Loop) Run(ctx context.Context, userInput string) (response string, runErr error) {
	// turnID is the stable per-interaction cross-link shared by the OTel
	// spans, the persisted messages, and the Shepherd turn facts of this
	// prompt-to-answer exchange.
	turnID := newTurnID()
	if l.Config.OtelEnabled {
		var rootSpan trace.Span
		ctx, rootSpan = observability.StartPrompt(ctx, l.Config.SessionID, turnID, userInput)
		defer func() {
			if runErr != nil {
				observability.RecordError(rootSpan, runErr)
			}
			rootSpan.End()
		}()
	}
	if l.Persister != nil {
		traceID := ""
		if l.Config.OtelEnabled {
			traceID = observability.TraceIDFromContext(ctx)
		}
		l.Persister.SetTurnContext(traceID, turnID)
	}
	return l.runMiddleware(ctx, userInput, turnID)
}

// runMiddleware executes the agent loop using the middleware pipeline.
func (l *Loop) runMiddleware(ctx context.Context, userInput, turnID string) (response string, runErr error) {
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

	// Release unused turn checkpoints when the Run ends, success or
	// failure — restores consume their own checkpoints as they happen.
	if l.turnCheckpointActive() {
		defer func() {
			if err := l.Config.TurnCheckpointer.Prune(ctx); err != nil {
				slog.Debug("turn_checkpoint: final prune failed", "err", err)
			}
			l.State.TurnCheckpoints = nil
		}()
	}

	l.applyDefaults()
	l.initMessages(userInput)

	// messages is the authoritative working copy of the conversation for
	// this Run. l.State.Messages has exactly two writers: the deferred
	// sync below (every exit path) and the end-of-iteration sync so
	// mid-run readers (diagnostics, UI usage, checkpoints) see fresh
	// state. Compaction results are adopted explicitly from the LLM
	// client's return path (review finding B6).
	messages := l.State.Messages
	defer func() { l.State.Messages = messages }()
	pipe := l.buildPipeline()
	traceMw := pipe.ShepherdTraceMiddleware()

	for {
		for iter := 0; iter < l.Config.MaxLoopCycles; iter++ {
			turnStart := time.Now()
			if traceMw != nil {
				traceMw.SetTurnContext(turnID)
				traceMw.StartTurn(iter, l.Config.Model, userInput)
			}

			select {
			case <-ctx.Done():
				if traceMw != nil {
					traceMw.FailTurn(iter, ctx.Err())
				}
				return "", ctx.Err()
			default:
			}

			tools.SendHeartbeat(ctx)

			// Snapshot workspace + conversation before the model turn so
			// a failed turn can be rewound to this point.
			l.checkpointTurn(ctx, messages)

			l.Hooks.Emit(HookEvent{
				Event:  events.TurnStart,
				Prompt: userInput,
				Turn:   iter,
				Model:  l.Config.Model,
			})

			var turnSpan trace.Span
			turnCtx := ctx
			if l.Config.OtelEnabled {
				turnCtx, turnSpan = observability.StartTurn(ctx, iter, turnID, userInput)
			}

			step, req, err := l.buildTurnRequest(ctx, iter, messages, pipe, turnSpan)
			if err != nil {
				if traceMw != nil {
					traceMw.FailTurn(iter, err)
				}
				return "", err
			}
			messages = step.Messages

			if err := l.guardContextBeforeCall(turnCtx, &messages, &req, turnSpan); err != nil {
				if traceMw != nil {
					traceMw.FailTurn(iter, err)
				}
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
				// Adopt overflow-recovery compaction even on failure: the
				// compacted baseline was already persistence-rebased, so
				// the in-memory state must match it (B6).
				if result.Compacted {
					messages = result.CompactedMessages
				}
				if traceMw != nil {
					traceMw.FailTurn(iter, err)
				}
				return "", fmt.Errorf("provider error: %w", err)
			}
			msg := result.Message

			// Adopt overflow-recovery compaction (or trimming) at the
			// defined compaction point — Call never mutates loop state
			// directly (B6).
			if result.Compacted {
				messages = result.CompactedMessages
			}

			// Short-lived diagnostic span — survives parent crash.
			observability.RecordTurnResponse(turnCtx, len(msg.Content), len(msg.ToolCalls),
				toolCallNames(msg.ToolCalls), result.FinishReason)

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
				l.State.Messages = messages // end-of-iteration sync; the deferred write covers the return
				observability.RecordAgentTurn(turnCtx, time.Since(turnStart))
				if traceMw != nil {
					traceMw.EndTurn(iter, result.Usage.PromptTokens, result.Usage.CompletionTokens)
				}
				return msg.Content, nil
			}

			if result.Streamed && msg.Content != "" && l.broker != nil {
				l.broker.PublishMustDeliver(&FlushEvent{Content: msg.Content})
			}

			step, err = pipe.RunPostModel(ctx, &msg, step)
			if err != nil {
				if traceMw != nil {
					traceMw.FailTurn(iter, err)
				}
				return "", err
			}

			observability.RecordToolDispatch(turnCtx, len(msg.ToolCalls),
				toolCallNames(msg.ToolCalls))

			err = l.executeToolPhase(turnCtx, iter, msg, &messages, &step, pipe, turnSpan)
			observability.RecordToolDispatchDone(turnCtx, len(msg.ToolCalls), err)
			if err != nil {
				if traceMw != nil {
					traceMw.FailTurn(iter, err)
				}
				guidance := fmt.Sprintf(turnFailureGuidanceFormat, err)
				if l.restoreLastTurnCheckpoint(ctx, &messages, guidance) {
					if turnSpan != nil {
						turnSpan.End()
					}
					continue
				}
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
			l.State.Messages = messages // end-of-iteration sync (single deferred write covers exits)
		}
		// max iterations reached — ask whether to continue
		if l.ContinueAfterMaxIter == nil || !l.ContinueAfterMaxIter() {
			// Rewind the last turn and retry with a fresh budget until
			// the restore cap is hit; then fall through to the error.
			if l.restoreLastTurnCheckpoint(ctx, &messages, exhaustionGuidance) {
				continue
			}
			err := MaxIterationsError{MaxIter: l.Config.MaxLoopCycles}
			if traceMw != nil {
				traceMw.FailTurn(l.Config.MaxLoopCycles, err)
			}
			return "", err
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
	l.BackgroundJobs.SetLoopHooks(
		func(id, role, model, prompt string) {
			if l.broker != nil {
				l.broker.PublishMustDeliver(&SubAgentStartEvent{SubAgentID: id, Role: role, Model: model, Prompt: prompt})
			}
		},
		func(id, role, model, prompt, result string, dur time.Duration, err string) {
			if l.broker != nil {
				l.broker.PublishMustDeliver(&SubAgentEndEvent{SubAgentID: id, Role: role, Model: model, Prompt: prompt, Duration: dur, Error: err, Result: result})
			}
		},
	)
}

// toolCallNames extracts the function names from a slice of tool calls.
func toolCallNames(calls []types.ToolCall) []string {
	names := make([]string, len(calls))
	for i, tc := range calls {
		names[i] = tc.Function.Name
	}
	return names
}

// unwireBackgroundHooks clears the loop-scoped event hooks so background
// jobs finishing between Runs do not publish to a retired broker. Usage
// attribution (OnUsage) is session-scoped on the manager and is left
// intact.
func (l *Loop) unwireBackgroundHooks() {
	if l.BackgroundJobs == nil {
		return
	}
	l.BackgroundJobs.SetLoopHooks(nil, nil)
}
