package agent

import (
	"context"

	"github.com/buchenberg/yaah/internal/agent/llm"
	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/pubsub"
	"github.com/buchenberg/yaah/internal/types"
	"go.opentelemetry.io/otel/trace"
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
	PreviousSummary          string
	LastPromptTokens         int
	LastCachedPromptTokens   int
	LastCompactionTokens     int
	IneffectiveCompactions   int
	CompactionSavingsHistory []float64

	// Injected dependencies for compaction LLM calls.
	Provider        Provider
	Model           string
	CompactProvider Provider
	CompactModel    string
	DB              *memory.DB
	SessionID       string
	OtelEnabled     bool

	// --- Phase 2: compaction infrastructure ---

	// LLMClient allows ContextManager to call LLMs for summarisation.
	LLMClient *llm.Client

	// CompactMaxMessages is the max messages to include in a compaction
	// summary from the tail.
	CompactMaxMessages int

	// CompactionBudgetMultiplier grows when back-to-back overflows occur.
	CompactionBudgetMultiplier float64

	// CompactionForcedByOverflow is set when a forced-compaction due to
	// context overflow occurred this turn.
	CompactionForcedByOverflow bool

	// Tracer for OpenTelemetry spans during compaction.
	Tracer trace.Tracer

	// Broker for publishing compaction lifecycle events.
	Broker *pubsub.Broker[Event]

	// CompactionHook is an optional callback invoked during compaction.
	CompactionHook func(event any)

	// Messages is a mutable snapshot of the current message list, used by
	// compactFn to read/write the working set.
	Messages []types.Message

	// compactFn is set by the Loop to delegate compaction back through its
	// own method while ContextManager satisfies the Compactor interface.
	compactFn func(ctx context.Context, messages []types.Message, threshold float64) []types.Message
}

// Reset resets all compaction-tracking state to zero values.
func (cm *ContextManager) Reset() {
	cm.PreviousSummary = ""
	cm.LastPromptTokens = 0
	cm.LastCachedPromptTokens = 0
	cm.LastCompactionTokens = 0
	cm.IneffectiveCompactions = 0
	cm.CompactionSavingsHistory = nil
	cm.CompactionBudgetMultiplier = 1.0
	cm.CompactionForcedByOverflow = false
}

// Compact implements pipeline.Compactor by delegating to the registered
// compaction function.
func (cm *ContextManager) Compact(ctx context.Context, messages []types.Message, threshold float64) []types.Message {
	if cm.compactFn == nil {
		return messages
	}
	return cm.compactFn(ctx, messages, threshold)
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
