package agent

import (
	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/types"
)

// ContextManager owns context-window policy configuration and state:
// compaction thresholds, pruning tuning, token tracking, and truncation
// limits. Extracted from the Loop to isolate context management concerns
// and make them independently configurable and testable.
//
// Phase 1: holds config and state fields. Methods still live on Loop
// (agent_context.go, agent_truncation.go, agent_reasoning.go) and read
// from this struct. Phase 2 will migrate the methods here.
type ContextManager struct {
	// Context window and compaction thresholds.
	ContextWindow          int
	CompactionThreshold    float64
	RawCompactionThreshold float64
	EstimateFactor         float64

	// Reasoning and truncation tuning.
	ReasoningProtectTurns int
	ToolResultMaxLines    int
	ToolResultMaxBytes    int

	// Pruner and its tuning knobs.
	Pruner             *pipeline.Pruner
	PruneProtectTokens int
	PruneMinReclaim    int
	PruneMinTurns      int

	// Compaction state.
	PreviousSummary        string
	LastPromptTokens       int
	LastCachedPromptTokens int
	LastCompactionTokens   int
	IneffectiveCompactions int

	// Injected dependencies for compaction LLM calls.
	Provider        Provider
	Model           string
	CompactProvider Provider
	CompactModel    string
	DB              *memory.DB
	SessionID       string
	OtelEnabled     bool
}

// NewContextManager creates a ContextManager with the given dependencies.
// Tuning fields are zero-valued; callers set them from config.
func NewContextManager(provider Provider, model string) *ContextManager {
	return &ContextManager{
		Provider: provider,
		Model:    model,
	}
}

// EnsurePruner constructs a default Pruner when none is attached,
// applying any tuning overrides from the config fields.
func (cm *ContextManager) EnsurePruner() {
	if cm.Pruner == nil {
		cfg := pipeline.DefaultPruneConfig()
		if cm.PruneProtectTokens > 0 {
			cfg.ProtectTokens = cm.PruneProtectTokens
		}
		if cm.PruneMinReclaim > 0 {
			cfg.MinReclaim = cm.PruneMinReclaim
		}
		if cm.PruneMinTurns > 0 {
			cfg.MinTurns = cm.PruneMinTurns
		}
		cm.Pruner = pipeline.NewPruner(cfg)
	}
}

// ResetPruner clears the soft-prune set.
func (cm *ContextManager) ResetPruner() {
	if cm.Pruner != nil {
		cm.Pruner.Reset()
	}
}

// PruneFilter returns a copy of messages with pruned tool-result content
// replaced by compact stubs.
func (cm *ContextManager) PruneFilter(messages []types.Message) []types.Message {
	if cm.Pruner == nil {
		return messages
	}
	return cm.Pruner.Filter(messages)
}
