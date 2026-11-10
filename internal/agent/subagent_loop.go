package agent

import (
	"strings"
	"time"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// SubAgentConfig holds the tuning parameters for a sub-agent loop.
// Unlike the main agent loop, sub-agents skip persistence, compaction,
// sub-agent spawning, quality gates, and hooks — they are ephemeral
// workers with a fixed turn budget.
type SubAgentConfig struct {
	// ProviderName labels the sub-agent's serving provider in error
	// envelopes. Empty means unknown; callers resolve it at dispatch.
	ProviderName  string
	MaxLoopCycles int
	MaxToolTurns  int
	// BudgetIterationsSource / BudgetTurnsSource record which precedence
	// branch resolved the two budgets, so traces and exhaustion errors can
	// answer "who set this?" (subagent-turn-budget-floors §4.7).
	BudgetIterationsSource string
	BudgetTurnsSource      string
	MaxRetries             int
	RetryBackoffSecs       int
	// Temperature and TopP are the dispatch's sampling overrides (parity
	// W1.4 — from the role file): passed through to the request
	// verbatim. nil leaves the provider's default; an explicit 0 is sent.
	Temperature        *float64
	TopP               *float64
	MaxToolConcurrency int
	EvidenceGuardNudge int
	EvidenceGuardHalt  int
	PipelineDisabled   []string
	JSONMode           bool
	// SeedMessages, when non-empty, pre-populates the loop's conversation so a
	// continuation resumes from the prior run's context. Run still appends its
	// prompt as the next user message.
	SeedMessages       []types.Message
	ToolResultMaxLines int
	ToolResultMaxBytes int

	// ToolSpillDir is the directory where oversized tool results are
	// spilled to disk (mirrors LoopConfig.ToolSpillDir). Empty disables
	// spilling; the truncation hint then carries no file path.
	ToolSpillDir string

	PruneProtectTokens int
	PruneMinReclaim    int
	PruneMinTurns      int
	PermissionRules    []pipeline.PermissionRule
	ContextWindow      int
	OtelEnabled        bool
	OtelVerbose        bool
	WrapUpThreshold    int
	// TurnCheckpointer, when non-nil and TurnCheckpointEnabled is set,
	// records a single-use git checkpoint before each model turn; on a
	// failed turn (hard tool-phase error, iteration exhaustion) the loop
	// rewinds workspace and conversation to the last checkpoint and
	// retries with guidance. TurnCheckpointMax caps the number of live
	// turn checkpoints (0 = unlimited). MaxTurnRestores bounds restores
	// per run (0 = default 3). See docs/sub-agents.md.
	TurnCheckpointer      TurnCheckpointer
	TurnCheckpointEnabled bool
	TurnCheckpointMax     int
	MaxTurnRestores       int

	// SessionID is the trace owner ID for this sub-agent's Shepherd
	// trace records (e.g. "sub-worker-sess-...-123"). The sub-agent
	// pipeline's shepherd_trace middleware records under this owner via
	// the session-shared store, so the parent can inspect the sub-agent's
	// execution history on failure. When empty, or when no shared store
	// is configured, tracing is a no-op.
	SessionID string

	// SteerCh, when non-nil, is the supervision inject channel
	// (shepherd-adoption plan A2): the supervisor tool's inject action
	// delivers guidance here, and the loop's steer middleware turns it
	// into a high-priority user message between turns. Nil disables
	// steering for this run — the run is unreachable by inject.
	//
	// Delivery is between-turn and best-effort by construction: guidance
	// that arrives after the final turn's drain is never consumed (a
	// response without tool calls ends the run). The runner unregisters
	// the run the moment the loop returns, so a finished run answers
	// inject with "no active run" instead of claiming delivery — the
	// loss window is only the final turn itself.
	SteerCh chan string

	// DestructiveGuard, when non-nil, is the supervision service's
	// blocking pre-dispatch check (shepherd-adoption plan B2): the
	// loop's pipeline consults it before a tool call executes, and a
	// non-empty reason denies the call with a synthesized result —
	// sub-agent loops run at ApprovalMode "allow", so this is the only
	// net their calls pass through. Nil when tracing is off or the
	// guard is disabled in config; every call then dispatches.
	DestructiveGuard func(ownerID, toolName, argsJSON string) string

	// TraceStore is the session's Shepherd trace store (plan 07, H4),
	// injected by the wiring through the task runner: the sub-agent
	// pipeline's shepherd_trace middleware writes through it. Nil when
	// tracing is disabled.
	TraceStore TraceSink

	// TraceCWD is the working directory recorded in the session-context
	// fact. Empty falls back to the process cwd.
	TraceCWD string

	// RetainedContext binds the sub-agent's transcript to its dispatch
	// substrate, containment, and role tool belt. The runner builds it from
	// the resolved world and registry; nil leaves the kernel default.
	RetainedContext *shepherdtrace.RetainedContext
}

// NewSubAgentLoop creates a Loop optimized for sub-agent execution.
// Sub-agents are ephemeral workers with limited turn budgets — they
// skip persistence, compaction, sub-agent spawning, quality gates,
// approval dialogs, and hook emission.
func NewSubAgentLoop(provider Provider, registry *tools.Registry, model, systemPrompt string, cfg SubAgentConfig) *Loop {
	if cfg.MaxLoopCycles <= 0 {
		cfg.MaxLoopCycles = 50
	}
	if cfg.MaxToolTurns <= 0 {
		cfg.MaxToolTurns = cfg.MaxLoopCycles
	}
	if cfg.RetryBackoffSecs <= 0 {
		cfg.RetryBackoffSecs = 5
	}
	if cfg.MaxToolConcurrency <= 0 {
		cfg.MaxToolConcurrency = 5
	}
	if cfg.WrapUpThreshold <= 0 {
		cfg.WrapUpThreshold = 5
	}

	l := &Loop{
		Provider: provider,
		Registry: registry,
		View:     NoopView{},
		Config: LoopConfig{
			Model:                  model,
			Temperature:            cfg.Temperature,
			TopP:                   cfg.TopP,
			ProviderName:           cfg.ProviderName,
			SystemPrompt:           systemPrompt,
			MaxLoopCycles:          cfg.MaxLoopCycles,
			MaxToolTurns:           cfg.MaxToolTurns,
			BudgetIterationsSource: cfg.BudgetIterationsSource,
			BudgetTurnsSource:      cfg.BudgetTurnsSource,
			MaxRetries:             cfg.MaxRetries,
			RetryBackoff:           time.Duration(cfg.RetryBackoffSecs) * time.Second,
			MaxToolConcurrency:     cfg.MaxToolConcurrency,
			EvidenceGuardNudge:     cfg.EvidenceGuardNudge,
			EvidenceGuardHalt:      cfg.EvidenceGuardHalt,
			JSONMode:               cfg.JSONMode,
			ToolSpillDir:           cfg.ToolSpillDir,
			PermissionRules:        cfg.PermissionRules,
			ContextWindow:          cfg.ContextWindow,
			OtelEnabled:            cfg.OtelEnabled,
			OtelVerbose:            cfg.OtelVerbose,
			ApprovalMode:           "allow",
			ToolsLevel:             FullTools,
			PipelineNames:          nil,
			PipelineDisabled:       cfg.PipelineDisabled,
			WrapUpThreshold:        cfg.WrapUpThreshold,
			SessionID:              cfg.SessionID,
			TraceCWD:               cfg.TraceCWD,
			RetainedContext:        cfg.RetainedContext,
			IsSubAgent:             true,
			DestructiveGuard:       cfg.DestructiveGuard,
			TraceStore:             cfg.TraceStore,

			TurnCheckpointer:      cfg.TurnCheckpointer,
			TurnCheckpointEnabled: cfg.TurnCheckpointEnabled,
			TurnCheckpointMax:     cfg.TurnCheckpointMax,
			MaxTurnRestores:       cfg.MaxTurnRestores,
		},
	}

	if len(cfg.SeedMessages) > 0 {
		// Seed the continuation's conversation. Run appends its prompt to a
		// non-nil State.Messages instead of building a fresh system+user pair,
		// so the prior turns survive.
		//
		// Drop the seeded system message and prepend the CURRENT system prompt:
		// a continued/forked run gets a new workspace root, and the prior
		// system message's environment header advertises the old one. Re-sending
		// it tells the model the wrong tree (the sub-agent then writes to a
		// prior-run absolute path and is refused by containment).
		seed := cfg.SeedMessages
		// Only the leading system message is the identity/environment prompt.
		// Later system messages are compaction summaries or steering, carry no
		// stale workspace root, and must be kept.
		hasSystem := len(seed) > 0 && seed[0].Role == "system"
		// Only replace the system message when there is a current prompt to put
		// in its place; an empty prompt must not blank the role/environment
		// context the seed already carries.
		replaceSystem := hasSystem && strings.TrimSpace(systemPrompt) != ""
		if replaceSystem {
			seed = seed[1:]
		}
		seeded := make([]types.Message, 0, len(seed)+1)
		if strings.TrimSpace(systemPrompt) != "" {
			seeded = append(seeded, types.SystemMsg(systemPrompt))
		}
		seeded = append(seeded, seed...)
		l.State.Messages = seeded
	}

	l.CtxMgr = NewContextManager(provider, model)
	l.CtxMgr.ToolResultMaxLines = cfg.ToolResultMaxLines
	l.CtxMgr.ToolResultMaxBytes = cfg.ToolResultMaxBytes
	l.CtxMgr.PruneProtectTokens = cfg.PruneProtectTokens
	l.CtxMgr.PruneMinReclaim = cfg.PruneMinReclaim
	l.CtxMgr.PruneMinTurns = cfg.PruneMinTurns
	l.CtxMgr.ContextWindow = cfg.ContextWindow
	l.CtxMgr.OtelEnabled = cfg.OtelEnabled
	l.CtxMgr.EnsurePruner()

	// Supervision steering (adoption plan A2): the channel the
	// supervisor injects into; the loop's steer middleware drains it.
	l.Steer = cfg.SteerCh

	if l.Config.MaxToolConcurrency > 0 {
		l.toolConcurrency = pipeline.NewToolConcurrencyMiddleware(l.Config.MaxToolConcurrency)
	}

	return l
}
