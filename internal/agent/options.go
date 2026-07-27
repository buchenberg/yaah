package agent

import (
	"time"

	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// Option configures a Loop via the functional options pattern.
type Option func(*Loop)

// NewLoop creates a Loop with the required provider and registry,
// applying any optional configuration. This replaces the 30+ field
// struct literal previously duplicated across agent_frame.go, serve.go,
// tui.go, and subagent_runner.go.
func NewLoop(provider Provider, registry *tools.Registry, opts ...Option) *Loop {
	l := &Loop{
		Provider: provider,
		Registry: registry,
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// WithModel sets the model name.
func WithModel(model string) Option {
	return func(l *Loop) { l.Model = model }
}

// WithSystemPrompt sets the system prompt.
func WithSystemPrompt(prompt string) Option {
	return func(l *Loop) { l.SystemPrompt = prompt }
}

// WithView sets the event view for TUI/REPL rendering.
func WithView(v View) Option {
	return func(l *Loop) { l.View = v }
}

// WithMessages sets the initial conversation history (for session resume).
func WithMessages(msgs []types.Message) Option {
	return func(l *Loop) { l.Messages = msgs }
}

// WithDB sets the SQLite database for persistence.
func WithDB(db *memory.DB) Option {
	return func(l *Loop) { l.DB = db }
}

// WithWriteDebouncer sets the debounced writer for persistence.
func WithWriteDebouncer(w *memory.DebouncedWriter) Option {
	return func(l *Loop) { l.WriteDebouncer = w }
}

// WithSessionID sets the session identifier.
func WithSessionID(id string) Option {
	return func(l *Loop) { l.SessionID = id }
}

// WithMsgIdx sets the starting message index (for session resume).
func WithMsgIdx(idx int) Option {
	return func(l *Loop) { l.MsgIdx = idx }
}

// WithHookDir sets the JSONL hook event directory.
func WithHookDir(dir string) Option {
	return func(l *Loop) { l.HookDir = dir }
}

// WithFallback sets the fallback provider and model.
func WithFallback(provider Provider, model string) Option {
	return func(l *Loop) {
		l.FallbackProvider = provider
		l.FallbackModel = model
	}
}

// WithCompactProvider sets the compaction provider and model.
func WithCompactProvider(provider Provider, model string) Option {
	return func(l *Loop) {
		l.CompactProvider = provider
		l.CompactModel = model
	}
}

// WithApprovalMode sets the tool approval mode.
func WithApprovalMode(mode string) Option {
	return func(l *Loop) { l.ApprovalMode = mode }
}

// WithPipeline configures the middleware pipeline.
func WithPipeline(enabled, disabled []string) Option {
	return func(l *Loop) {
		l.PipelineNames = enabled
		l.PipelineDisabled = disabled
	}
}

// WithSteer sets the mid-turn steering channel.
func WithSteer(ch <-chan string) Option {
	return func(l *Loop) { l.Steer = ch }
}

// WithFollowUps sets the follow-up message channel.
func WithFollowUps(ch <-chan string) Option {
	return func(l *Loop) { l.FollowUps = ch }
}

// WithConflictTracker sets the file conflict tracker.
func WithConflictTracker(t *tools.ConflictTracker) Option {
	return func(l *Loop) { l.ConflictTracker = t }
}

// WithToolsLevel sets the tool visibility level.
func WithToolsLevel(level ToolsLevel) Option {
	return func(l *Loop) { l.ToolsLevel = level }
}

// WithOtel enables OpenTelemetry tracing.
func WithOtel(enabled, verbose bool) Option {
	return func(l *Loop) {
		l.OtelEnabled = enabled
		l.OtelVerbose = verbose
	}
}

// WithSubAgentConcurrency sets the sub-agent concurrency cap and timeouts.
func WithSubAgentConcurrency(max int, stuckTimeout time.Duration, stuckTimeouts map[string]time.Duration) Option {
	return func(l *Loop) {
		l.MaxSubAgentConcurrency = max
		l.StuckChildTimeout = stuckTimeout
		l.StuckChildTimeouts = stuckTimeouts
	}
}

// LoopConfig holds the full set of tuning parameters typically derived
// from config.yaml. Use WithLoopConfig to apply them all at once.
type LoopConfig struct {
	MaxIterations          int
	MaxTurns               int
	MaxRetries             int
	RetryBackoffSecs       int
	ContextWindow          int
	CompactionThreshold    float64
	RawCompactionThreshold float64
	EstimateFactor         float64
	LoopDetectCount        int
	LoopDetectWindow       int
	MaxToolConcurrency     int
	MaxInlineToolsPerTurn  int
	PromptCaching          bool
	ReasoningProtectTurns  int
	ToolResultMaxLines     int
	ToolResultMaxBytes     int
	PruneProtectTokens     int
	PruneMinReclaim        int
	PruneMinTurns          int
	JSONMode               bool
}

// WithLoopConfig applies all tuning parameters from a LoopConfig.
// Zero values are left unset (applyDefaults fills them later).
func WithLoopConfig(cfg LoopConfig) Option {
	return func(l *Loop) {
		l.MaxIterations = cfg.MaxIterations
		l.MaxTurns = cfg.MaxTurns
		l.MaxRetries = cfg.MaxRetries
		if cfg.RetryBackoffSecs > 0 {
			l.RetryBackoff = time.Duration(cfg.RetryBackoffSecs) * time.Second
		}
		l.ContextWindow = cfg.ContextWindow
		l.CompactionThreshold = cfg.CompactionThreshold
		l.RawCompactionThreshold = cfg.RawCompactionThreshold
		l.EstimateFactor = cfg.EstimateFactor
		l.LoopDetectCount = cfg.LoopDetectCount
		l.LoopDetectWindow = cfg.LoopDetectWindow
		l.MaxToolConcurrency = cfg.MaxToolConcurrency
		l.MaxInlineToolsPerTurn = cfg.MaxInlineToolsPerTurn
		l.PromptCaching = cfg.PromptCaching
		l.ReasoningProtectTurns = cfg.ReasoningProtectTurns
		l.ToolResultMaxLines = cfg.ToolResultMaxLines
		l.ToolResultMaxBytes = cfg.ToolResultMaxBytes
		l.PruneProtectTokens = cfg.PruneProtectTokens
		l.PruneMinReclaim = cfg.PruneMinReclaim
		l.PruneMinTurns = cfg.PruneMinTurns
		l.JSONMode = cfg.JSONMode
	}
}
