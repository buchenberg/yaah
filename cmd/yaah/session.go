package yaah

// UI Driver Pattern
//
// To attach a new UI to a session:
//
//  1. Create a chan control.Msg and call sess.SetCtrlCh(ch).
//     This wires todos and status messages to your channel.
//
//  2. Implement agent.View (HandleEvent) and call sess.SetView(your view).
//     Events: TokenDelta, Thinking, Flush, ToolStart, ToolEnd,
//             SubAgentStart, SubAgentEnd, Done.
//
//  3. Read from the control channel in a goroutine and dispatch
//     CtrlStatus, CtrlError, CtrlQuestion, CtrlApproval,
//     CtrlModelList, CtrlTodos, CtrlContextInfo, CtrlDone.
//
//  4. Call sess.RunPrompt(ctx, text) for each turn.
//     ctx cancellation aborts the in-flight agent loop.
//
// See: terminalView (REPL), agentViewFwd (Bubble Tea TUI).

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	goatherd "github.com/buchenberg/goatherd-runtime"
	"github.com/buchenberg/goatherd-runtime/sandbox/egress"
	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/agent/supervision"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/control"
	"github.com/buchenberg/yaah/internal/jobs"
	"github.com/buchenberg/yaah/internal/mcp"
	"github.com/buchenberg/yaah/internal/memory"
	processpkg "github.com/buchenberg/yaah/internal/process"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
	"github.com/buchenberg/yaah/internal/worldinfo"
)

// Session is the stable contract between UI drivers and the shared

// agent session. Any driver (TUI, REPL, web, gRPC) should depend only
// on this interface, not on *agentSession directly.
type Session interface {
	RunPrompt(ctx context.Context, prompt string) (string, bool, error)
	Compact()
	Steer(string)
	FollowUp(string)
	SetView(agent.View)
	SetCtrlCh(chan<- control.Msg)
	SetApproveFn(func(name, args string) bool)
	SetModel(providerName, modelName string)
	ProviderName() string
	ModelName() string
	MCPInfos() []mcp.ServerInfo
	// WorldStatus reports the session's workspace and dispatch-world
	// posture for display. The zero value means it was never derived;
	// surfaces hide the World section in that case.
	WorldStatus() worldinfo.Status
	Close()
}

// Compile-time check: agentSession satisfies Session.
var _ Session = (*agentSession)(nil)

