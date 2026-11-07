package runner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	goatherd "github.com/buchenberg/goatherd-runtime"
	"github.com/buchenberg/goatherd-runtime/sandbox/confinement"
	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/agent/llm"
	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/agent/supervision"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// taskRunnerOpts holds the shared state needed to build sub-agent loops.
// It is captured by every makeTaskRunner closure so nested sub-agents
// (planner -> worker) inherit the same provider, prompt base, and config.
type taskRunnerOpts struct {
	provider agent.Provider
	// providerName labels the planner's provider in sub-agent error
	// envelopes when no explicit sub-agent provider is configured.
	providerName  string
	systemPrompt  string
	modelName     string
	db            *memory.DB
	parentSession string
	subCfg        config.SubAgentConfig

	// subNetwork is the egress posture for confined dispatches (Plan 7 Phase 3),
	// lowered from agents.subagent.network with the session broker's port.
	//
	// The zero value means allow_all — the config default, and the behaviour of
	// every session before this key existed — and resolveSubAgentConfinement
	// normalizes it to an explicit allow_all rather than handing an empty mode to
	// the runtime (whose own zero policy fails closed to deny_all). A caller that
	// omits it therefore gets today's behaviour, not a dispatch that refuses
	// wherever the host cannot jail.
	subNetwork confinement.NetworkPolicy

	// subAgentProvider and subAgentModel override the planner's
	// provider/model for sub-agent loops. When nil/empty, sub-agents
	// inherit the planner's provider and model.
	subAgentProvider agent.Provider
	subAgentModel    string

	// OtelEnabled propagates OpenTelemetry tracing to sub-agent loops
	// so their tool calls appear as child spans in the trace waterfall.
	OtelEnabled bool

	// OtelVerbose propagates verbose trace recording to sub-agent loops
	// so their model content/reasoning is captured when the parent has
	// verbose tracing enabled.
	OtelVerbose bool

	// tracker records file operations from sub-agent write/edit/delete
	// tools so the parent agent can detect parallel-worker conflicts.
	tracker *tools.ConflictTracker

	// subContextWindow is the parent agent's context window halved for
	// sub-agent use, with a floor of 32000. Zero disables compaction.
	subContextWindow int

	// outputLimit caps the final synthesized sub-agent result in bytes.
	outputLimit int

	// estimateFactor is the preflight token estimate multiplier inherited
	// from the parent config.
	estimateFactor float64

	// providerMap is the full set of configured providers, needed for
	// per-role provider resolution by name. When nil/unset, per-role
	// provider overrides fall through to the global override.
	providerMap map[string]config.Provider

	// defaults carries the top-level agent defaults so sub-agent loops
	// inherit loop-detection thresholds, retry policies, compaction
	// tuning, and concurrency caps from the parent config.
	defaults config.Defaults

	// pipelineDisabled is agents.middleware.disabled, forwarded so an
	// opt-out such as [evidence_guard] also applies inside delegated loops.
	pipelineDisabled []string

	// parentPermissionRules, when non-nil, are passed to the sub-agent's
	// PermissionMiddleware so path-based deny rules from the parent
	// session are enforced by child agents.
	parentPermissionRules []pipeline.PermissionRule

	// toolSpillDir is the directory where oversized sub-agent tool
	// results are spilled to disk. Injected from the composition root so
	// sub-agents inherit the parent's spill directory instead of reaching
	// into config for paths. Empty disables spilling.
	toolSpillDir string

	// pathValidator, when non-nil, confines the sub-agent's
	// file-accessing tools to the session workspace, matching the
	// parent session's --workspace policy.
	pathValidator *tools.PathValidator

	// workspace, when non-nil, is the isolated session's sandbox
	// workspace (plan §1.3, decision D2): every sub-agent inherits the
	// session's one sandbox instead of running host-side. Mutually
	// exclusive with pathValidator — the wiring installs exactly one
	// substrate per session.
	workspace tools.Workspace

	// rootScopeID is the session's shepherd root scope (adoption plan
	// D1): sub-agent scopes fork from it. Empty falls back to the legacy
	// flat per-dispatch scope.
	rootScopeID string

	// runs is the session's supervision run registry (adoption plan A2),
	// injected by the wiring alongside the root scope — session-owned,
	// per the repo's no-globals convention. Nil when tracing is off:
	// dispatches then have no injectable bridge.
	runs *supervision.Registry

	// scopeManager is the session's scope registry (plan 07, H4),
	// injected by the wiring like the run registry — sub-agent scopes
	// fork through it without reaching through package globals. Nil
	// when tracing is off: dispatches then run without scopes.
	scopeManager *goatherd.ScopeManager

	// maxDispatchScopes caps concurrently active dispatch scopes; 0 =
	// jobs.DefaultMaxDispatchScopes (yaah-95ri.4).
	maxDispatchScopes int

	// resumedScopes is the set of scope IDs startup recovery re-admitted
	// from the trace (yaah-95ri.7). Nothing runs under them in this
	// process, so the dispatch cap must not count them — see
	// jobs.DispatchScopeCount. Nil when no recovery adopted anything.
	resumedScopes map[string]bool

	// traceStore is the session's Shepherd trace store (plan 07, H4),
	// injected alongside the manager: the sub-agent loops' shepherd_trace
	// middleware writes through it. Nil when tracing is off.
	traceStore pipeline.TraceSink

	// guardCheck is the supervision service's blocking pre-dispatch
	// check (adoption plan B2), injected by the wiring: each sub-agent
	// loop's pipeline consults it before executing a tool call, and a
	// non-empty reason denies the call — sub-agent loops run at
	// ApprovalMode "allow", so this is the net their calls pass
	// through. Nil when tracing is off or the guard is disabled in
	// config; calls then dispatch unchanged.
	guardCheck func(ownerID, toolName, argsJSON string) string

	// resolveProviderByName resolves a configured provider by name. It
	// is injected from the cmd layer because provider construction
	// (makeProvider) is CLI wiring that does not belong here. May be nil;
	// a nil resolver means per-role provider overrides fall through to
	// the planner's provider.
	resolveProviderByName func(pmap map[string]config.Provider, name string) agent.Provider

	// now mints dispatch timestamps for the sub-agent trace ID. Nil means
	// time.Now; tests pin it to force two dispatches into the same clock
	// tick and prove the per-dispatch sequence keeps their scope keys
	// apart.
	now func() time.Time
}

