// context.go defines the context-key contract between the task tool,
// the sub-agent runner, and the agent loop: model writeback, start
// notification, usage accumulation, and stuck-child heartbeats. These
// keys are set by the spawner and read/written by the runner and loop.
package jobs

import (
	"context"
	"errors"

	"github.com/buchenberg/yaah/internal/types"
)

// contextKey is the package-local context key type for string keys.
type contextKey string

// subAgentModelWriterKey is a context key for the closure the runner
// calls to report the sub-agent's model name. Background jobs install a
// mutex-guarded writer; foreground callers may adapt a *string.
type subAgentModelWriterKey struct{}

// WithSubAgentModelWriter stores write in ctx so the sub-agent runner
// can report the model name through it. The writer must be safe for the
// runner's goroutine only — synchronization with concurrent readers is
// the installer's responsibility.
func WithSubAgentModelWriter(ctx context.Context, write func(string)) context.Context {
	return context.WithValue(ctx, subAgentModelWriterKey{}, write)
}

// WithSubAgentModelPtr stores ptr in ctx so the sub-agent runner can write
// the model name into it. Call after runner returns to read the value.
// Only safe when the caller reads *ptr after joining the runner (e.g. a
// foreground dispatch collected via a channel); use
// WithSubAgentModelWriter for shared targets.
func WithSubAgentModelPtr(ctx context.Context, ptr *string) context.Context {
	return WithSubAgentModelWriter(ctx, func(model string) { *ptr = model })
}

// WriteSubAgentModel reports model through the writer stored in ctx, if
// present.
func WriteSubAgentModel(ctx context.Context, model string) {
	if write, ok := ctx.Value(subAgentModelWriterKey{}).(func(string)); ok && write != nil {
		write(model)
	}
}

// WithSubAgentModelTap observes the sub-agent's resolved model WITHOUT
// displacing the writeback already installed on ctx. A dispatch body that needs
// the model for its result envelope must not simply install a pointer: the
// agent loop's start event reads the same writeback, and shadowing it would
// leave that event with no model. Wrapping the writer keeps both readers.
func WithSubAgentModelTap(ctx context.Context, tap func(string)) context.Context {
	prev, _ := ctx.Value(subAgentModelWriterKey{}).(func(string))
	return WithSubAgentModelWriter(ctx, func(model string) {
		if tap != nil {
			tap(model)
		}
		if prev != nil {
			prev(model)
		}
	})
}

// dispatchIDKey stores the per-dispatch identifier minted by the conflict
// tracker (label#n) so the result envelope names the same dispatch the conflict
// evidence does.
const dispatchIDKey contextKey = "yaah-dispatch-id"

// WithDispatchID records the per-dispatch identity on ctx. It lives in this
// package rather than with the conflict tracker because the background job
// manager must read it back when it builds a completion delivery, and jobs
// cannot import its callers.
func WithDispatchID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, dispatchIDKey, id)
}

// DispatchIDFrom returns the dispatch identity recorded by WithDispatchID, or
// "" when none was.
func DispatchIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(dispatchIDKey).(string); ok {
		return v
	}
	return ""
}

// subAgentStartKey is a context key for the start notifier the runner
// fires once the sub-agent's provider and model are resolved, just
// before its loop begins.
type subAgentStartKey struct{}

// SubAgentWorld is the resolved dispatch world carried from the dispatch
// body (the retained runtime or the shared lane) to the event publishers.
// The zero value means no world was resolved, which every surface renders as
// nothing. It is a plain struct so events can carry it without importing
// internal/agent/subagent.
type SubAgentWorld struct {
	// Resolved is the world kind: "worktree", "container", or "shared".
	Resolved string
	// Degraded reports that the request could not get its world.
	Degraded bool
	// Reason explains a degraded resolution.
	Reason string
}

// subAgentWorldWriterKey is a context key for the closure a dispatch body
// calls to report its resolved world.
type subAgentWorldWriterKey struct{}

// subAgentWorldValueKey stores the *SubAgentWorld the pointer writer targets,
// so a reader (the background manager seeding a job) can observe it without
// knowing the writer closure.
type subAgentWorldValueKey struct{}

// WithSubAgentWorldWriter stores write in ctx so a dispatch body can report
// its resolved world. Synchronization with concurrent readers is the
// installer's responsibility.
func WithSubAgentWorldWriter(ctx context.Context, write func(SubAgentWorld)) context.Context {
	return context.WithValue(ctx, subAgentWorldWriterKey{}, write)
}