// agentSession holds the long-lived infrastructure shared across REPL and
// one-shot prompts. Building it once avoids re-opening the database,
// re-spawning MCP servers, and re-discovering skills on every turn.
type agentSession struct {
	cfg *config.Config
	// opts snapshots the CLI-flag inputs used at construction time so
	// later per-turn helpers (approval resolution) never read globals.
	opts         SessionOptions
	provider     agent.Provider
	providerName string
	modelName    string
	systemPrompt string
	// mainPrompt is systemPrompt plus the default-agent directives
	// injected after the identity block. Only the top-level loop sees
	// it; sub-agents receive systemPrompt as their base so default
	// directives never leak into child prompts.
	mainPrompt string
	toolReg    *tools.Registry
	db         *memory.DB
	mcpClients []mcp.MCPClient
	mcpInfos   []mcp.ServerInfo
	// deepGate is the light/deep sleep policy, created once per session so
	// its every-Nth cadence spans prompt runs instead of resetting each turn.
	deepGate     *deepSleepGate
	deepGateOnce sync.Once
	// mcpToolNames lists tool names registered from MCP servers so the
	// loop can apply the mcp_approval policy to them (remote tools
	// cannot implement tools.DangerClassifier).
	mcpToolNames map[string]bool
	procMgr      *processpkg.Manager
	messages     []types.Message
	sessionID    string
	msgIdx       int
	// turnBudgetExhausted mirrors the last Run's soft tool-turn cap
	// outcome (tool-stripped final answer or hard iteration limit) so
	// the ACP server can report max_turn_requests instead of end_turn.
	turnBudgetExhausted bool
	// titlePublished tracks the persisted session title (empty until the
	// first user message derives one). Cached across prompt runs so the
	// store's one-shot title adoption is known without re-reading; built
	// runs seed the persister's KnownTitle from it and the persist hook
	// updates it. Guarded by mu.
	titlePublished string
	otelShutdown   func(context.Context) error
	tracker        *tools.ConflictTracker

	// backgroundJobs manages background sub-agents for this session:
	// owns their session-rooted contexts, tracks them for status/cancel,
	// attributes their usage to totalUsage, and delivers results as
	// follow-ups. Cancelled at session close.
	backgroundJobs *jobs.BackgroundJobs

	// drainTimeout overrides backgroundJobDrainTimeout when set — the
	// session-owned seam the close tests use; zero means the default.
	drainTimeout time.Duration

	// sessionSandbox owns the isolated workspace backend's lifecycle, when
	// workspace.backend is containerd: the sandbox was constructed, image-
	// probed (D5), and created once at session start, and close() destroys
	// it. Nil in local mode.
	sessionSandbox *tools.SessionSandbox

	// worldStatus is the session-level workspace/dispatch-world posture,
	// derived once by the wiring after the containment switch. Read by every
	// UI driver through WorldStatus; the zero value means it was never
	// populated (surfaces hide the section).
	worldStatus worldinfo.Status

	// rootScopeID is the session's shepherd root scope (adoption plan
	// A2): registered in the run registry against steerCh, so supervisor
	// inject into the root steers the session itself. close() unregisters
	// it BEFORE closing steerCh — a registry send must never land on a
	// closed channel. Empty when tracing is off.
	rootScopeID string

	// runs is the session's supervision run registry (adoption plan A2),
	// injected by the wiring, session-owned per the repo's no-globals
	// convention; the root scope registers here, and it is the D2
	// bridge the supervisor tool — and the B1 rules engine — deliver
	// through. close() unregisters the root BEFORE closing steerCh — a
	// registry send must never land on a closed channel. Nil when
	// tracing is off.
	runs *supervision.Registry

	// supervisor is the session's advisory rules service (adoption plan
	// B1): it drains the kernel supervisor's interventions into runs.
	// Nil when tracing is off or the engine is disabled in config.
	// Closed FIRST in teardown — a drain mid-delivery must finish
	// before the registry it delivers through goes away.
	supervisor *supervision.Service

	// egressBroker is the session's sub-agent egress broker (Plan 7 Phase 3),
	// non-nil only when agents.subagent.network is "broker". close() stops it
	// after background sub-agents are drained — Stop waits for accepted
	// connections, so stopping before the drain would take the network away from
	// a job that has not been cancelled yet.
	egressBroker *egress.Broker

	// scopes is the session's scope manager (adoption plan R2): the
	// recovered-at-startup registry the wiring handed the session.
	// close() prunes its terminal scopes — merged and discarded
	// children accumulate over a long session, and the trace records
	// survive either way. Nil when tracing is off.
	scopes *goatherd.ScopeManager

	// traceStore and traceBus are the session's shepherd infrastructure
	// (plan 07, H2): the store and bus the wiring created and the
	// session closes on a clean exit — store first, then the bus, only
	// when the background-job drain succeeded, warn-only like the rest
	// of the clean-exit path. Nil when tracing is off.
	traceStore *shepherdtrace.SQLiteTraceStore
	traceBus   *shepherdtrace.EffectBus

	// runCancel is the session-owned cancellation for the currently
	// running prompt (adoption plan A3): the target of
	// HaltCurrentRun — the root-halt bridge. Nil between prompts.
	runCancel context.CancelFunc

	cwd string

	view      agent.View
	ctrlCh    chan<- control.Msg
	approveFn func(name, args string) bool
	// approveCallFn is the call-id-aware approval bridge (ACP). When
	// set it takes precedence over approveFn.
	approveCallFn func(callID, name, args string) bool
	mu            sync.RWMutex

	// pendingEvents buffers session-scoped events for views whose
	// HandleEvent cannot safely run outside a prompt (the REPL and ACP
	// connection writers). Each event keeps the view it was produced for, so
	// an ACP session switch cannot replay another session's completion into
	// the new connection. Bounded so a view that never runs again cannot
	// retain results forever.
	pendingEvents []pendingSessionEvent
	// evictedStarts records sub-agent IDs whose buffered start was dropped by
	// the cap; their later end is dropped too instead of rendering an
	// unmatched completion.
	evictedStarts map[string]bool

	stdinMu sync.Mutex // serialises stdin reads for approve/prompt fallbacks

	steerCh    chan string
	followupCh chan string
	totalUsage types.Usage
	// flushIn/flushOut/flushCached/flushSubIn/flushSubOut are the last
	// values written to the session row by the incremental usage flush.
	// The flush is monotonic: a snapshot that arrives out of order
	// (concurrent tool goroutines) or computed from a narrower view
	// (the per-call hook's before-run + running) can never overwrite a
	// larger, already-flushed total.
	flushIn, flushOut, flushCached int
	flushSubIn, flushSubOut        int
	// subUsage attributes the sub-agent share of totalUsage: blocking
	// dispatches fold it into the loop totals (agent_dispatch) and fire
	// the loop's OnSubUsage here; background dispatches report through
	// the jobs manager's session-scoped OnUsage. totalUsage includes
	// sub-agent spend; orchestrator spend = totalUsage - subUsage.
	subUsage types.Usage

	// Compact/fallback providers resolved once per session and reused
	// across turns — resolving them per prompt constructed fresh HTTP
	// clients every turn (review B15). auxResolved guards lazy fill;
	// SetModel invalidates the cache so a provider switch re-resolves.
	compactProvider  agent.Provider
	compactModel     string
	fallbackProvider agent.Provider
	fallbackModel    string
	fallbackName     string
	auxResolved      bool
}

