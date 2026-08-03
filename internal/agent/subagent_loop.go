package agent

import (
	"time"

	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/tools"
)

// SubAgentConfig holds the tuning parameters for a sub-agent loop.
// Unlike the main agent loop, sub-agents skip persistence, compaction,
// sub-agent spawning, quality gates, and hooks — they are ephemeral
// workers with a fixed turn budget.
type SubAgentConfig struct {
	MaxIterations      int
	MaxTurns           int
	MaxRetries         int
	RetryBackoffSecs   int
	MaxToolConcurrency int
	JSONMode           bool
	ToolResultMaxLines int
	ToolResultMaxBytes int
	PruneProtectTokens int
	PruneMinReclaim    int
	PruneMinTurns      int
	PermissionRules    []pipeline.PermissionRule
	ContextWindow      int
	OtelEnabled        bool
	OtelVerbose        bool
}

// NewSubAgentLoop creates a Loop optimized for sub-agent execution.
// Sub-agents are ephemeral workers with limited turn budgets — they
// skip persistence, compaction, sub-agent spawning, quality gates,
// approval dialogs, and hook emission.
func NewSubAgentLoop(provider Provider, registry *tools.Registry, model, systemPrompt string, cfg SubAgentConfig) *Loop {
	if cfg.MaxIterations <= 0 {
		cfg.MaxIterations = 50
	}
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = cfg.MaxIterations
	}
	if cfg.RetryBackoffSecs <= 0 {
		cfg.RetryBackoffSecs = 5
	}
	if cfg.MaxToolConcurrency <= 0 {
		cfg.MaxToolConcurrency = 5
	}

	l := &Loop{
		Provider:     provider,
		Registry:     registry,
		Model:        model,
		SystemPrompt: systemPrompt,
		View:         NoopView{},

		MaxIterations:      cfg.MaxIterations,
		MaxTurns:           cfg.MaxTurns,
		MaxRetries:         cfg.MaxRetries,
		RetryBackoff:       time.Duration(cfg.RetryBackoffSecs) * time.Second,
		MaxToolConcurrency: cfg.MaxToolConcurrency,
		JSONMode:           cfg.JSONMode,

		ToolResultMaxLines: cfg.ToolResultMaxLines,
		ToolResultMaxBytes: cfg.ToolResultMaxBytes,
		PruneProtectTokens: cfg.PruneProtectTokens,
		PruneMinReclaim:    cfg.PruneMinReclaim,
		PruneMinTurns:      cfg.PruneMinTurns,
		PermissionRules:    cfg.PermissionRules,
		ContextWindow:      cfg.ContextWindow,
		OtelEnabled:        cfg.OtelEnabled,
		OtelVerbose:        cfg.OtelVerbose,

		ApprovalMode: "allow",
		ToolsLevel:   FullTools,

		// Sub-agents use an in-memory pruner only — no compaction pipeline.
		Middleware:       nil,
		PipelineNames:    nil,
		PipelineDisabled: nil,
	}

	if l.MaxToolConcurrency > 0 {
		l.toolConcurrency = pipeline.NewToolConcurrencyMiddleware(l.MaxToolConcurrency)
	}
	l.ensurePruner()

	return l
}