// WithSubAgentWorldPtr stores ptr in ctx so a dispatch body can write the
// resolved world into it. Only safe when the caller reads *ptr after joining
// the body (e.g. a foreground dispatch collected via a channel); use
// WithSubAgentWorldWriter for shared targets.
func WithSubAgentWorldPtr(ctx context.Context, ptr *SubAgentWorld) context.Context {
	ctx = context.WithValue(ctx, subAgentWorldValueKey{}, ptr)
	return WithSubAgentWorldWriter(ctx, func(w SubAgentWorld) { *ptr = w })
}

// WriteSubAgentWorld reports w through the writer stored in ctx, if present.
func WriteSubAgentWorld(ctx context.Context, w SubAgentWorld) {
	if write, ok := ctx.Value(subAgentWorldWriterKey{}).(func(SubAgentWorld)); ok && write != nil {
		write(w)
	}
}

// WithSubAgentWorldTap observes the resolved dispatch world without displacing
// the writeback already installed on ctx, for the same reason
// WithSubAgentModelTap wraps rather than replaces.
func WithSubAgentWorldTap(ctx context.Context, tap func(SubAgentWorld)) context.Context {
	prev, _ := ctx.Value(subAgentWorldWriterKey{}).(func(SubAgentWorld))
	return WithSubAgentWorldWriter(ctx, func(w SubAgentWorld) {
		if tap != nil {
			tap(w)
		}
		if prev != nil {
			prev(w)
		}
	})
}

// SubAgentWorldFromContext returns the world last written through a pointer
// installed by WithSubAgentWorldPtr, or the zero value when none was.
func SubAgentWorldFromContext(ctx context.Context) SubAgentWorld {
	if ptr, ok := ctx.Value(subAgentWorldValueKey{}).(*SubAgentWorld); ok && ptr != nil {
		return *ptr
	}
	return SubAgentWorld{}
}

// WithSubAgentStartNotifier stores fn in ctx so the sub-agent runner can
// announce the resolved model at start time. The caller uses this to
// emit a start event that already knows which model the child runs on.
func WithSubAgentStartNotifier(ctx context.Context, fn func(model string)) context.Context {
	return context.WithValue(ctx, subAgentStartKey{}, fn)
}

// NotifySubAgentStart invokes the start notifier stored in ctx, if
// present. Call from the runner after the sub-agent model is resolved.
func NotifySubAgentStart(ctx context.Context, model string) {
	if fn, ok := ctx.Value(subAgentStartKey{}).(func(string)); ok {
		fn(model)
	}
}

// subAgentUsageKey stores a *types.Usage pointer for accumulating
// sub-agent token usage in the caller.
type subAgentUsageKey struct{}

// WithSubAgentUsage sets a usage accumulator in ctx so the caller can
// collect sub-agent token counts.
func WithSubAgentUsage(ctx context.Context, usage *types.Usage) context.Context {
	return context.WithValue(ctx, subAgentUsageKey{}, usage)
}

// AddSubAgentUsage adds delta to the usage accumulator in ctx, if present.
func AddSubAgentUsage(ctx context.Context, delta types.Usage) {
	if acc, ok := ctx.Value(subAgentUsageKey{}).(*types.Usage); ok {
		acc.PromptTokens += delta.PromptTokens
		acc.CompletionTokens += delta.CompletionTokens
		acc.TotalTokens += delta.TotalTokens
		if delta.PromptIncludesCache {
			acc.PromptIncludesCache = true
		}
		// CacheTokens covers both shapes (nested details on raw
		// provider usage, top-level on aggregates) — adding both
		// fields separately would double-count.
		acc.CachedPromptTokens += delta.CacheTokens()
	}
}

// subAgentHeartbeatKey is a context key for the per-sub-agent heartbeat
// channel. The sub-agent loop non-blocking-sends on this channel each
// iteration so a parent watchdog can detect stuck children.
type subAgentHeartbeatKey struct{}

// WithSubAgentHeartbeat stores hb in ctx so the sub-agent loop can emit
// heartbeats. The caller should create a buffered channel (cap 1) so the
// sub-agent never blocks on send.
func WithSubAgentHeartbeat(ctx context.Context, hb chan struct{}) context.Context {
	return context.WithValue(ctx, subAgentHeartbeatKey{}, hb)
}

// SendHeartbeat non-blocking-sends on the heartbeat channel stored in ctx,
// if present. Designed to be called at the top of each agent loop iteration.
func SendHeartbeat(ctx context.Context) {
	if hb, ok := ctx.Value(subAgentHeartbeatKey{}).(chan struct{}); ok {
		select {
		case hb <- struct{}{}:
		default:
		}
	}
}

// subAgentReadyKey is a context key for the closure a dispatch body calls
// once its dispatch world exists and the child's run is about to begin.
type subAgentReadyKey struct{}