// backgroundJobDrainTimeout bounds how long close() waits for cancelled
// background sub-agents to finish before destroying the session sandbox
// anyway. Generous: cancellation propagates through the tool call in
// flight, and the alternative is racing a live sub-agent with backend
// teardown. A session whose drainTimeout field is set uses that instead
// — the test seam, session-owned per the no-globals convention.
const backgroundJobDrainTimeout = 30 * time.Second

// workspaceSandboxDestroyTimeout bounds the sandbox teardown in close() so
// a wedged containerd daemon cannot hang session shutdown.
const workspaceSandboxDestroyTimeout = 30 * time.Second

func (s *agentSession) close() {
	ctx := context.Background()
	// The advisory rules service drains INTO the registry below: stop it
	// first and join its drain goroutine, so no delivery is mid-flight
	// when the registry entries it addresses start going away.
	if s.supervisor != nil {
		s.supervisor.Close()
	}
	// The root's registry entry goes before its channel closes: the
	// registry sends under its lock, so an unregister that returns
	// guarantees no later send — and a send on a closed channel panics.
	if s.rootScopeID != "" && s.runs != nil {
		s.runs.Unregister(s.rootScopeID)
	}
	if s.steerCh != nil {
		close(s.steerCh)
	}
	// Cancel any still-running background sub-agents, unblock their result
	// deliveries, and WAIT (bounded) for their goroutines to finish: a job
	// mid-Exec through the sandbox workspace must not still be running
	// when the session sandbox is destroyed below. Close() alone only
	// cancels — it never joins — which is a use-after-destroy race, not a
	// quiesced teardown.
	drainTimeout := s.drainTimeout
	if drainTimeout == 0 {
		drainTimeout = backgroundJobDrainTimeout
	}
	drained := true
	if s.backgroundJobs != nil {
		if !s.backgroundJobs.CloseAndWait(drainTimeout) {
			drained = false
			fmt.Fprintf(os.Stderr, "warning: background sub-agent(s) still finishing after %s; continuing session teardown\n", drainTimeout)
		}
	}
	// The egress broker stops AFTER the drain, not before: Stop() waits for its
	// accepted connections, and a background jailed sub-agent can still be
	// mid-tunnel through it — stopping first would both block shutdown and pull
	// the broker out from under a job that has not been cancelled yet.
	if s.egressBroker != nil {
		s.egressBroker.Stop()
	}
	// PruneTerminal on close (adoption plan R2): merged and discarded
	// children accumulate over a long session; their trace records
	// survive either way, so the in-memory registry shrinks here.
	// Runs AFTER the background-jobs drain — the jobs share this
	// manager, and the registry must not mutate under their forks and
	// checkpoints. The warn-only orphans are untouchable by it —
	// recovery detached their sandboxes, and pruning never destroys
	// anything.
	if s.scopes != nil {
		s.scopes.PruneTerminal()
	}
	// followupCh closes only AFTER the drain. A finishing job delivers
	// through it, and Deliver's select still has a send case on this
	// channel — sending on a closed channel panics, so closing it while
	// the drain deliberately keeps job goroutines alive would crash
	// shutdown. (A job that outlives the drain bound entirely retains the
	// pre-existing exposure; the drain is what shrinks it.)
	if s.followupCh != nil {
		close(s.followupCh)
	}
	// Destroy the isolated workspace sandbox only after the sub-agents are
	// cancelled and can no longer dispatch tools into it. Bounded, so a
	// wedged daemon cannot hang close(); a teardown failure warns rather
	// than aborts close() — the rest of the session still needs tearing
	// down, and the warning names the container that may still be running.
	if s.sessionSandbox != nil {
		destroyCtx, cancelDestroy := context.WithTimeout(ctx, workspaceSandboxDestroyTimeout)
		defer cancelDestroy()
		if err := s.sessionSandbox.Destroy(destroyCtx); err != nil {
			fmt.Fprintf(os.Stderr, "warning: workspace sandbox teardown: %v\n", err)
		}
	}
	// Shepherd infrastructure (plan 07, H2): a clean exit closes the trace
	// store and its bus — store first, then the bus — instead of leaving
	// both to whatever the process exit does to them. Warn-only, like the
	// rest of this path. Gated on the drain: a background job that
	// outlived CloseAndWait shares this store, and closing it under a
	// live writer turns the job's final writes into corruption-shaped
	// failures — worse than an unclosed store the process exit reaps, so
	// the infrastructure stays open with a warning instead.
	if s.traceStore != nil || s.traceBus != nil {
		if !drained {
			fmt.Fprintln(os.Stderr, "warning: shepherd trace store left open — a background job outlived the drain and still writes it")
		} else {
			pipeline.CloseShepherdInfrastructure(s.traceStore, s.traceBus)
			s.traceStore, s.traceBus = nil, nil
		}
	}
	s.mu.RLock()
	ch := s.ctrlCh
	s.mu.RUnlock()
	if ch != nil {
		ch <- &control.Done{}
	}
	if s.otelShutdown != nil {
		s.otelShutdown(ctx)
	}
	if s.db != nil {
		s.db.EndSession(s.sessionID, time.Now().Unix(), s.totalUsage.PromptTokens, s.totalUsage.CompletionTokens,
			s.totalUsage.CachedPromptTokens, s.subUsage.PromptTokens, s.subUsage.CompletionTokens)
		// Memory hygiene (docs/compaction-as-sleep.md §8): run the age-based
		// staleness sweep. Sessions are resumable, so session-scoped atoms
		// are intentionally NOT pruned here — only old, never-accessed
		// compaction atoms go, whatever scope they carry.
		pruneMemoryAtClose(s.db)
		s.db.Close()
	}
	for _, c := range s.mcpClients {
		c.Close()
	}
}

