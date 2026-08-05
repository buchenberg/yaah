package agent

import (
	"time"

	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// Option configures a Loop via the functional options pattern.
type Option func(*Loop)

// NewLoop creates a Loop with the required provider and registry,
// applying any optional configuration. This replaces the 30+ field
// struct literal previously duplicated across cmd/yaah/wiring.go,
// serve.go, tui.go, and subagent_runner.go.
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
	return func(l *Loop) { l.Config.Model = model }
}

// WithSystemPrompt sets the system prompt.
func WithSystemPrompt(prompt string) Option {
	return func(l *Loop) { l.Config.SystemPrompt = prompt }
}

// WithView sets the event view for TUI/REPL rendering.
func WithView(v View) Option {
	return func(l *Loop) { l.View = v }
}

// WithMessages sets the initial conversation history (for session resume).
func WithMessages(msgs []types.Message) Option {
	return func(l *Loop) { l.State.Messages = msgs }
}

// WithSessionID sets the session identifier.
func WithSessionID(id string) Option {
	return func(l *Loop) { l.Config.SessionID = id }
}

// WithPersister sets the session persister.
func WithPersister(p *SessionPersister) Option {
	return func(l *Loop) { l.Persister = p }
}

// WithHooks sets the hook emitter.
func WithHooks(h *HookEmitter) Option {
	return func(l *Loop) { l.Hooks = h }
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
		l.Config.CompactModel = model
	}
}

// WithApprovalMode sets the tool approval mode.
func WithApprovalMode(mode string) Option {
	return func(l *Loop) { l.Config.ApprovalMode = mode }
}

// WithPipeline configures the middleware pipeline.
func WithPipeline(enabled, disabled []string) Option {
	return func(l *Loop) {
		l.Config.PipelineNames = enabled
		l.Config.PipelineDisabled = disabled
	}
}

// WithPermissionRules sets the path-based permission rules for the
// PermissionMiddleware. When nil, no path-filtering is applied.
func WithPermissionRules(rules []pipeline.PermissionRule) Option {
	return func(l *Loop) { l.Config.PermissionRules = rules }
}

// WithSteer sets the mid-turn steering channel.
func WithSteer(ch <-chan string) Option {
	return func(l *Loop) { l.Steer = ch }
}

// WithFollowUps sets the follow-up message channel.
func WithFollowUps(ch <-chan string) Option {
	return func(l *Loop) { l.FollowUps = ch }
}

// WithBackgroundJobs attaches the session's background sub-agent manager
// so the loop can register its broker event hooks for background jobs at
// Run start.
func WithBackgroundJobs(jobs *tools.BackgroundJobs) Option {
	return func(l *Loop) { l.BackgroundJobs = jobs }
}

// WithConflictTracker sets the file conflict tracker.
func WithConflictTracker(t *tools.ConflictTracker) Option {
	return func(l *Loop) { l.ConflictTracker = t }
}

// WithToolsLevel sets the tool visibility level.
func WithToolsLevel(level ToolsLevel) Option {
	return func(l *Loop) { l.Config.ToolsLevel = level }
}

// WithOtel enables OpenTelemetry tracing.
func WithOtel(enabled, verbose bool) Option {
	return func(l *Loop) {
		l.Config.OtelEnabled = enabled
		l.Config.OtelVerbose = verbose
	}
}

// WithSubAgentConcurrency sets the sub-agent concurrency cap and timeouts.
func WithSubAgentConcurrency(max int, stuckTimeout time.Duration, stuckTimeouts map[string]time.Duration) Option {
	return func(l *Loop) {
		l.Config.MaxSubAgentConcurrency = max
		l.Config.StuckChildTimeout = stuckTimeout
		l.Config.StuckChildTimeouts = stuckTimeouts
	}
}

// AgentConfig holds the full set of tuning parameters typically derived
// from config.yaml. Use WithAgentConfig to apply them all at once.
type AgentConfig struct {
	MaxLoopCycles          int
	MaxToolTurns           int
	MaxRetries             int
	RetryBackoffSecs       int
	ContextWindow          int
	CompactionThreshold    float64
	RawCompactionThreshold float64
	CompactMaxMessages     int
	EstimateFactor         float64
	QualityGates           map[string][]string
	LoopDetectCount        int
	LoopDetectWindow       int
	MaxToolConcurrency     int
	WrapUpThreshold        int
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

// WithAgentConfig applies all tuning parameters from an AgentConfig.
// Zero values are left unset (applyDefaults fills them later).
func WithAgentConfig(cfg AgentConfig) Option {
	return func(l *Loop) {
		l.Config.MaxLoopCycles = cfg.MaxLoopCycles
		l.Config.MaxToolTurns = cfg.MaxToolTurns
		l.Config.MaxRetries = cfg.MaxRetries
		if cfg.RetryBackoffSecs > 0 {
			l.Config.RetryBackoff = time.Duration(cfg.RetryBackoffSecs) * time.Second
		}
		l.Config.ContextWindow = cfg.ContextWindow
		l.Config.CompactionThreshold = cfg.CompactionThreshold
		l.Config.RawCompactionThreshold = cfg.RawCompactionThreshold
		l.Config.CompactMaxMessages = cfg.CompactMaxMessages
		l.Config.EstimateFactor = cfg.EstimateFactor
		l.Config.QualityGates = cfg.QualityGates
		l.Config.LoopDetectCount = cfg.LoopDetectCount
		l.Config.LoopDetectWindow = cfg.LoopDetectWindow
		l.Config.MaxToolConcurrency = cfg.MaxToolConcurrency
		l.Config.WrapUpThreshold = cfg.WrapUpThreshold
		l.Config.MaxInlineToolsPerTurn = cfg.MaxInlineToolsPerTurn
		l.Config.PromptCaching = cfg.PromptCaching
		l.Config.JSONMode = cfg.JSONMode
		if l.CtxMgr == nil {
			l.CtxMgr = &ContextManager{}
		}
		l.CtxMgr.ReasoningProtectTurns = cfg.ReasoningProtectTurns
		l.CtxMgr.ToolResultMaxLines = cfg.ToolResultMaxLines
		l.CtxMgr.ToolResultMaxBytes = cfg.ToolResultMaxBytes
		l.CtxMgr.PruneProtectTokens = cfg.PruneProtectTokens
		l.CtxMgr.PruneMinReclaim = cfg.PruneMinReclaim
		l.CtxMgr.PruneMinTurns = cfg.PruneMinTurns
	}
}
