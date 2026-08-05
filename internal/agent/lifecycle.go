package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/buchenberg/yaah/internal/agent/llm"
	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/pubsub"
	"github.com/buchenberg/yaah/internal/types"
)

// publishDone publishes the final DoneEvent to the broker view.
func (l *Loop) publishDone(response *string, runErr *error) {
	if l.brokerView == nil {
		return
	}
	var done DoneEvent
	done.Response = *response
	if *runErr != nil {
		done.Error = (*runErr).Error()
	}
	done.ContextTokens = l.EstimatedTokens()
	done.ContextWindow = l.Config.ContextWindow
	done.LastPromptTokens = l.State.LastPromptTokens
	done.FinishReason = l.State.LastFinishReason
	done.ResponseModel = l.State.LastResponseModel
	done.Usage = l.State.TotalTokens
	if l.State.TotalReasoningTokens > 0 {
		done.Usage.CompletionTokensDetails = &types.CompletionTokensDetails{
			ReasoningTokens: l.State.TotalReasoningTokens,
		}
	}
	if l.State.TotalCachedPromptTokens > 0 {
		done.Usage.PromptTokensDetails = &types.PromptTokensDetails{
			CachedTokens: l.State.TotalCachedPromptTokens,
		}
	}
	l.broker.PublishMustDeliver(&done)
	l.brokerView.Close()
}

// teardown handles panic recovery, flushes the persister, closes hooks,
// and emits the session-end event.
func (l *Loop) teardown(runErr *error) {
	if r := recover(); r != nil {
		*runErr = fmt.Errorf("panic: %v", r)
	}
	l.Persister.Flush()
	l.Hooks.Close()
	reason := "completed"
	if *runErr != nil {
		reason = "error"
	}
	l.Hooks.Emit(HookEvent{
		Event:      SessionEnd,
		ExitReason: reason,
		Model:      l.Config.Model,
	})
}

// initMessages appends the user input to the conversation, persisting
// messages and emitting session-start hooks for new conversations.
func (l *Loop) initMessages(userInput string) {
	if l.State.Messages != nil {
		l.State.Messages = append(l.State.Messages, types.UserMsg(userInput))
		l.Persister.Persist(l.State.Messages[len(l.State.Messages)-1])
	} else {
		l.State.Messages = []types.Message{
			types.SystemMsg(l.Config.SystemPrompt),
			types.UserMsg(userInput),
		}
		l.Hooks.Emit(HookEvent{
			Event: SessionStart,
			Model: l.Config.Model,
		})
		l.Persister.Persist(l.State.Messages[0])
		l.Persister.Persist(l.State.Messages[1])
	}
}

// ctxMgr returns the CtxMgr, creating one lazily if needed.
// This avoids nil panics when tests call context methods directly
// without going through Run → applyDefaults.
func (l *Loop) ctxMgr() *ContextManager {
	if l.CtxMgr == nil {
		l.CtxMgr = &ContextManager{}
	}
	return l.CtxMgr
}

// applyDefaults sets default values for Loop fields.
func (l *Loop) applyDefaults() {
	if l.CtxMgr == nil {
		l.CtxMgr = NewContextManager(l.Provider, l.Config.Model)
		l.CtxMgr.PreviousSummary = l.State.PreviousSummary
		l.CtxMgr.LastPromptTokens = l.State.LastPromptTokens
		l.CtxMgr.LastCachedPromptTokens = l.State.LastCachedPromptTokens
		l.CtxMgr.LastCompactionTokens = l.State.LastCompactionTokens
		l.CtxMgr.IneffectiveCompactions = l.State.IneffectiveCompactions
	}
	l.CtxMgr.ContextWindow = l.Config.ContextWindow
	l.CtxMgr.CompactionThreshold = l.Config.CompactionThreshold
	l.CtxMgr.RawCompactionThreshold = l.Config.RawCompactionThreshold
	l.CtxMgr.EstimateFactor = l.Config.EstimateFactor
	l.CtxMgr.CompactProvider = l.CompactProvider
	l.CtxMgr.CompactModel = l.Config.CompactModel
	l.CtxMgr.SessionID = l.Config.SessionID
	l.CtxMgr.OtelEnabled = l.Config.OtelEnabled
	if l.CtxMgr.ReasoningProtectTurns <= 0 {
		l.CtxMgr.ReasoningProtectTurns = 2
	}
	l.CtxMgr.EnsurePruner()

	// Wire compaction functions so ContextManager can satisfy the
	// pipeline.Compactor interface and handle chunked fallback.
	l.CtxMgr.compactFn = func(ctx context.Context, msgs []types.Message, thresh float64) []types.Message {
		l.CtxMgr.Messages = msgs
		l.CtxMgr.compactContext(ctx, thresh)
		return l.CtxMgr.Messages
	}
	l.CtxMgr.chunkedCompactFn = l.chunkedCompact

	if l.State.CompactionBudgetMultiplier <= 0 {
		l.State.CompactionBudgetMultiplier = 1.0
	}
	if l.Config.MaxLoopCycles <= 0 {
		l.Config.MaxLoopCycles = 50
	}
	if l.Config.WrapUpThreshold == 0 {
		l.Config.WrapUpThreshold = 1
	}
	if l.Config.Model == "" {
		l.Config.Model = "deepseek-v4-pro"
	}
	if l.Config.RetryBackoff <= 0 {
		l.Config.RetryBackoff = time.Second
	}
	if l.Config.LoopDetectCount <= 0 {
		l.Config.LoopDetectCount = 5
	}
	if l.Config.LoopDetectWindow <= 0 {
		l.Config.LoopDetectWindow = 10
	}
	if l.Config.MaxToolConcurrency > 0 && l.toolConcurrency == nil {
		l.toolConcurrency = pipeline.NewToolConcurrencyMiddleware(l.Config.MaxToolConcurrency)
	}
	if l.Config.MaxSubAgentConcurrency > 0 && l.subAgentSem == nil {
		l.subAgentSem = make(chan struct{}, l.Config.MaxSubAgentConcurrency)
	}
	if l.Hooks == nil {
		l.Hooks = NewHookEmitter("", l.Config.SessionID)
	}
	if l.Persister == nil {
		l.Persister = NewSessionPersister(nil, nil, l.Config.SessionID)
	}
	if l.LLM == nil {
		if l.View != nil {
			l.broker = pubsub.NewBroker[Event]()
			l.brokerView = NewBrokerView(l.broker, l.View)
		}
		var onToken llm.TokenCallback
		var onThinking llm.ThinkingCallback
		if l.broker != nil {
			onToken = func(token string) {
				l.broker.Publish(&TokenDeltaEvent{Text: token})
			}
			onThinking = func(text string) {
				l.broker.Publish(&ThinkingEvent{Text: text})
			}
		}
		l.LLM = &llm.Client{
			Provider:         l.Provider,
			FallbackProvider: l.FallbackProvider,
			Model:            l.Config.Model,
			FallbackModel:    l.FallbackModel,
			MaxRetries:       l.Config.MaxRetries,
			RetryBackoff:     l.Config.RetryBackoff,
			ContextWindow:    l.Config.ContextWindow,
			SessionID:        l.Config.SessionID,
			OnToken:          onToken,
			OnThinking:       onThinking,
			Compact:          l.llmCompact,
			Trim:             l.llmTrim,
			StripReasoning:   l.StripAllReasoning,
			OtelEnabled:      l.Config.OtelEnabled,
			OtelVerbose:      l.Config.OtelVerbose,
		}
	}
}