func (s *agentSession) Close()   { s.close() }
func (s *agentSession) Compact() { s.compactContext() }

// flushUsage persists the session's running usage counters to state.db
// without ending the session. Caller must hold s.mu.
func (s *agentSession) flushUsage() {
	s.flushUsageValues(
		s.totalUsage.PromptTokens, s.totalUsage.CompletionTokens,
		s.totalUsage.CachedPromptTokens,
		s.subUsage.PromptTokens, s.subUsage.CompletionTokens)
}

// flushUsageValues writes the given usage counters to the session's
// row without ending the session. Caller must hold s.mu. Values may be
// computed (not read from the session's live totals) so the per-call
// hook can flush without clobbering concurrent background attribution.
// The flush is monotonic — only values that advance every counter past
// the last write are written; a stale or out-of-order snapshot (the
// OnSubUsage path fires after the fold already flushed, and concurrent
// tool goroutines can deliver snapshots late) is skipped rather than
// allowed to shrink the row. A failed flush is logged, not fatal:
// usage accounting must never take down a session.
func (s *agentSession) flushUsageValues(tokensIn, tokensOut, tokensCached, subTokensIn, subTokensOut int) {
	if s.db == nil {
		return
	}
	if tokensIn < s.flushIn || tokensOut < s.flushOut || tokensCached < s.flushCached ||
		subTokensIn < s.flushSubIn || subTokensOut < s.flushSubOut {
		return
	}
	s.flushIn, s.flushOut, s.flushCached = tokensIn, tokensOut, tokensCached
	s.flushSubIn, s.flushSubOut = subTokensIn, subTokensOut
	err := s.db.UpdateSessionUsage(s.sessionID, tokensIn, tokensOut, tokensCached, subTokensIn, subTokensOut)
	if err != nil {
		slog.Warn("session: usage flush failed", "err", err)
	}
}

