package yaah

import (
	"context"
	"time"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/observability"
	"github.com/buchenberg/yaah/internal/providers"
	"github.com/buchenberg/yaah/internal/types"
)

// runPrompt executes a single agent prompt with the session's shared
// infrastructure. The caller must set a view via SetView before calling.
func (s *agentSession) runPrompt(ctx context.Context, prompt string) (string, bool, error) {
	compactProvider, compactModel := resolveCompact(s.cfg)
	if compactProvider != nil {
		if sp, ok := compactProvider.(agent.StreamProvider); ok {
			compactProvider = &observability.InstrumentedProvider{Inner: sp, Verbose: s.cfg.Observability.Otel.Verbose}
		}
	}
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

	var debouncer *memory.DebouncedWriter
	if s.db != nil {
		debouncer = memory.NewDebouncedWriter(s.db)
	}
	persister := agent.NewSessionPersister(s.db, debouncer, s.sessionID)
	persister.SetMsgIdx(s.msgIdx)
	hooks := agent.NewHookEmitter(s.cfg.Hooks.Dir, s.sessionID)

	loop := agent.NewLoop(prov, s.toolReg,
		agent.WithModel(mName),
		agent.WithSystemPrompt(s.mainPrompt),
		agent.WithView(v),
		agent.WithMessages(s.messages),
		agent.WithSessionID(s.sessionID),
		agent.WithPersister(persister),
		agent.WithHooks(hooks),
		agent.WithFallback(fallbackProvider, fallbackModel),
		agent.WithCompactProvider(compactProvider, compactModel),
		agent.WithPipeline(s.cfg.Agent.Middleware.Enabled, s.cfg.Agent.Middleware.Disabled),
		agent.WithSteer(s.steerCh),
		agent.WithFollowUps(s.followupCh),
		agent.WithBackgroundJobs(s.backgroundJobs),
		agent.WithConflictTracker(s.tracker),
		agent.WithToolsLevel(agent.FullTools),
		agent.WithOtel(s.cfg.Observability.Otel.Enabled, s.cfg.Observability.Otel.Verbose),
		agent.WithSubAgentConcurrency(
			s.cfg.Agent.SubAgent.MaxConcurrency,
			time.Duration(s.cfg.Agent.SubAgent.StuckChildTimeout)*time.Second,
			buildStuckChildTimeouts(s.cfg.Agent.SubAgent),
		),
		agent.WithAgentConfig(agent.AgentConfig{
			MaxLoopCycles:          s.cfg.Agent.Default.MaxLoopCycles,
			MaxToolTurns:           s.cfg.Agent.Default.MaxToolTurns,
			MaxRetries:             s.cfg.Agent.Default.MaxRetries,
			RetryBackoffSecs:       s.cfg.Agent.Default.RetryBackoffSecs,
			ContextWindow:          providers.ResolveWindow(mName, s.cfg.Agent.Default.ContextWindow),
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
		}),
		agent.WithApprovalMode(resolveApproval(s.cfg)),
	)

	if appr != nil {
		loop.ApproveFn = appr
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
