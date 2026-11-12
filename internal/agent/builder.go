package agent

import (
	"context"
	"time"

	"github.com/buchenberg/yaah/internal/jobs"
	"github.com/buchenberg/yaah/internal/observability"
	"github.com/buchenberg/yaah/internal/permissions"
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
	// ToolsLevel narrows the advertised tool set for this run. Zero means
	// FullTools.
	ToolsLevel ToolsLevel
	// PersonaTools narrows the advertised tool set to the session
	// persona's tools list (yaah-21uk.12); empty means no narrowing.
	PersonaTools []string
	// Permissions is the merged permission ruleset (parity W5): global
	// config rules first, persona rules appended after (persona wins on
	// overlap). Empty means no rules.
	Permissions permissions.Rules
	// Model is the model name for the primary provider.
	Model string
	// SystemPrompt is the assembled system prompt.
	SystemPrompt string
	// Messages is the initial conversation history (for resume).
	Messages []types.Message
	// SessionID is the session identifier.
	SessionID string
	// TraceStore is the session-shared Shepherd trace sink. When set, the
	// orchestrator pipeline records turn, session-context, assistant, and
	// tool-output facts through it (yaah-o86). The seam, not the kernel's
	// concrete store — see pipeline.TraceSink.
	TraceStore TraceSink
	// Persister writes messages to the database (nil = in-memory).
	Persister *SessionPersister
	// Hooks emits structured JSONL hook events.
	Hooks *HookEmitter
	// Steer is the mid-turn steering channel (nil = none).
	Steer <-chan string
	// FollowUps is the between-turn follow-up channel (nil = none).
	FollowUps <-chan string
	// OnUsage fires after every LLM call with the loop's running usage
	// totals (incremental persistence); OnSubUsage fires when a
	// blocking sub-agent dispatch's usage folds in (attribution).
	OnUsage    func(types.Usage)
	OnSubUsage func(types.Usage)
	// OnTitle fires when the session title transitions from empty
	// (unknown title → first derived one). The session-state seam for
	// caching; the loop itself forwards the event to its broker.
	OnTitle func(title string, updatedAt int64)
	// ConsolidateFn is the consolidate nudge's dispatcher
	// (docs/compaction-as-sleep.md §8 P3): the cadence-driven middleware
	// enqueues, this runs the consolidation out-of-band. Nil disables the
	// nudge.
	ConsolidateFn func(ctx context.Context, kind string)
	// BackgroundJobs manages background sub-agent dispatch.
	BackgroundJobs *jobs.BackgroundJobs
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
	// MCPApproval gates MCP-served tools ("ask", "allow", "deny");
	// MCPToolNames carries the names they registered under.
	MCPApproval  string
	MCPToolNames map[string]bool
	// SavedAllow is the persisted-"always" consultant (yaah-x6vi.6),
	// handed to the evaluator via WithSavedAllow. Nil means no store.
	SavedAllow func(name, resource string) bool
	// SubAgentMaxConcurrency caps simultaneous sub-agent dispatches.
	SubAgentMaxConcurrency int
	// SubAgentStuckChildTimeout is the per-role stuck-child deadline.
	SubAgentStuckChildTimeout time.Duration
	// SubAgentStuckChildTimeouts overrides the timeout per role.
	SubAgentStuckChildTimeouts map[string]time.Duration
	// Temperature and TopP are the sampling overrides the persona file
	// declares (parity W1.4); passed through to every request via
	// WithSampling. nil means the file did not set them.
	Temperature *float64
	TopP        *float64
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
		WithTraceSink(b.TraceStore),
		WithPersister(b.Persister),
		WithHooks(b.Hooks),
		WithFallback(b.FallbackProvider, b.FallbackModel),
		WithCompactProvider(b.CompactProvider, b.CompactModel),
		WithPipeline(b.PipelineEnabled, b.PipelineDisabled),
		WithSteer(b.Steer),
		WithFollowUps(b.FollowUps),
		WithUsageHooks(b.OnUsage, b.OnSubUsage),
		WithTitleHook(b.OnTitle),
		WithBackgroundJobs(b.BackgroundJobs),
		WithConflictTracker(b.ConflictTracker),
		WithToolsLevel(b.ToolsLevel),
		WithPersonaTools(b.PersonaTools),
		WithPermissionRulesets(b.Permissions),
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
	if b.Temperature != nil || b.TopP != nil {
		optsList = append(optsList, WithSampling(b.Temperature, b.TopP))
	}
	if b.MCPApproval != "" {
		optsList = append(optsList, WithMCPApproval(b.MCPApproval, b.MCPToolNames))
	}
	if b.SavedAllow != nil {
		optsList = append(optsList, WithSavedAllow(b.SavedAllow))
	}

	loop := NewLoop(b.Provider, b.Registry, optsList...)
	// Assigned after construction because buildPipeline reads it lazily at
	// Run: the consolidate nudge's dispatcher is wired by the composition
	// root, which is outside the option list.
	loop.ConsolidateFn = b.ConsolidateFn
	return loop
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