// ToolReg returns the session's tool registry.
func (s *agentSession) ToolReg() *tools.Registry { return s.toolReg }

// AgentSessionID returns the id of the underlying persisted
// conversation (the memory-DB session row). The ACP server exposes it
// as the ACP session id so a later session/load in a fresh process
// names the same conversation.
func (s *agentSession) AgentSessionID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessionID
}

// History returns a copy of the session's live conversation, used by
// the ACP server to replay the conversation on session/load. A copy:
// the loop mutates the backing slice during runs while the caller
// streams it after releasing the server lock.
func (s *agentSession) History() []types.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]types.Message, len(s.messages))
	copy(out, s.messages)
	return out
}

func (s *agentSession) ProviderName() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.providerName
}
func (s *agentSession) ModelName() string          { s.mu.RLock(); defer s.mu.RUnlock(); return s.modelName }
func (s *agentSession) MCPInfos() []mcp.ServerInfo { return s.mcpInfos }

// WorldStatus returns the derived workspace/dispatch-world posture. It is
// immutable after session construction, so no lock is needed.
func (s *agentSession) WorldStatus() worldinfo.Status { return s.worldStatus }

// Model reports the provider and model the session currently runs on.
// The ACP server reads it to publish the model picker's current value;
// it is the pair SetModel last installed.
func (s *agentSession) Model() (provider, model string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.providerName, s.modelName
}

// TurnBudgetExhausted reports whether the last Run ended because a
// request budget ran out — the soft tool-turn cap stripped tools before
// the model finished, or the hard iteration limit was reached. ACP maps
// it to stopReason max_turn_requests so the client can offer a continue.
func (s *agentSession) TurnBudgetExhausted() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.turnBudgetExhausted
}

// RunPrompt runs one prompt under a context the session owns: derived
// from the caller's, so the frontends' own cancels (TUI abort, serve's
// signal context) compose, but with a session-held cancel that
// HaltCurrentRun can invoke — that is what supervisor halt on the root
// scope bridges to (adoption plan A3, gated by supervisor.halt_root).
func (s *agentSession) RunPrompt(ctx context.Context, prompt string) (string, bool, error) {
	runCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.runCancel = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.runCancel = nil
		s.mu.Unlock()
		cancel()
	}()
	return s.runPrompt(runCtx, prompt)
}