// dispatchClock returns the clock that stamps dispatch IDs: the test
// override when set, time.Now otherwise.
func dispatchClock(opts taskRunnerOpts) time.Time {
	if opts.now != nil {
		return opts.now()
	}
	return time.Now()
}

// subAgentTraceID mints the trace/scope owner ID for one dispatch. The
// kernel registers the sub-agent scope under this exact string
// ("scope:" + owner), so it must be unique per dispatch: two parallel
// delegate calls routinely start within one clock tick, which is why a
// timestamp alone is not a key — the same reason NewEntryID abandoned
// mem-<UnixNano>. The timestamp stays for ordering and readability in
// traces; 128 bits of crypto/rand make uniqueness structural.
func subAgentTraceID(role subagent.SubAgentRole, parent string, at time.Time) string {
	return fmt.Sprintf("sub-%s-%s-%d-%s", role, parent, at.UnixNano(), dispatchSuffix())
}

// dispatchSuffix returns 128 bits of crypto/rand, hex-encoded, matching
// newTurnID and NewEntryID. If the system entropy source is unavailable
// the timestamp is the fallback, accepting the collision risk that only
// a broken entropy source creates.
func dispatchSuffix() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return hex.EncodeToString(b[:])
}

// makeTaskRunner creates a sub-agent runner that honours roles, timeouts,
// iteration caps, and nesting depth.
//
// remainingDepth bounds how many levels of nested task calls the
// returned runner may itself issue. When it reaches zero the task tool
// is omitted from the sub-loop's registry, so nesting is bounded
// structurally without relying on middleware alone. Depth is hardcoded to 1.
func makeTaskRunner(opts taskRunnerOpts, remainingDepth int) tools.TaskRunner {
	return func(ctx context.Context, prompt string, params tools.SubAgentParams) (string, error) {
		// A per-dispatch world overrides the session's substrate for this
		// dispatch only (yaah-ihfh.3). The shadow copy keeps the captured
		// closure state immutable, so a container dispatch cannot leak its
		// workspace into the next one. The workspace and the host path
		// validator are mutually exclusive by construction: the workspace is
		// the substrate, and a validator rooted on the host would gate paths
		// in a tree this dispatch never sees.
		opts := dispatchOpts(opts, ctx)
		role := subagent.SubAgentRole(params.Role)
		profile := subagent.RoleProfileFor(role)

		// A tool-less profile is valid — some roles (e.g. grump) are
		// designed to respond without calling any tools.

		subReg, subConf, rerr := buildSubAgentRegistry(opts, profile, role, remainingDepth, params.Workdir)
		if rerr != nil {
			return "", fmt.Errorf("sub-agent %s: %w", role, rerr)
		}

		b := resolveSubAgentBudget(params.MaxLoopCycles, params.MaxToolTurns, profile, opts.subCfg, role)
		maxIter, maxTurns := b.Iterations, b.Turns

		// Record the effective budget and its provenance on the
		// sub-agent span (subagent-turn-budget-floors §4.7) so a trace
		// answers "who set this budget to N?" directly.
		if span := trace.SpanFromContext(ctx); span.IsRecording() {
			span.SetAttributes(
				attribute.Int("subagent.budget.iterations", b.Iterations),
				attribute.String("subagent.budget.iterations_source", b.IterationsSource.String()),
				attribute.Int("subagent.budget.turns", b.Turns),
				attribute.String("subagent.budget.turns_source", b.TurnsSource.String()),
			)
		}

		effectiveCW := opts.subContextWindow
		if rc, ok := opts.subCfg.Roles[string(role)]; ok && rc.ContextWindow > 0 {
			effectiveCW = rc.ContextWindow
		}

		jsonMode := params.JSONMode
		if !jsonMode && opts.subCfg.Roles[string(role)].JSONMode {
			jsonMode = true
		}
		if !jsonMode && profile.JSONMode {
			jsonMode = true
		}
		if !jsonMode && opts.subCfg.JSONMode {
			jsonMode = true
		}

		outLimit := resolveOutputLimit(params.OutputLimit, opts.subCfg, role, opts.outputLimit)

		sysPrompt := buildSubAgentSysPrompt(opts, role, profile, jsonMode, params.Workdir, subConf)

		// Record the role's directives on the sub-agent span so traces
		// show which policy statements this child received (if any).
		if ds := opts.subCfg.Roles[string(role)].Directives; len(ds) > 0 {
			if span := trace.SpanFromContext(ctx); span.IsRecording() {
				span.SetAttributes(attribute.String("subagent.directives", strings.Join(ds, " | ")))
			}
		}

		subProvider := opts.subAgentProvider
		subModel := opts.subAgentModel

		// Start with the provider that runs when no role override
		// resolves: the global sub-agent provider, else the planner's.
		subProviderName := opts.providerName
		if opts.subAgentProvider != nil && opts.subCfg.Provider != "" {
			subProviderName = opts.subCfg.Provider
		}

		if rc, ok := opts.subCfg.Roles[string(role)]; ok {
			if rc.Provider != "" {
				if opts.resolveProviderByName != nil {
					subProvider = opts.resolveProviderByName(opts.providerMap, rc.Provider)
					if subProvider != nil {
						subProviderName = rc.Provider
					}
				}
				if subProvider != nil {
					subModel = ""
				}
			}
			if rc.Model != "" {
				subModel = rc.Model
			}
		}
		if subProvider == nil {
			subProvider = opts.provider
			subProviderName = opts.providerName
		}
		if subModel == "" {
			subModel = opts.modelName
		}

		// Announce the resolved model before the loop runs so the parent
		// emits its sub-agent start event with the role/model pairing,
		// and the model pointer carries the final value even when the
		// loop errors early.
		tools.WriteSubAgentModel(ctx, subModel)
		tools.NotifySubAgentStart(ctx, subModel)

		subTraceID := subAgentTraceID(role, opts.parentSession, dispatchClock(opts))
		traceContext := subAgentRetainedContext(ctx, opts, role, params.Workdir, subConf, subReg)
		traceCWD := subAgentTraceCWD(opts, params.Workdir)

		// Persist the trace-owner mapping so Shepherd facts recorded under
		// the synthetic sub-ID remain joinable to the parent session
		// (consolidate-persistence Phase 0 / finding F4).
		if opts.db != nil {
			if err := opts.db.RecordTraceOwner(subTraceID, opts.parentSession, string(role)); err != nil {
				slog.Debug("trace_owner: record failed", "owner", subTraceID, "err", err)
			}
		}

		// Create a scope for this sub-agent so the supervisor can
		// inject guidance or halt it during execution. The scope is a
		// fork of the session's root scope (adoption plan D1), so the
		// supervisor sees the true dispatch tree; the fork's sandbox is
		// the sub-agent's workspace: its own worktree when the dispatch
		// isolated one, otherwise the root's shared sandbox (nil for an
		// isolated session — the container is the substrate, isolation
		// plan D2).
		var subScope *goatherd.Scope
		if mgr := opts.scopeManager; mgr != nil {
			var cerr error
			subScope, cerr = createSubAgentScope(pipeline.ScopeSourceOf(mgr), opts, subTraceID, params.Workdir)
			if cerr != nil {
				// A scope-creation failure fails the dispatch (R1
				// review): the durable worktree record is part of the
				// supervision contract, and a sub-agent that runs
				// unsupervised against a worktree the trace cannot
				// find is exactly the orphan this exists to prevent.
				return "", fmt.Errorf("sub-agent scope: %w", cerr)
			}
			// The run's scope dies with the run (yaah-95ri.4): every
			// dispatch body — report or proposal, container, worktree or
			// direct — leaves its runner-side supervision scope discarded.
			// The retained proposal's own scope is a separate fork that
			// survives until settlement; this one bridges THIS body only.
			defer func() {
				if derr := mgr.Discard(subScope.ID()); derr != nil {
					slog.Debug("dispatch scope discard at run end", "scope", subScope.ID(), "err", derr)
				}
			}()
		}

		// Record the resolved jail placement on the sub-agent's trace (Plan 7
		// Phase 3): requested vs resolved, the backend, and the sole writable
		// root — so an advisory run is never read as jailed.
		recordConfinementFact(ctx, subScope, subConf)

		// Supervision bridge (adoption plans A2/A3): the run registers
		// its steering channel and its cancellation under the scope ID,
		// so the supervisor tool's inject and halt reach this loop
		// instead of only recording trace facts. The loop runs under a
		// context derived from the dispatch's, so cancelling it stops
		// the run without touching the caller's context. The deferred
		// unregister is panic safety only; the explicit unregister
		// right after Run returns is the real cut, so the entry never
		// outlives the loop through post-run enrichment.
		var steerCh chan string
		var haltedBySupervisor atomic.Bool
		if subScope != nil && opts.runs != nil {
			steerCh = make(chan string, supervision.SteerBuffer)
			quiesce := make(chan struct{})
			haltCtx, cancelRun := context.WithCancel(ctx)
			opts.runs.Register(subScope.ID(), supervision.Run{
				SteerCh: steerCh,
				Cancel: func() {
					haltedBySupervisor.Store(true)
					cancelRun()
				},
				// Closed the moment the loop returns: the supervisor's
				// bounded quiesce wait before kernel-side sandbox
				// teardown selects on this.
				Done: quiesce,
			})
			defer opts.runs.Unregister(subScope.ID())
			defer close(quiesce)
			ctx = haltCtx
		}

		// Per-turn checkpointing is enabled per role (subagent.roles.<name>.
		// turn_checkpoints). When active, the checkpointer lives on this
		// sub-agent's own scope so turn rewinds never touch the supervised
		// tool's attempt-level checkpoints.
		var turnCk agent.TurnCheckpointer
		if opts.subCfg.Roles[string(role)].TurnCheckpoints && subScope != nil && subScope.Sandbox() != nil {
			turnCk = NewShepherdTurnCheckpointer(pipeline.ScopeSourceOf(opts.scopeManager), subScope.ID(), subScope.Sandbox())
		}

		subLoop := agent.NewSubAgentLoop(subProvider, subReg, subModel, sysPrompt, agent.SubAgentConfig{
			ProviderName:           subProviderName,
			MaxLoopCycles:          maxIter,
			MaxToolTurns:           maxTurns,
			BudgetIterationsSource: b.IterationsSource.String(),
			BudgetTurnsSource:      b.TurnsSource.String(),
			MaxRetries:             opts.defaults.MaxRetries,
			RetryBackoffSecs:       opts.defaults.RetryBackoffSecs,
			MaxToolConcurrency:     opts.defaults.MaxToolConcurrency,
			EvidenceGuardNudge:     opts.defaults.EvidenceGuardNudge,
			EvidenceGuardHalt:      opts.defaults.EvidenceGuardHalt,
			PipelineDisabled:       opts.pipelineDisabled,
			JSONMode:               jsonMode,
			SeedMessages:           params.SeedMessages,
			ToolResultMaxLines:     opts.defaults.ToolResultMaxLines,
			ToolResultMaxBytes:     opts.defaults.ToolResultMaxBytes,
			ToolSpillDir:           opts.toolSpillDir,
			PruneProtectTokens:     opts.defaults.PruneProtectTokens,
			PruneMinReclaim:        opts.defaults.PruneMinReclaim,
			PruneMinTurns:          opts.defaults.PruneMinTurns,
			PermissionRules:        opts.parentPermissionRules,
			ContextWindow:          effectiveCW,
			OtelEnabled:            opts.OtelEnabled,
			OtelVerbose:            opts.OtelVerbose,
			SessionID:              subTraceID,
			TraceStore:             opts.traceStore,
			TraceCWD:               traceCWD,
			RetainedContext:        traceContext,
			SteerCh:                steerCh,
			DestructiveGuard:       opts.guardCheck,

			TurnCheckpointer:      turnCk,
			TurnCheckpointEnabled: turnCk != nil,
			TurnCheckpointMax:     opts.defaults.TurnCheckpointMax,
			MaxTurnRestores:       opts.defaults.MaxTurnRestores,
		})

		result, runErr := subLoop.Run(ctx, prompt)
		// Capture the final conversation so a turn-exhausted dispatch can be
		// continued from its context instead of restarted cold. WithoutCancel
		// keeps the write alive when a timeout ended the run.
		tools.CaptureSubAgentTranscript(context.WithoutCancel(ctx), subLoop.State.Messages)
		var maxIterErr agent.MaxIterationsError
		iterExhausted := errors.As(runErr, &maxIterErr)
		tools.CaptureSubAgentStop(context.WithoutCancel(ctx), tools.SubAgentStop{
			// The loop sets TurnBudgetExhausted on the hard iteration stop too;
			// only the soft tool-turn cap means tools were stripped.
			TurnBudgetExhausted:      subLoop.State.TurnBudgetExhausted && !iterExhausted,
			IterationBudgetExhausted: iterExhausted,
			HaltedBySupervisor:       haltedBySupervisor.Load(),
			FinishReason:             subLoop.State.LastFinishReason,
		})
		recordSubAgentUsage(context.WithoutCancel(ctx), opts.traceStore, subTraceID, opts.parentSession, string(role), subModel, params.Lane, subLoop.OwnUsage())
		// Record the jail the body actually ran under, not just the one the
		// previous fact resolved: a backend can pass its availability check and
		// still fail to establish containment at exec time.
		recordConfinementDegradedFact(context.WithoutCancel(ctx), subScope, subConf)

		// The run is over: cut the bridge now, not at closure return —
		// trace enrichment and output budgeting below must not sit inside
		// a window where inject or halt claims delivery to a dead loop.
		if subScope != nil && opts.runs != nil {
			opts.runs.Unregister(subScope.ID())
		}

		// A supervisor halt is a deliberate stop, not a failure to
		// retry: report it as such instead of surfacing the raw
		// context-canceled noise from the cancelled run.
		if haltedBySupervisor.Load() && runErr != nil {
			runErr = fmt.Errorf("sub-agent %s halted by supervisor", role)
		}

		// Whitespace-only output is a silent failure (observed when a
		// forced final turn degenerates after tools are stripped). Turn
		// it into an explicit error so the orchestrator can retry with
		// a narrower task instead of seeing an empty result.
		if runErr == nil && strings.TrimSpace(result) == "" {
			runErr = fmt.Errorf("sub-agent %s produced no output — its final turn returned empty content (likely hit its turn cap mid-task; consider raising max_turns)", role)
		}

		// Markup-only output is a silent pseudo-success (issue #314):
		// the model emitted a tool call in a nonstandard text dialect,
		// the call never executed, and the raw markup became the report.
		// Turn it into an explicit error so the orchestrator never
		// accepts an unexecuted tool call as a completed task.
		if runErr == nil && llm.LooksLikeUnexecutedToolCalls(result) {
			runErr = fmt.Errorf("sub-agent %s returned unexecuted tool-call markup as its final report — the model emitted a tool call in a nonstandard text dialect that was never executed; treat this dispatch as failed (consider raising max_turns or reducing the role's toolset)", role)
		}

		// A zero-tool role cannot have executed anything, so a command
		// transcript with output evidence in its final report is
		// fabricated (issue #329 — the same pseudo-success class without
		// the markup). Scoped to tool-less dispatches: a role with a
		// shell quoting a transcript it really produced is legitimate.
		if runErr == nil && len(profile.Tools) == 0 && llm.LooksLikeFabricatedTranscript(result) {
			runErr = fmt.Errorf("sub-agent %s has no tools but returned a command transcript with invented output — the report is fabricated, nothing was executed; treat this dispatch as failed", role)
		}

		// Enrich errors with the sub-agent's trace so the orchestrator
		// can see exactly which tool calls led to the failure. Tracing is
		// integral, so the store path always resolves; only the profile
		// lookup decides whether there is anything to append.
		if runErr != nil {
			tracePath := opts.defaults.TracePath()
			if profile := subagentTraceProfile(ctx, tracePath, subTraceID); profile != "" {
				runErr = fmt.Errorf("%w\n\nSubagent trace:\n%s", runErr, profile)
			}
		}

		if outLimit > 0 && len(result) > outLimit {
			result = safeTruncateBytes(result, outLimit)
			result += "\n...[sub-agent output capped at " + formatBytes(outLimit) + "]"
		}

		// Summary budgeting: if the result exceeds 25% of the sub-agent's
		// context window, trim it to prevent sub-agent output from consuming
		// the parent's entire context headroom.
		if effectiveCW > 0 && len(result) > effectiveCW/4 {
			budget := effectiveCW / 4
			result = safeTruncateBytes(result, budget)
			result += "\n...[sub-agent output trimmed to context budget " + formatBytes(budget) + "]"
		}

		tools.AddSubAgentUsage(ctx, subLoop.State.TotalTokens)

		if span := trace.SpanFromContext(ctx); span.IsRecording() {
			span.SetAttributes(
				attribute.Int("subagent.prompt_tokens", subLoop.State.TotalTokens.PromptTokens),
				attribute.Int("subagent.completion_tokens", subLoop.State.TotalTokens.CompletionTokens),
				attribute.String("subagent.model", subModel),
			)
		}

		return result, runErr
	}
}

