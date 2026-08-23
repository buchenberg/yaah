package agent

import (
	"time"

	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// SubAgentConfig holds the tuning parameters for a sub-agent loop.
// Unlike the main agent loop, sub-agents skip persistence, compaction,
// sub-agent spawning, quality gates, and hooks — they are ephemeral
// workers with a fixed turn budget.
type SubAgentConfig struct {
	MaxLoopCycles      int
	MaxToolTurns       int
	MaxRetries         int
	RetryBackoffSecs   int
	MaxToolConcurrency int
	JSONMode           bool
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
	// per run (0 = default 3). See
	// .agents/plans/per-turn-checkpoint-restore.
	TurnCheckpointer      TurnCheckpointer
	TurnCheckpointEnabled bool
	TurnCheckpointMax     int
	MaxTurnRestores       int

	// InitialMessages, when non-empty, seeds the loop's conversation so
	// this dispatch continues from prior history. See LoopConfig.
	// InitialMessages.
	InitialMessages []types.Message

	// SessionID is the trace owner ID for this sub-agent's Shepherd
	// trace records (e.g. "sub-worker-sess-...-123"). The sub-agent
	// pipeline's shepherd_trace middleware records under this owner via
	// the session-shared store, so the parent can inspect the sub-agent's
	// execution history on failure. When empty, or when no shared store
	// is configured, tracing is a no-op.
	SessionID string
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
			Model:              model,
			SystemPrompt:       systemPrompt,
			MaxLoopCycles:      cfg.MaxLoopCycles,
			MaxToolTurns:       cfg.MaxToolTurns,
			MaxRetries:         cfg.MaxRetries,
			RetryBackoff:       time.Duration(cfg.RetryBackoffSecs) * time.Second,
			MaxToolConcurrency: cfg.MaxToolConcurrency,
			JSONMode:           cfg.JSONMode,
			ToolSpillDir:       cfg.ToolSpillDir,
			PermissionRules:    cfg.PermissionRules,
			ContextWindow:      cfg.ContextWindow,
			OtelEnabled:        cfg.OtelEnabled,
			OtelVerbose:        cfg.OtelVerbose,
			ApprovalMode:       "allow",
			ToolsLevel:         FullTools,
			PipelineNames:      nil,
			PipelineDisabled:   nil,
			WrapUpThreshold:    cfg.WrapUpThreshold,
			SessionID:          cfg.SessionID,
			IsSubAgent:         true,

			TurnCheckpointer:      cfg.TurnCheckpointer,
			TurnCheckpointEnabled: cfg.TurnCheckpointEnabled,
			TurnCheckpointMax:     cfg.TurnCheckpointMax,
			MaxTurnRestores:       cfg.MaxTurnRestores,
			InitialMessages:       cfg.InitialMessages,
		},
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

	if l.Config.MaxToolConcurrency > 0 {
		l.toolConcurrency = pipeline.NewToolConcurrencyMiddleware(l.Config.MaxToolConcurrency)
	}

	return l
}