// HaltCurrentRun cancels the currently running prompt, if one is
// running — the supervisor's root-halt bridge (adoption plan A3). It is
// the session's ctrl-C equivalent: the run unwinds, the frontends keep
// their own cancel paths, and a session with no active run is a no-op.
func (s *agentSession) HaltCurrentRun() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runCancel != nil {
		s.runCancel()
	}
}

func (s *agentSession) Steer(text string) {
	select {
	case s.steerCh <- text:
	default:
		s.sendCtrl(&control.Status{Text: "steer queue full"})
	}
}

func (s *agentSession) FollowUp(text string) {
	select {
	case s.followupCh <- text:
	default:
		s.sendCtrl(&control.Status{Text: "follow-up queue full"})
	}
}

func (s *agentSession) sendCtrl(msg control.Msg) {
	s.mu.RLock()
	ch := s.ctrlCh
	s.mu.RUnlock()
	if ch == nil {
		return
	}
	select {
	case ch <- msg:
	default:
	}
}

func (s *agentSession) SetView(v agent.View) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.view = v
}

// maxPendingSessionEvents bounds the between-run event buffer. A view that
// never starts another run (headless serve) must not retain result strings
// indefinitely.
const maxPendingSessionEvents = 64

// pendingSessionEvent pairs an event with the view that was current when it
// arrived. Unsafe views change between ACP prompts, so delivery must not use
// whatever view happens to be current at drain time.
type pendingSessionEvent struct {
	view agent.View
	evt  agent.Event
}

// dispatchEvent delivers a session-scoped event to the current view. Only
// views that marshal HandleEvent onto their own UI loop (or otherwise guard
// their output) accept events between runs; the rest are buffered until the
// next run's view forwarder drains them, so a background completion cannot
// interleave with the REPL prompt or an ACP connection write. Nil and Noop
// views drop the event.
func (s *agentSession) dispatchEvent(evt agent.Event) {
	s.mu.Lock()
	v := s.view
	if v == nil || isNoopView(v) {
		s.mu.Unlock()
		return
	}
	safe := false
	if sink, ok := v.(interface{ AcceptsEventsBetweenRuns() bool }); ok {
		safe = sink.AcceptsEventsBetweenRuns()
	}
	if !safe {
		if len(s.pendingEvents) >= maxPendingSessionEvents {
			s.evictOldestPendingLocked()
		}
		if end, ok := evt.(*agent.SubAgentEndEvent); ok && s.evictedStarts[end.SubAgentID] {
			delete(s.evictedStarts, end.SubAgentID)
			s.mu.Unlock()
			return
		}
		s.pendingEvents = append(s.pendingEvents, pendingSessionEvent{view: v, evt: evt})
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	v.HandleEvent(evt)
}

// evictOldestPendingLocked drops the oldest buffered event. When that event
// is a sub-agent start, its matching end is dropped too, so the cap cannot
// leave a completion without its start. Caller holds s.mu.
func (s *agentSession) evictOldestPendingLocked() {
	if len(s.pendingEvents) == 0 {
		return
	}
	first := s.pendingEvents[0]
	if start, ok := first.evt.(*agent.SubAgentStartEvent); ok {
		paired := false
		for i := 1; i < len(s.pendingEvents); i++ {
			end, ok := s.pendingEvents[i].evt.(*agent.SubAgentEndEvent)
			if ok && end.SubAgentID == start.SubAgentID && s.pendingEvents[i].view == first.view {
				s.pendingEvents = append(s.pendingEvents[:i], s.pendingEvents[i+1:]...)
				paired = true
				break
			}
		}
		if !paired {
			if s.evictedStarts == nil {
				s.evictedStarts = make(map[string]bool)
			}
			s.evictedStarts[start.SubAgentID] = true
		}
	}
	s.pendingEvents = s.pendingEvents[1:]
}

// isNoopView reports whether v discards events, so buffering them for a
// future run would only waste memory.
func isNoopView(v agent.View) bool {
	switch v.(type) {
	case agent.NoopView, *agent.NoopView:
		return true
	default:
		return false
	}
}

// deliverPendingEvents replays buffered events to the view that produced
// them. It runs on the run's BrokerView forwarder goroutine, before any live
// broker event, so the view still sees events from exactly one goroutine and
// start/end order is preserved.
func (s *agentSession) deliverPendingEvents() {
	s.mu.Lock()
	pending := s.pendingEvents
	s.pendingEvents = nil
	s.mu.Unlock()
	for _, p := range pending {
		if p.view != nil {
			p.view.HandleEvent(p.evt)
		}
	}
}

func (s *agentSession) SetCtrlCh(ch chan<- control.Msg) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ctrlCh = ch

	if tt := s.toolReg.Get("todowrite"); tt != nil {
		if ttp, ok := tt.(*tools.TodoWriteTool); ok {
			ttp.OnWrite = func() {
				ch <- &control.Todos{Items: ttp.Store.List()}
			}
		}
	}
}

