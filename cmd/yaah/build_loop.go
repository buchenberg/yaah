package yaah

import (
	"context"
	"time"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/providers"
	"github.com/buchenberg/yaah/internal/types"
)

// runPrompt executes a single agent prompt with the session's shared
// infrastructure. The caller must set a view via SetView before calling.
func (s *agentSession) runPrompt(ctx context.Context, prompt string) (string, bool, error) {
	rawCompactProvider, compactModel := resolveCompact(s.cfg)
	compactProvider := agent.ResolveCompactProvider(rawCompactProvider, s.cfg.Observability.Otel.Verbose)
	fallbackProvider, fallbackModel, fallbackProviderName := resolveFallback(s.cfg)

	s.mu.RLock()
	prov := s.provider
	mName := s.modelName
	v := s.view
	ctrl := s.ctrlCh
	appr := s.approveFn
	s.mu.RUnlock()

	if v == nil {
		v = agent.NoopView{}
	}

	b := s.loopBuilder(prov, mName, compactProvider, compactModel, fallbackProvider, fallbackModel)

	otelEnabled := s.cfg.Observability.Otel.Enabled
	loop := b.Build(agent.LoopBuildOptions{
		View:         v,
		ApprovalMode: resolveApproval(s.cfg, s.opts),
		OtelEnabled:  &otelEnabled,
		OtelVerbose:  s.cfg.Observability.Otel.Verbose,
	})

	if appr != nil {
		loop.ApproveFn = appr
	}

	// Wire continuation callback for max iterations (UI modes: TUI, Web, ACP).
	// REPL mode handles this differently (inline prompt in repl_loop.go).
	if v != nil && ctrl != nil {
		loop.ContinueAfterMaxIter = func() bool {
			maxIter := s.cfg.Agent.Default.MaxLoopCycles
			ch := make(chan bool, 1)
			select {
			case ctrl <- &types.CtrlContinue{MaxIter: maxIter, AnswerCh: ch}:
			default:
				return false
			}
			answer, ok := <-ch
			return ok && answer
		}
	}

	response, err := loop.Run(ctx, prompt)

	s.messages = loop.State.Messages
	s.msgIdx = loop.Persister.MsgIdx()

	s.mu.Lock()
	s.totalUsage.PromptTokens += loop.State.TotalTokens.PromptTokens
	s.totalUsage.CompletionTokens += loop.State.TotalTokens.CompletionTokens
	s.totalUsage.TotalTokens += loop.State.TotalTokens.TotalTokens
	s.mu.Unlock()

	if ctrl != nil {
		if loop.Config.Model != mName && fallbackProviderName != "" {
			select {
			case ctrl <- &types.CtrlFallback{Provider: fallbackProviderName, Model: loop.Config.Model}:
			default:
			}
		}
		if err != nil {
			select {
			case ctrl <- &types.CtrlError{Err: err}:
			default:
			}
		}
		select {
		case ctrl <- &types.CtrlContextInfo{
			Tokens:           loop.EstimatedTokens(),
			Window:           loop.Config.ContextWindow,
			LastPromptTokens: loop.State.LastPromptTokens,
		}:
		default:
		}
	}

	streamed := false
	if tv, ok := v.(*terminalView); ok {
		streamed = tv.streamed
	}

	return response, streamed, err
}

// loopBuilder constructs a LoopBuilder populated with the session's shared
// infrastructure. Both runPrompt (interactive) and runHeadless (serve)
// use this to avoid duplicating the ~20-field option construction.
func (s *agentSession) loopBuilder(
	prov agent.Provider,
	modelName string,
	compactProvider agent.Provider,
	compactModel string,
	fallbackProvider agent.Provider,
	fallbackModel string,
) *agent.LoopBuilder {
	var debouncer *memory.DebouncedWriter
	if s.db != nil {
		debouncer = memory.NewDebouncedWriter(s.db)
	}
	persister := agent.NewSessionPersister(s.db, debouncer, s.sessionID)
	persister.SetMsgIdx(s.msgIdx)
	hooks := agent.NewHookEmitter(s.cfg.Hooks.Dir, s.sessionID)

	return &agent.LoopBuilder{
		Provider:                   prov,
		Registry:                   s.toolReg,
		Model:                      modelName,
		SystemPrompt:               s.mainPrompt,
		Messages:                   s.messages,
		SessionID:                  s.sessionID,
		Persister:                  persister,
		Hooks:                      hooks,
		Steer:                      s.steerCh,
		FollowUps:                  s.followupCh,
		BackgroundJobs:             s.backgroundJobs,
		ConflictTracker:            s.tracker,
		CompactProvider:            compactProvider,
		CompactModel:               compactModel,
		FallbackProvider:           fallbackProvider,
		FallbackModel:              fallbackModel,
		PipelineEnabled:            s.cfg.Agent.Middleware.Enabled,
		PipelineDisabled:           s.cfg.Agent.Middleware.Disabled,
		SubAgentMaxConcurrency:     s.cfg.Agent.SubAgent.MaxConcurrency,
		SubAgentStuckChildTimeout:  time.Duration(s.cfg.Agent.SubAgent.StuckChildTimeout) * time.Second,
		SubAgentStuckChildTimeouts: buildStuckChildTimeouts(s.cfg.Agent.SubAgent),
		Cfg: agent.AgentConfig{
			MaxLoopCycles:          s.cfg.Agent.Default.MaxLoopCycles,
			MaxToolTurns:           s.cfg.Agent.Default.MaxToolTurns,
			MaxRetries:             s.cfg.Agent.Default.MaxRetries,
			RetryBackoffSecs:       s.cfg.Agent.Default.RetryBackoffSecs,
			ContextWindow:          providers.ResolveWindow(modelName, s.cfg.Agent.Default.ContextWindow),
			CompactionThreshold:    s.cfg.Agent.Default.CompactionThreshold,
			RawCompactionThreshold: s.cfg.Agent.Default.RawCompactionThreshold,
			CompactMaxMessages:     s.cfg.Agent.Default.CompactMaxMessages,
			EstimateFactor:         s.cfg.Agent.Default.EstimateFactor,
			QualityGates:           s.cfg.Agent.QualityGates,
			LoopDetectCount:        s.cfg.Agent.Default.LoopDetectCount,
			LoopDetectWindow:       s.cfg.Agent.Default.LoopDetectWindow,
			MaxToolConcurrency:     s.cfg.Agent.Default.MaxToolConcurrency,
			WrapUpThreshold:        s.cfg.Agent.Default.WrapUpThreshold,
			MaxInlineToolsPerTurn:  s.cfg.Agent.Default.MaxInlineToolsPerTurn,
			PromptCaching:          s.cfg.Agent.Default.PromptCaching,
			ReasoningProtectTurns:  s.cfg.Agent.Default.ReasoningProtect,
			ToolResultMaxLines:     s.cfg.Agent.Default.ToolResultMaxLines,
			ToolResultMaxBytes:     s.cfg.Agent.Default.ToolResultMaxBytes,
			PruneProtectTokens:     s.cfg.Agent.Default.PruneProtectTokens,
			PruneMinReclaim:        s.cfg.Agent.Default.PruneMinReclaim,
			PruneMinTurns:          s.cfg.Agent.Default.PruneMinTurns,
		},
	}
}
