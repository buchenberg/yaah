package agent

import (
	"time"

	"github.com/buchenberg/yaah/internal/observability"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// LoopBuilder captures the session-level state needed to construct an
// agent.Loop, eliminating the option-list duplication between the
// interactive (runPrompt) and headless (runHeadless) call sites.
//
// Callers set the shared fields, then call Build with any caller-specific
// overrides (view, approval mode, otel forcing). The returned Loop is
// ready for Run(ctx, prompt).
type LoopBuilder struct {
	// Provider is the primary LLM provider.
	Provider Provider
	// Registry is the tool registry.
	Registry *tools.Registry
	// Model is the model name for the primary provider.
	Model string
	// SystemPrompt is the assembled system prompt.
	SystemPrompt string
	// Messages is the initial conversation history (for resume).
	Messages []types.Message
	// SessionID is the session identifier.
	SessionID string
	// Persister writes messages to the database (nil = in-memory).
	Persister *SessionPersister
	// Hooks emits structured JSONL hook events.
	Hooks *HookEmitter
	// Steer is the mid-turn steering channel (nil = none).
	Steer <-chan string
	// FollowUps is the between-turn follow-up channel (nil = none).
	FollowUps <-chan string
	// BackgroundJobs manages background sub-agent dispatch.
	BackgroundJobs *tools.BackgroundJobs
	// ConflictTracker detects parallel sub-agent file conflicts.
	ConflictTracker *tools.ConflictTracker
	// Cfg holds the tuning parameters typically derived from config.yaml.
	Cfg AgentConfig
	// FallbackProvider and FallbackModel are used on primary failure.
	FallbackProvider Provider
	FallbackModel    string
	// CompactProvider and CompactModel are used for context compaction.
	CompactProvider Provider
	CompactModel    string
	// PipelineEnabled / PipelineDisabled configure the middleware chain.
	PipelineEnabled  []string
	PipelineDisabled []string
	// SubAgentMaxConcurrency caps simultaneous sub-agent dispatches.
	SubAgentMaxConcurrency int
	// SubAgentStuckChildTimeout is the per-role stuck-child deadline.
	SubAgentStuckChildTimeout time.Duration
	// SubAgentStuckChildTimeouts overrides the timeout per role.
	SubAgentStuckChildTimeouts map[string]time.Duration
}

// LoopBuildOptions holds caller-specific overrides applied on top of the
// LoopBuilder's shared state. Zero values are ignored.
type LoopBuildOptions struct {
	// View overrides the default NoopView. Use nil for NoopView.
	View View
	// ApprovalMode, when non-empty, is applied via WithApprovalMode.
	// When empty, WithApprovalMode is omitted and the loop keeps its
	// default (unset) approval mode.
	ApprovalMode string
	// OtelEnabled forces a specific OTel state. When nil, OTel defaults
	// to false (disabled).
	OtelEnabled *bool
	OtelVerbose bool
}

// Build constructs an agent.Loop from the builder's shared state plus
// any caller-specific overrides. The returned loop is ready for Run.
func (b *LoopBuilder) Build(opts LoopBuildOptions) *Loop {
	view := opts.View
	if view == nil {
		view = NoopView{}
	}

	otelEnabled := false
	if opts.OtelEnabled != nil {
		otelEnabled = *opts.OtelEnabled
	}

	var optsList []Option
	optsList = append(optsList,
		WithModel(b.Model),
		WithSystemPrompt(b.SystemPrompt),
		WithView(view),
		WithMessages(b.Messages),
		WithSessionID(b.SessionID),
		WithPersister(b.Persister),
		WithHooks(b.Hooks),
		WithFallback(b.FallbackProvider, b.FallbackModel),
		WithCompactProvider(b.CompactProvider, b.CompactModel),
		WithPipeline(b.PipelineEnabled, b.PipelineDisabled),
		WithSteer(b.Steer),
		WithFollowUps(b.FollowUps),
		WithBackgroundJobs(b.BackgroundJobs),
		WithConflictTracker(b.ConflictTracker),
		WithToolsLevel(FullTools),
		WithOtel(otelEnabled, opts.OtelVerbose),
		WithSubAgentConcurrency(
			b.SubAgentMaxConcurrency,
			b.SubAgentStuckChildTimeout,
			b.SubAgentStuckChildTimeouts,
		),
		WithAgentConfig(b.Cfg),
	)

	if opts.ApprovalMode != "" {
		optsList = append(optsList, WithApprovalMode(opts.ApprovalMode))
	}

	return NewLoop(b.Provider, b.Registry, optsList...)
}

// ResolveCompactProvider wraps a compact provider with OTel instrumentation
// if it supports streaming. Returns the provider unchanged if it does not
// implement StreamProvider or is nil.
func ResolveCompactProvider(p Provider, verbose bool) Provider {
	if p == nil {
		return nil
	}
	if sp, ok := p.(StreamProvider); ok {
		return &observability.InstrumentedProvider{Inner: sp, Verbose: verbose}
	}
	return p
}