func (s *agentSession) SetApproveFn(fn func(name, args string) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.approveFn = fn
}

// SetApproveCallFn installs the call-id-aware approval bridge. It takes
// precedence over SetApproveFn; drivers that never need the provider
// tool_call id keep using SetApproveFn.
func (s *agentSession) SetApproveCallFn(fn func(callID, name, args string) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.approveCallFn = fn
}

// promptWorkspaceAccess is the PathValidator's ask fallback: it routes
// out-of-workspace access decisions through the driver's approval UI
// when one is attached (TUI, web) and falls back to a stdin prompt
// otherwise, mirroring the loop's approveTool behaviour.
func (s *agentSession) promptWorkspaceAccess(path, reason string) bool {
	s.mu.RLock()
	call := s.approveCallFn
	fn := s.approveFn
	s.mu.RUnlock()
	if call != nil {
		// Call-id bridge (ACP). The ask carries no tool call, so the
		// empty id tells requestPermission to fall back to its
		// request-id identity and emit no tool_call row for it.
		return call("", "workspace_access", fmt.Sprintf("%s (%s)", path, reason))
	}
	if fn != nil {
		return fn("workspace_access", fmt.Sprintf("%s (%s)", path, reason))
	}

	s.stdinMu.Lock()
	defer s.stdinMu.Unlock()

	fi, err := os.Stdin.Stat()
	if err != nil || (fi.Mode()&os.ModeCharDevice) == 0 {
		return false
	}

	fmt.Fprintf(os.Stderr, "\n  ⚠ Path %s is %s. Allow access? [y/N]: ", path, reason)
	os.Stderr.Sync()

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 1024), 1024)
	if scanner.Scan() {
		input := strings.ToLower(strings.TrimSpace(scanner.Text()))
		return input == "y" || input == "yes"
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "  read error: %v\n", err)
	}
	return false
}

func (s *agentSession) SetModel(providerName, modelName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prov := s.provider
	if p, ok := s.cfg.Providers[providerName]; ok {
		if pv, ok2 := makeProvider(providerName, p); ok2 {
			prov = pv
		}
	}
	s.provider = prov
	s.providerName = providerName
	s.modelName = modelName
	s.auxResolved = false
}

// auxProviders returns the session-cached compact and fallback
// providers, resolving them on first use and again after a /model
// switch invalidates the cache (review B15).
func (s *agentSession) auxProviders() (compact agent.Provider, compactModel string, fb agent.Provider, fbModel, fbName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.auxResolved {
		rawCompact, cm := resolveCompact(s.cfg)
		s.compactProvider = agent.ResolveCompactProvider(rawCompact, s.cfg.Observability.Otel.Verbose)
		s.compactModel = cm
		s.fallbackProvider, s.fallbackModel, s.fallbackName = resolveFallback(s.cfg)
		s.auxResolved = true
	}
	return s.compactProvider, s.compactModel, s.fallbackProvider, s.fallbackModel, s.fallbackName
}
