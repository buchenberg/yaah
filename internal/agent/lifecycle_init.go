package agent

import (
	"context"
	"time"

	"github.com/buchenberg/yaah/internal/agent/events"
	"github.com/buchenberg/yaah/internal/agent/llm"
	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/pubsub"
	"github.com/buchenberg/yaah/internal/types"
)

// initMessages appends the user input to the conversation, persisting
// messages and emitting session-start hooks for new conversations.
func (l *Loop) initMessages(userInput string) {
	// Seed continuation history (supervised review sessions) before the
	// append below, so the user input lands after the seeded turns.
	if len(l.State.Messages) == 0 && len(l.Config.InitialMessages) > 0 {
		l.State.Messages = append(l.State.Messages, l.Config.InitialMessages...)
	}
	if l.State.Messages != nil {
		l.State.Messages = append(l.State.Messages, types.UserMsg(userInput))
		l.Persister.Persist(l.State.Messages[len(l.State.Messages)-1])
	} else {
		l.State.Messages = []types.Message{
			types.SystemMsg(l.Config.SystemPrompt),
			types.UserMsg(userInput),
		}
		l.Hooks.Emit(HookEvent{
			Event: events.SessionStart,
			Model: l.Config.Model,
		})
		l.Persister.Persist(l.State.Messages[0])
		l.Persister.Persist(l.State.Messages[1])
	}
}

// ctxMgr returns the CtxMgr, creating one lazily if needed.
// This avoids nil panics when tests call context methods directly
// without going through Run → applyDefaults. It is called from
// concurrent tool goroutines (truncateToolResult), so the lazy fill
// is mutex-guarded.
func (l *Loop) ctxMgr() *ContextManager {
	l.ctxMgrMu.Lock()
	defer l.ctxMgrMu.Unlock()
	if l.CtxMgr == nil {
		var db *memory.DB
		if l.Persister != nil {
			db = l.Persister.DB()
		}
		l.CtxMgr = &ContextManager{
			ContextWindow:  l.Config.ContextWindow,
			EstimateFactor: l.Config.EstimateFactor,
			OtelEnabled:    l.Config.OtelEnabled,
			SystemPrompt:   l.Config.SystemPrompt,
			SessionID:      l.Config.SessionID,
			DB:             db,
			State:          &l.State,
		}
	}
	if l.CtxMgr.State == nil {
		l.CtxMgr.State = &l.State
	}
	if l.CtxMgr.OnMessagesReplaced == nil {
		l.CtxMgr.OnMessagesReplaced = l.rebasePersistence
	}
	if l.State.CompactionBudgetMultiplier <= 0 {
		l.State.CompactionBudgetMultiplier = 1.0
	}
	if l.CtxMgr.Provider == nil {
		l.CtxMgr.Provider = l.Provider
	}
	if l.CtxMgr.CompactProvider == nil {
		l.CtxMgr.CompactProvider = l.CompactProvider
	}
	if l.CtxMgr.Model == "" {
		l.CtxMgr.Model = l.Config.Model
	}
	if l.CtxMgr.CompactModel == "" {
		l.CtxMgr.CompactModel = l.Config.CompactModel
	}
	if l.CtxMgr.ContextWindow == 0 {
		l.CtxMgr.ContextWindow = l.Config.ContextWindow
	}
	if l.CtxMgr.RawCompactionThreshold == 0 {
		l.CtxMgr.RawCompactionThreshold = l.Config.RawCompactionThreshold
	}
	if l.CtxMgr.CompactMaxMessages == 0 {
		l.CtxMgr.CompactMaxMessages = l.Config.CompactMaxMessages
	}
	if l.CtxMgr.ReasoningProtectTurns <= 0 {
		l.CtxMgr.ReasoningProtectTurns = 2
	}
	l.CtxMgr.EnsurePruner()
	return l.CtxMgr
}

// applyDefaults sets default values for Loop fields.
func (l *Loop) applyDefaults() {
	if l.CtxMgr == nil {
		l.CtxMgr = NewContextManager(l.Provider, l.Config.Model)
	}
	l.CtxMgr.State = &l.State
	l.CtxMgr.OnMessagesReplaced = l.rebasePersistence
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
		l.State.Messages = msgs
		l.compactContext(ctx, thresh)
		return l.State.Messages
	}

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
		l.Hooks = events.NewHookEmitter("", l.Config.SessionID)
	}
	if l.Persister == nil {
		l.Persister = NewSessionPersister(nil, nil, l.Config.SessionID)
	}
	// (Re)create the event broker for this Run. publishDone closes the
	// broker at the end of every Run; a reused Loop re-arms eventing here
	// instead of silently dropping events on later Runs (finding A4).
	if l.View != nil && (l.broker == nil || l.brokerClosed) {
		l.broker = pubsub.NewBroker[Event]()
		l.brokerView = NewBrokerView(l.broker, l.View)
		l.brokerClosed = false
	}
	if l.LLM == nil {
		var onToken llm.TokenCallback
		var onThinking llm.ThinkingCallback
		if l.broker != nil {
			onToken = l.publishTokenDelta
			onThinking = l.publishThinking
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
	} else if l.broker != nil {
		// Rebind the streaming callbacks to the current broker — closures
		// captured by a previous Run reference a closed broker.
		l.LLM.OnToken = l.publishTokenDelta
		l.LLM.OnThinking = l.publishThinking
	}
}

// publishTokenDelta and publishThinking are method values so rebinding
// across broker generations always targets the live l.broker field.
func (l *Loop) publishTokenDelta(token string) {
	if l.broker != nil {
		l.broker.Publish(&TokenDeltaEvent{Text: token})
	}
}

func (l *Loop) publishThinking(text string) {
	if l.broker != nil {
		l.broker.Publish(&ThinkingEvent{Text: text})
	}
}