// subAgentTraceCWD mirrors buildSubAgentSysPrompt: the sandbox workdir wins
// when a workspace is installed, because the isolated lane ignores a
// model-supplied workdir and file tools resolve against the sandbox.
func subAgentTraceCWD(opts taskRunnerOpts, workdir string) string {
	if opts.workspace != nil {
		return opts.workspace.WorkDir()
	}
	return workdir
}

// subAgentRetainedContext binds a sub-agent's transcript to the dispatch's
// substrate, containment, and role tool belt. The kernel stamps it into every
// fact envelope, so authority provenance is digest-bound rather than living
// only in the mutable sessions.workspace_backend/session_prompt columns.
func subAgentRetainedContext(ctx context.Context, opts taskRunnerOpts, role subagent.SubAgentRole, workdir string, conf *subAgentConfinement, reg *tools.Registry) *shepherdtrace.RetainedContext {
	substrate := "yaah.substrate.host.v1"
	containment := shepherdtrace.ContainUncontained
	switch {
	case tools.DispatchWorkspace(ctx) != nil, opts.workspace != nil:
		substrate = "yaah.substrate.container.v1"
		// Match the runtime's container backend, which reports contained
		// (see sandboxfake's documented parity with containerd).
		containment = shepherdtrace.ContainContained
	case workdir != "":
		substrate = "yaah.substrate.worktree.v1"
		if conf != nil && conf.Enforced {
			containment = shepherdtrace.ContainContained
		}
	}
	if conf != nil && conf.Enforced && containment == shepherdtrace.ContainUncontained {
		containment = shepherdtrace.ContainContained
	}

	var caps []string
	if reg != nil {
		for _, name := range reg.List() {
			if name != "" {
				caps = append(caps, types.ToolSchemaRef(name))
			}
		}
	}
	sort.Strings(caps)
	return &shepherdtrace.RetainedContext{
		SubstrateRef:          substrate,
		Containment:           containment,
		CapabilityWitnessRefs: caps,
		ActiveBindingRefs:     []string{"role:" + string(role)},
	}
}

// dispatchOpts returns the runtime options for one dispatch. A per-dispatch
// container world travels in the context (the retained lane owns the sandbox,
// the runner owns the registry), and it replaces the session's substrate for
// this dispatch: the workspace becomes the tools' world and the host path
// validator is dropped, because the two substrates must never race for a tool.
// Without one the captured options pass through unchanged.
func dispatchOpts(opts taskRunnerOpts, ctx context.Context) taskRunnerOpts {
	if ws := tools.DispatchWorkspace(ctx); ws != nil {
		opts.workspace = ws
		opts.pathValidator = nil
	}
	return opts
}