// WithSubAgentReadyNotifier stores fn in ctx so the dispatching body can
// report that its dispatch world is ready.
//
// The stuck-child watchdog arms its deadline on this signal. A world that
// is still being provisioned — a worktree being created, a container being
// started — cannot heartbeat, so charging that window against the child's
// deadline reported healthy dispatches as stuck (yaah-95ri.2). The notifier
// is fired at most once per dispatch; the watchdog ignores repeats.
func WithSubAgentReadyNotifier(ctx context.Context, fn func()) context.Context {
	return context.WithValue(ctx, subAgentReadyKey{}, fn)
}

// NotifySubAgentReady invokes the ready notifier stored in ctx, if present.
// Call it from a dispatch body once the dispatch world exists and the
// child's run is about to start.
func NotifySubAgentReady(ctx context.Context) {
	if fn, ok := ctx.Value(subAgentReadyKey{}).(func()); ok && fn != nil {
		fn()
	}
}

// ErrStuckChild is returned when a sub-agent is cancelled by the parent
// watchdog after StuckChildTimeout elapses with no heartbeat from a child
// that had already reported at least one.
var ErrStuckChild = errors.New("sub-agent stuck: no heartbeat received within deadline")

// ErrDispatchStartup is returned when a dispatched sub-agent never started:
// its dispatch world became ready, but the child produced no heartbeat
// before the startup deadline.
//
// It is deliberately distinct from ErrStuckChild. A world that never got the
// child going is a dispatch/setup failure and its message names the world
// that was stood up; only a child that was beating and then went silent is
// stuck (yaah-95ri.3).
var ErrDispatchStartup = errors.New("sub-agent dispatch failed to start: the child never produced a heartbeat after its dispatch world became ready")

// ErrScopeExhausted reports a dispatch refused because the session's
// dispatch-scope budget is spent (yaah-95ri.4): the substrate refuses
// the new dispatch instead of provisioning a world into a scope tree
// already holding the maximum of concurrently active scopes, where
// exhaustion would otherwise hang into the watchdog. Unlike
// ErrStuckChild/ErrDispatchStartup this fires before any world is
// provisioned and before the watchdog arms, so it is never a
// reclassification of a hang.
var ErrScopeExhausted = errors.New("dispatch scope capacity exceeded: too many concurrently active dispatch scopes")

// subAgentTranscriptKey stores a *[]types.Message the sub-agent runner writes
// its final conversation into after the loop returns. A dispatch body installs
// it before invoking the runner so a turn-exhausted sub-agent's context survives
// the run and can seed a continuation instead of being thrown away.
type subAgentTranscriptKey struct{}

// WithSubAgentTranscript stores ptr in ctx so the sub-agent runner can report
// the final conversation it ended with. Only safe when the caller reads *ptr
// after joining the runner (the foreground retained lane does).
func WithSubAgentTranscript(ctx context.Context, ptr *[]types.Message) context.Context {
	return context.WithValue(ctx, subAgentTranscriptKey{}, ptr)
}

// CaptureSubAgentTranscript writes messages through the transcript sink stored
// in ctx, if present. The runner calls it once, after its loop returns.
func CaptureSubAgentTranscript(ctx context.Context, messages []types.Message) {
	if ptr, ok := ctx.Value(subAgentTranscriptKey{}).(*[]types.Message); ok && ptr != nil {
		*ptr = messages
	}
}

// SubAgentStop is the typed outcome of one sub-agent run: why it ended, so a
// dispatch envelope can report a stop_reason instead of leaving the
// orchestrator to infer it from result prose.
type SubAgentStop struct {
	// TurnBudgetExhausted reports that the soft tool-turn cap stripped tools
	// and forced a text-only final turn.
	TurnBudgetExhausted bool
	// IterationBudgetExhausted reports a MaxIterationsError hard stop.
	IterationBudgetExhausted bool
	// HaltedBySupervisor reports a deliberate supervisor halt/cancel.
	HaltedBySupervisor bool
	// FinishReason is the provider's last finish reason, when known.
	FinishReason string
}

// subAgentStopKey stores a *SubAgentStop the runner writes after its loop
// returns. A dispatch body installs it before invoking the runner.
type subAgentStopKey struct{}

// WithSubAgentStop stores ptr in ctx so the runner can report the run's typed
// stop outcome. Only safe when the caller reads *ptr after joining the runner.
func WithSubAgentStop(ctx context.Context, ptr *SubAgentStop) context.Context {
	return context.WithValue(ctx, subAgentStopKey{}, ptr)
}

// CaptureSubAgentStop writes stop through the sink stored in ctx, if present.
func CaptureSubAgentStop(ctx context.Context, stop SubAgentStop) {
	if ptr, ok := ctx.Value(subAgentStopKey{}).(*SubAgentStop); ok && ptr != nil {
		*ptr = stop
	}
}
