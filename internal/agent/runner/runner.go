// Package runner wires sub-agent execution: it builds the per-role tool
// registries, resolves provider/model/timeout/iteration budgets, assembles
// the sub-agent system prompt, and constructs the top-level DelegateTool that
// the planner uses to spawn sub-agents.
//
// This is a composition layer over the agent loop (internal/agent), the
// tool registry (internal/tools), role profiles (internal/agent/subagent),
// config, memory, and prompts. It also provides the SubAgentRoleResolver
// adapter that bridges agent/subagent's RoleRegistry to the
// tools.RoleResolver interface, so the tools package never needs to import
// agent/subagent directly.
package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	shepherdtrace "github.com/buchenberg/shepherd-kernel-go"
	shepherd "github.com/buchenberg/shepherd-runtime-go"
	shepherdsandbox "github.com/buchenberg/shepherd-runtime-go/sandbox"
	"github.com/buchenberg/shepherd-runtime-go/sandbox/confinement"
	"github.com/buchenberg/shepherd-runtime-go/sandbox/containment"
	shepherdgit "github.com/buchenberg/shepherd-runtime-go/sandbox/git"
	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/agent/budget"
	"github.com/buchenberg/yaah/internal/agent/llm"
	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/agent/supervision"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/gitsandbox"
	"github.com/buchenberg/yaah/internal/jail"
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/prompts"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// DelegateToolOpts carries everything NewDelegateTool needs. A struct beats a
// 20-parameter constructor: new sub-agent knobs no longer grow the
// signature, and call sites read as keyed literals (finding C3).
type DelegateToolOpts struct {
	Provider              agent.Provider
	ProviderName          string
	SystemPrompt          string
	ModelName             string
	DB                    *memory.DB
	SessionID             string
	SubAgentProvider      agent.Provider
	SubAgentModel         string
	SubCfg                config.SubAgentConfig
	RoleNames             []string
	OtelEnabled           bool
	OtelVerbose           bool
	Tracker               *tools.ConflictTracker
	EstimateFactor        float64
	SubContextWindow      int
	OutputLimit           int
	ProviderMap           map[string]config.Provider
	Defaults              config.Defaults
	PipelineDisabled      []string
	ParentPermissionRules []pipeline.PermissionRule

	// ToolSpillDir is where oversized sub-agent tool results spill to
	// disk. The composition root passes the same directory it injected
	// into the parent loop's LoopConfig so both share one spill home.
	ToolSpillDir string

	PathValidator *tools.PathValidator

	// Workspace, when non-nil, is the isolated session's sandbox
	// workspace: sub-agents inherit it (plan §1.3, decision D2) so their
	// tools run inside the session's container instead of on the host.
	// Mutually exclusive with PathValidator — the wiring installs
	// exactly one substrate per session.
	Workspace tools.Workspace

	// RootScopeID is the session's shepherd root scope (shepherd-adoption
	// plan D1): sub-agent scopes fork from it, so the supervisor's scope
	// tree mirrors the dispatch tree. Empty when tracing is off or the
	// root could not be created — the runner then falls back to a flat
	// per-dispatch scope, the pre-plan behaviour.
	RootScopeID string

	// Runs is the session's supervision run registry (adoption plan A2):
	// the runner registers each dispatch's steering channel under its
	// scope ID so supervisor inject reaches the loop. Nil when tracing
	// is off.
	Runs *supervision.Registry

	// ScopeManager is the session's scope registry (plan 07, H4):
	// sub-agent scopes fork through it. Nil when tracing is off.
	ScopeManager *shepherd.ScopeManager

	// TraceStore is the session's Shepherd trace store (plan 07, H4):
	// the sub-agent loops' shepherd_trace middleware writes through it.
	// Nil when tracing is off.
	TraceStore *shepherdtrace.SQLiteTraceStore

	// GuardCheck is the supervision service's blocking pre-dispatch
	// check (adoption plan B2), passed through to every sub-agent loop's
	// pipeline: a non-empty reason denies the tool call before
	// execution. Nil when tracing is off or the guard is disabled.
	GuardCheck func(ownerID, toolName, argsJSON string) string

	// ResolveProviderByName translates a configured provider name into a
	// live agent.Provider. Injected from the cmd layer because provider
	// construction (makeProvider) belongs with the rest of the CLI wiring.
	ResolveProviderByName func(map[string]config.Provider, string) agent.Provider

	// Retain enables the retained-run lane for delegate (Plan 7 Phase 2):
	// each dispatch runs in an isolated worktree and returns a sealed proposal
	// the orchestrator settles. RetainedRepoPath / RetainedWorktreeRoot /
	// RetainedBootstrap are resolved from the retained_* config keys — they
	// provision the worktree.
	Retain               bool
	RetainedRepoPath     string
	RetainedWorktreeRoot string
	RetainedBootstrap    string

	// SubNetwork is the egress posture for confined dispatches (Plan 7 Phase 3),
	// lowered from agents.subagent.network with the session broker's port (see
	// SubAgentNetworkPolicy). The zero value means allow_all — the config
	// default; the composition root passes an explicit policy either way.
	SubNetwork containment.NetworkPolicy
}

// NewDelegateTool builds the planner's DelegateTool: it resolves the advertised
// role names/descriptions and returns a DelegateTool whose Runner is a
// makeTaskRunner closure bounded to one level of nesting.
func NewDelegateTool(opts DelegateToolOpts) *tools.DelegateTool {
	// Sub-agent spawning depth is hard-coded at 1: the top-level agent
	// can spawn one level of sub-agents; sub-agents cannot spawn further
	// sub-agents (remainingDepth reaches 0).
	depth := 1
	runnerFn := makeTaskRunner(taskRunnerOpts{
		provider:              opts.Provider,
		providerName:          opts.ProviderName,
		systemPrompt:          opts.SystemPrompt,
		modelName:             opts.ModelName,
		db:                    opts.DB,
		parentSession:         opts.SessionID,
		subCfg:                opts.SubCfg,
		subNetwork:            opts.SubNetwork,
		subAgentProvider:      opts.SubAgentProvider,
		subAgentModel:         opts.SubAgentModel,
		OtelEnabled:           opts.OtelEnabled,
		OtelVerbose:           opts.OtelVerbose,
		tracker:               opts.Tracker,
		estimateFactor:        opts.EstimateFactor,
		subContextWindow:      opts.SubContextWindow,
		outputLimit:           opts.OutputLimit,
		providerMap:           opts.ProviderMap,
		defaults:              opts.Defaults,
		pipelineDisabled:      opts.PipelineDisabled,
		parentPermissionRules: opts.ParentPermissionRules,
		toolSpillDir:          opts.ToolSpillDir,
		pathValidator:         opts.PathValidator,
		workspace:             opts.Workspace,
		rootScopeID:           opts.RootScopeID,
		runs:                  opts.Runs,
		scopeManager:          opts.ScopeManager,
		traceStore:            opts.TraceStore,
		guardCheck:            opts.GuardCheck,
		resolveProviderByName: opts.ResolveProviderByName,
	}, depth)

	tool := &tools.DelegateTool{
		Runner:           runnerFn,
		ResolveTimeout:   subAgentTimeoutResolver(opts.SubCfg),
		RoleNames:        opts.RoleNames,
		RoleDescriptions: RoleDescriptionsFor(opts.RoleNames),
		Tracker:          opts.Tracker,
	}
	// Retained mode (Plan 7 Phase 2): route delegate through the
	// retained-run lane. It needs a scope manager and a root scope to fork from;
	// without them the tool stays on the direct lane rather than failing each
	// dispatch.
	if opts.Retain && opts.ScopeManager != nil && opts.RootScopeID != "" {
		tool.Retained = &tools.RetainedRuntime{
			Runner:            runnerFn,
			RepoPath:          opts.RetainedRepoPath,
			ScopeManager:      opts.ScopeManager,
			RootScopeID:       opts.RootScopeID,
			WorktreeRoot:      opts.RetainedWorktreeRoot,
			WorktreeBootstrap: opts.RetainedBootstrap,
		}
	}
	return tool
}

// RoleDescriptionsFor maps each role name to a one-line description (or
// specialty fallback) drawn from the role registry. Used to populate the
// role parameter description of the delegate schema so the orchestrator
// can choose a role without calling list_subagents.
func RoleDescriptionsFor(roleNames []string) map[string]string {
	descs := make(map[string]string, len(roleNames))
	for _, name := range roleNames {
		p := subagent.RoleProfileFor(subagent.SubAgentRole(name))
		if p.Description != "" {
			descs[name] = p.Description
		} else if p.Specialty != "" {
			descs[name] = p.Specialty
		}
	}
	return descs
}

// BuiltinRoleFiles reads the embedded roles/*.md files shipped in the
// binary and returns them keyed by file name (e.g. "worker.md").
func BuiltinRoleFiles() map[string][]byte {
	entries, err := prompts.BuiltinRolesFS.ReadDir("roles")
	if err != nil {
		return nil
	}
	files := make(map[string][]byte, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, _ := prompts.BuiltinRolesFS.ReadFile("roles/" + e.Name())
		files[e.Name()] = data
	}
	return files
}

// RoleSearchPaths returns directories to scan for user-defined role
// definitions. Mirrors the skill search hierarchy: project-level
// (walked up from cwd) then user-level (~/.agents/roles/).
func RoleSearchPaths(cwd string) []string {
	home := config.HomeDir()
	var dirs []string
	for dir := cwd; ; dir = filepath.Dir(dir) {
		dirs = append(dirs, filepath.Join(dir, ".agents", "roles"))
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	dirs = append(dirs, filepath.Join(home, ".agents", "roles"))
	return dirs
}

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
	subNetwork containment.NetworkPolicy

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
	scopeManager *shepherd.ScopeManager

	// traceStore is the session's Shepherd trace store (plan 07, H4),
	// injected alongside the manager: the sub-agent loops' shepherd_trace
	// middleware writes through it. Nil when tracing is off.
	traceStore *shepherdtrace.SQLiteTraceStore

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
		role := subagent.SubAgentRole(params.Role)
		profile := subagent.RoleProfileFor(role)

		// A tool-less profile is valid — some roles (e.g. grump) are
		// designed to respond without calling any tools.

		subReg, subConf, rerr := buildSubAgentRegistry(opts, profile, remainingDepth, params.Workdir)
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

		subTraceID := fmt.Sprintf("sub-%s-%s-%d", role, opts.parentSession, time.Now().UnixNano())

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
		var subScope *shepherd.Scope
		if mgr := opts.scopeManager; mgr != nil {
			var cerr error
			subScope, cerr = createSubAgentScope(mgr, opts, subTraceID, params.Workdir)
			if cerr != nil {
				// A scope-creation failure fails the dispatch (R1
				// review): the durable worktree record is part of the
				// supervision contract, and a sub-agent that runs
				// unsupervised against a worktree the trace cannot
				// find is exactly the orphan this exists to prevent.
				return "", fmt.Errorf("sub-agent scope: %w", cerr)
			}
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
			turnCk = NewShepherdTurnCheckpointer(opts.scopeManager, subScope.ID(), subScope.Sandbox())
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
			SteerCh:                steerCh,
			DestructiveGuard:       opts.guardCheck,

			TurnCheckpointer:      turnCk,
			TurnCheckpointEnabled: turnCk != nil,
			TurnCheckpointMax:     opts.defaults.TurnCheckpointMax,
			MaxTurnRestores:       opts.defaults.MaxTurnRestores,
		})

		result, runErr := subLoop.Run(ctx, prompt)
		recordSubAgentUsage(context.WithoutCancel(ctx), opts.traceStore, subTraceID, string(role), subModel, subLoop.OwnUsage())

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

// buildSubAgentSysPrompt assembles the full system prompt for a sub-agent
// loop. Section order: environment header, identity-derived base prompt,
// per-role directives (from agent.subagent.roles.<role>.directives), the
// isolated-workspace delta (isolated sessions only, decision D2), role
// guidance, response contract, escalation format, and shell anchoring.
// Directives sit immediately after the base prompt so they stay prominent
// instead of being buried behind role guidance and format blocks.
//
// Shell guidance is gated on the role's tool set (#329): a role without a
// shell tool gets an environment header with no shell sentence, and a
// zero-tool role's base prompt swaps the tool-oriented sub-agent identity
// for the no-tools variant whose directive leads the identity block.
func buildSubAgentSysPrompt(opts taskRunnerOpts, role subagent.SubAgentRole, profile subagent.RoleProfile, jsonMode bool, workdir string, conf *subAgentConfinement) string {
	cwd, _ := os.Getwd()

	// The tools this environment cannot offer (yaah-9n0): the shell
	// header and the available-shell block must describe what the
	// registry actually holds, not what the role asked for.
	unavailable := subAgentUnavailableTools(opts, profile, workdir)
	effectiveTools := profile.Tools
	if len(unavailable) > 0 {
		dropped := make(map[string]bool, len(unavailable))
		for _, name := range unavailable {
			dropped[name] = true
		}
		effectiveTools = make([]string, 0, len(profile.Tools))
		for _, name := range profile.Tools {
			if !dropped[name] {
				effectiveTools = append(effectiveTools, name)
			}
		}
	}

	// The environment header must advertise the directory the sub-agent's
	// tools actually resolve against. In an isolated session that is the
	// sandbox workdir, not the host cwd — the header's working-directory
	// line is what shell-cd'ing and relative paths key off, and a sandboxed
	// tool rejects the advertised host path as an escape. The shell line
	// comes from the workspace too: the container's shell is sh, not the
	// host's bash/powershell. A local-mode workdir dispatch (a supervised
	// session's fork variant) gets the same treatment: its file tools and
	// shell are rooted at the assigned worktree, so advertising the
	// orchestrator's cwd would point the model at the wrong tree (#312).
	envOS, envArch, envShell, envCwd := runtime.GOOS, runtime.GOARCH, "", cwd
	if opts.workspace != nil {
		envShell, _ = opts.workspace.Shell()
		envCwd = opts.workspace.WorkDir()
	} else {
		if workdir != "" {
			envCwd = workdir
		}
		if runtime.GOOS == "windows" {
			envShell = "powershell (pwsh 7+ or Windows PowerShell)"
		} else {
			envShell = "bash"
		}
	}
	// A role without a shell tool must not receive the header's "use it
	// for all shell commands" imperative (#329): the registry gives it no
	// shell, and tool-less roles resolve the contradiction by fabricating
	// fenced command blocks with invented output. An empty shell drops the
	// sentence; the OS and working-directory lines stay.
	if !roleHasShell(effectiveTools) {
		envShell = ""
	}

	// A zero-tool role gets the no-tools identity variant: the directive
	// sits before the identity text so it lands early in the prompt, and
	// the tool-batching cardinal rule and named-tool guidance disappear.
	base := opts.systemPrompt
	if len(effectiveTools) == 0 {
		base = prompts.InjectNoToolsIdentity(base)
	}
	// Section order is a caching contract (yaah-o02): the prompt opens
	// with the byte-stable block — identity, directives, guidance,
	// contract, escalation, shell rules — so sibling dispatches and later
	// turns of one dispatch share the longest possible provider
	// prompt-cache prefix. The volatile parts close it: confinement
	// guidance, then the environment header with its per-dispatch
	// working directory.
	sysPrompt := base

	if ds := opts.subCfg.Roles[string(role)].Directives; len(ds) > 0 {
		sysPrompt += "\n\n" + prompts.FormatDirectives(ds)
	}

	// Isolated session (decision D2): the sub-agent shares the session's
	// container, so its guidance must say so — without it a sub-agent
	// claims its changes are saved, or retries a host-only tool its
	// profile names that isolated mode omitted. Placed after the
	// directives block, which the section order below pins immediately
	// after the base prompt.
	if opts.workspace != nil {
		var dropped []string
		for _, name := range profile.Tools {
			if tools.LeafToolIsHostOnly(name) {
				dropped = append(dropped, name)
			}
		}
		sysPrompt += "\n\n" + prompts.IsolatedSubAgentGuidance(dropped)
	}

	if g := subagent.RoleGuidance(role); g != "" {
		if sysPrompt != "" {
			sysPrompt += "\n\n"
		}
		sysPrompt += g
	}
	if g := prompts.SubAgentUnavailableTools(unavailable); g != "" {
		sysPrompt += "\n\n" + g
	}

	if profile.Contract.Heading != "" && len(profile.Contract.Fields) > 0 {
		var b strings.Builder
		if jsonMode {
			b.WriteString("\n\nRespond with a JSON object matching the contract below.\n\n")
		}
		b.WriteString(prompts.ContractRules())
		b.WriteString("\n\n## Response contract\n\n")
		b.WriteString("Always end your response with a structured block:\n\n```\n")
		b.WriteString(profile.Contract.Heading + "\n")
		for _, f := range profile.Contract.Fields {
			if f.Kind != "" {
				b.WriteString("- **" + f.Name + "** (" + f.Kind + "): <value>\n")
			} else {
				b.WriteString("- **" + f.Name + "**: <value>\n")
			}
		}
		b.WriteString("```")
		sysPrompt += b.String()
	}

	// Escalation contract: all sub-agents must know how to raise
	// structured escalations when they hit a blocker.
	sysPrompt += prompts.Escalation()

	if roleHasShell(effectiveTools) {
		shell := "bash"
		if runtime.GOOS == "windows" {
			shell = "powershell"
		}
		otherShell := "powershell"
		if runtime.GOOS == "windows" {
			otherShell = "bash, sh, or cmd"
		}
		if opts.workspace != nil {
			// The isolated workspace's contract (D5) guarantees sh
			// — bash may exist in the image, but only sh is promised,
			// and the shell the workspace actually launches is what
			// the header below already advertises. The two statements
			// must agree, or the model flips between them.
			shell, _ = opts.workspace.Shell()
			otherShell = "bash or powershell — only " + shell + " is guaranteed in the container"
		}
		sysPrompt += "\n\n## Available Shell\n"
		sysPrompt += fmt.Sprintf("Your environment only has %s available. ", shell)
		sysPrompt += fmt.Sprintf("Use %s for ALL command execution. ", shell)
		sysPrompt += fmt.Sprintf("There is no %s.", otherShell)
	}

	// A confined dispatch gets told the boundary before it meets it (Plan 7
	// Phase 3): the one writable root, the redirect, and the git-write rule that
	// the default flip makes reachable. Enforced, not merely requested — a
	// paddock placement with no usable backend refuses at Exec, so there is no
	// run to advise. This and the environment header below are the prompt's
	// only per-dispatch sections; both name the assigned worktree.
	// The egress half follows the POSTURE (a dispatch refused a jail still must not
	// reach the network from outside one); the jail half follows enforcement. The
	// function returns "" when it has nothing to say, so the block is only added
	// when it does.
	if opts.workspace == nil {
		// The posture comes from the POLICY: the confinement record carries it only
		// when the jail was established, so reading it from conf would suppress the
		// egress half exactly when the jail was refused — while the tools are still
		// omitted.
		network, broker := networkMode(opts.subNetwork), opts.subNetwork.BrokerPort
		if g := prompts.ConfinementSubAgentGuidance(workdir, network, broker,
			hostNetworkToolsIn(profile), conf != nil && conf.Enforced); g != "" {
			if sysPrompt != "" {
				sysPrompt += "\n\n"
			}
			sysPrompt += g
		}
	}

	sysPrompt += "\n\n" + prompts.EnvironmentHeader(envOS, envArch, envShell, envCwd)
	return sysPrompt
}

// subAgentTimeoutResolver returns a DelegateTool timeout resolver that folds
// in the actual per-call role, so role-profile and per-role config
// timeouts are honoured rather than a single construction-time default.
func subAgentTimeoutResolver(subCfg config.SubAgentConfig) func(tools.SubAgentParams) time.Duration {
	return func(p tools.SubAgentParams) time.Duration {
		return resolveSubAgentTimeout(0, subCfg, subagent.SubAgentRole(p.Role))
	}
}

// subAgentSandbox returns the workspace substrate for a sub-agent: its own
// isolated worktree when the supervised session assigned one, otherwise the
// shared repository.
//
// In the isolated case the worktree already exists — the supervised session
// created it — so this sandbox is used for capture and apply only and never
// calls Create. A nil return leaves the scope pure-causal, which makes its
// workspace operations report ErrNoSandbox rather than silently doing nothing.
func subAgentSandbox(opts taskRunnerOpts, workdir string) shepherdsandbox.Sandbox {
	// Isolated session (plan §1.3, decision D2): the sub-agent shares the
	// session's one sandbox, so there is no per-sub-agent host substrate
	// to capture. A host worktree or host git sandbox would checkpoint a
	// tree this sub-agent never touches, and in-container checkpoints
	// could not isolate one sub-agent's turns from the parent's work in
	// the shared root — the same reason D2 rejects per-sub-agent
	// sandboxes. Nil keeps the scope pure-causal, so the supervisor's
	// inject/halt still works and turn checkpoints stay off rather than
	// rewinding the shared container.
	if opts.workspace != nil {
		return nil
	}
	repoPath := opts.defaults.RetainedRepoPath
	if repoPath == "" {
		repoPath, _ = os.Getwd()
	}
	if repoPath == "" {
		return nil
	}
	if workdir != "" {
		return shepherdgit.NewWorktreeSandbox(repoPath, workdir)
	}
	return gitsandbox.NewLocalSandbox(repoPath)
}

// ErrRootTerminal reports a recovered root that cannot be reattached
// because its lifecycle already ended (halted, discarded, or merged).
// Callers use it to make the degraded mode explicit instead of
// silently falling back to flat scopes.
var ErrRootTerminal = errors.New("recovered root scope is terminal")

// CreateRootScope creates the session's root scope (shepherd-adoption plan
// D1): the anchor every sub-agent scope forks from, owned by the session so
// scope IDs and trace owners line up. Local mode gets the shared in-place
// git sandbox — the same backend supervised sessions use, and never
// destroyed (the kernel's materializer contract). An isolated session gets
// a pure-causal root: the container is the substrate, and host-git capture
// would record a tree the session never works on (isolation plan D2). An
// empty repoPath falls back to the process working directory, matching
// subAgentSandbox's resolution so the root and legacy child scopes agree
// on the repository.
//
// The sandbox is probed with one capture before the root is registered:
// Fork captures the parent's baseline first, so a root over a directory
// git cannot capture (not a repository, unborn HEAD) would make every
// child fork fail — and the dispatch ignores that error, leaving all
// sub-agents without a scope. A failed probe demotes the root to
// pure-causal: the tree still exists, inject/halt still address every
// scope, and only checkpoint capture is unavailable — which it would
// have been anyway.
func CreateRootScope(mgr *shepherd.ScopeManager, sessionID, repoPath string, isolated bool) (*shepherd.Scope, error) {
	// Resume reattach (adoption plan R2): --resume continues under
	// the same session ID, so a recovered registry already holds the
	// crashed run's root — reattach it instead of colliding.
	// Recovery re-adopted its sandbox (or kept it pure-causal for an
	// isolated session, matching the mode the resolver was given), so
	// the scope tree resumes where it left off.
	if scope, ok := mgr.Get("scope:" + sessionID); ok {
		if scope.State() == shepherd.ScopeActive {
			return scope, nil
		}
		return nil, fmt.Errorf("%w: %s — it is terminal, so scope-tree supervision for this session ended; start a new session", ErrRootTerminal, scope.State())
	}
	var sb shepherdsandbox.Sandbox
	if !isolated {
		if repoPath == "" {
			repoPath, _ = os.Getwd()
		}
		if repoPath != "" {
			candidate := gitsandbox.NewLocalSandbox(repoPath)
			if _, cerr := candidate.Capture(context.Background()); cerr == nil {
				sb = candidate
			}
		}
	}
	return mgr.Create(sessionID, sb)
}

// createSubAgentScope creates the sub-agent's execution scope (adoption
// plan D1): a fork of the session's root scope, so the scope tree mirrors
// the dispatch tree and recovery can rebuild it. A workdir dispatch forks
// in isolation with its own worktree sandbox (destroyed on halt/discard by
// the kernel, exactly as the pre-plan Create+WorktreeSandbox path was);
// every other dispatch shares the root's sandbox. The fork snapshot is nil
// (D1): seed messages live in the loop config, not the trace. An empty
// root scope ID — tracing came up without a root — falls back to the
// legacy flat Create so the sub-agent still gets a superviseable scope.
func createSubAgentScope(mgr *shepherd.ScopeManager, opts taskRunnerOpts, subTraceID, workdir string) (*shepherd.Scope, error) {
	if opts.rootScopeID == "" {
		return mgr.Create(subTraceID, subAgentSandbox(opts, workdir))
	}
	// Post-halt dispatch policy (plan 07, H6): a root whose lifecycle
	// ended — the supervisor halt's target, A3's session-level kill —
	// refuses the dispatch loudly instead of silently degrading to a
	// flat scope outside the session tree. A racing dispatch that
	// landed there would be superviseable-but-orphaned: recovery
	// rebuilds it as a sibling of a dead session, and its scope ID
	// addresses nothing the session owns.
	//
	// The pre-check fails fast (the workdir path would provision a
	// worktree for a dispatch that cannot run), and the fork-failure
	// paths below RE-CHECK before degrading: a halt that lands between
	// the check and the fork must not slip through the TOCTOU window
	// into the flat-scope fallback. A root missing from the registry
	// is not this policy's case — the fork fails and the dispatch
	// degrades like any other non-terminal failure.
	root, ok := mgr.Get(opts.rootScopeID)
	if ok && root.State() != shepherd.ScopeActive {
		return nil, fmt.Errorf("%w: %s — the supervisor halt ended this session's supervision; start a new session", ErrRootTerminal, root.State())
	}
	// workdir isolates this dispatch in its own worktree — a local-mode
	// concept: an isolated session ignores it (its root has no sandbox,
	// so the fork is already pure-causal and the container's root is
	// every sub-agent's workspace). Without a resolvable repository the
	// worktree cannot be provisioned (subAgentSandbox returns nil in the
	// same degenerate case), so the dispatch degrades to a plain shared
	// fork rather than a worktree sandbox rooted at an empty path.
	if workdir != "" && opts.workspace == nil {
		repoPath := opts.defaults.RetainedRepoPath
		if repoPath == "" {
			repoPath, _ = os.Getwd()
		}
		if repoPath != "" {
			child, ferr := mgr.ForkIsolated(opts.rootScopeID, subTraceID, nil, shepherdgit.NewWorktreeSandbox(repoPath, workdir))
			if ferr == nil {
				// No yaah-side worktree record is needed (plan 06,
				// kernel v0.14.0): the kernel's fork record carries
				// the sandbox address itself, so recovery finds the
				// worktree from the trace alone.
				return child, nil
			}
			// A halt that landed between the pre-check and this fork
			// must not slip through the TOCTOU window into the
			// fallback: re-check before degrading. A nil root is the
			// registry miss the pre-check let through — it degrades
			// like any other non-terminal failure.
			if root != nil && root.State() != shepherd.ScopeActive {
				return nil, fmt.Errorf("%w: %s — the supervisor halt ended this session's supervision; start a new session", ErrRootTerminal, root.State())
			}
			// A non-terminal ForkIsolated failure degrades loudly:
			// the sub-agent still runs, superviseable, but its escape
			// from the session tree is visible, not silent.
			slog.Warn("sub-agent dispatch: isolated fork failed; degrading to a flat scope outside the session tree",
				"root", opts.rootScopeID, "sub", subTraceID, "err", ferr)
			return mgr.Create(subTraceID, subAgentSandbox(opts, workdir))
		}
	}
	child, ferr := mgr.Fork(opts.rootScopeID, subTraceID, nil)
	if ferr == nil {
		return child, nil
	}
	// A halt that landed between the pre-check and this fork must not
	// slip through the TOCTOU window into the fallback: re-check
	// before degrading. A nil root is the registry miss the pre-check
	// let through — it degrades like any other non-terminal failure.
	if root != nil && root.State() != shepherd.ScopeActive {
		return nil, fmt.Errorf("%w: %s — the supervisor halt ended this session's supervision; start a new session", ErrRootTerminal, root.State())
	}
	// A non-terminal fork failure degrades loudly, the same contract:
	// superviseable, with the escape from the tree visible.
	slog.Warn("sub-agent dispatch: fork failed; degrading to a flat scope outside the session tree",
		"root", opts.rootScopeID, "sub", subTraceID, "err", ferr)
	return mgr.Create(subTraceID, subAgentSandbox(opts, workdir))
}

// buildSubAgentRegistry builds the tool registry for a sub-agent from
// its role profile. If the profile includes the task tool and
// remainingDepth > 0, a nested DelegateTool is registered so the sub-agent
// can spawn further workers. When remainingDepth == 0 the task tool is
// omitted entirely.
//
// An isolated session (opts.workspace set) installs the session's sandbox
// workspace — the sub-agent inherits the container (decision D2) — and
// skips any host-only tool the profile names; a SetWorkspace refusal
// surfaces as an error rather than a silently host-side sub-agent.
// subAgentUnavailableTools names the tools a role asked for that this
// environment cannot offer: a shell the machine does not have, or Go
// toolchain machinery in a workspace that is not a Go module.
//
// The role file states what the role needs; the environment decides what
// is there. Shipping a schema for a tool that cannot run costs tokens on
// every call and buys a failed call later, so the tool is left out and
// named in the prompt instead (yaah-9n0).
func subAgentUnavailableTools(opts taskRunnerOpts, profile subagent.RoleProfile, workdir string) []string {
	// The root of the dispatch, in the same order the rest of this file
	// uses: the workspace decides (in the isolated lane the workdir is
	// meaningless), then the assigned worktree, then the process cwd the
	// shared lane runs in.
	// The Go check needs a HOST path: the workspace's workdir is a
	// container path ("/workspace"), which the host cannot stat. The
	// container holds a copy of the session's project, so the project
	// root is the right proxy — the assigned worktree in the local
	// lane, the process cwd in the isolated one (where the workdir is
	// meaningless).
	root := workdir
	if opts.workspace != nil {
		root = ""
	}
	if root == "" {
		root, _ = os.Getwd()
	}
	goAvailable := tools.GoToolAvailable(root)

	var unavailable []string
	for _, name := range profile.Tools {
		switch name {
		case "bash":
			// The local lane runs the shell on this machine, so the
			// host decides. The isolated lane delegates to the
			// workspace, which runs commands in the container.
			if opts.workspace == nil && !tools.ShellToolAvailable(name) {
				unavailable = append(unavailable, name)
			}
		case "powershell":
			// There is no PowerShell inside the Linux sandbox — the
			// tool itself refuses there — so the isolated lane must
			// not advertise it. The local lane needs one on PATH.
			if opts.workspace != nil || !tools.ShellToolAvailable(name) {
				unavailable = append(unavailable, name)
			}
		default:
			if tools.IsGoToolchainTool(name) && !goAvailable {
				unavailable = append(unavailable, name)
			}
		}
	}
	return unavailable
}

func buildSubAgentRegistry(opts taskRunnerOpts, profile subagent.RoleProfile, remainingDepth int, workdir string) (*tools.Registry, *subAgentConfinement, error) {
	resolveTimeout := subAgentTimeoutResolver(opts.subCfg)
	registerTask := func(reg *tools.Registry) {
		reg.Register(&tools.DelegateTool{
			Runner:         makeTaskRunner(opts, remainingDepth-1),
			ResolveTimeout: resolveTimeout,
		})
	}

	wrapWithTracker := func(t tools.Tool) tools.Tool {
		if opts.tracker != nil {
			return tools.NewRecordingTool(t, opts.tracker)
		}
		return t
	}

	destructiveTools := map[string]bool{
		"write":  true,
		"edit":   true,
		"delete": true,
	}

	registerLeaf := func(reg *tools.Registry, name string) bool {
		if t := tools.NewLeafTool(name); t != nil {
			if destructiveTools[name] {
				reg.Register(wrapWithTracker(t))
			} else {
				reg.Register(t)
			}
			return true
		}
		return false
	}

	// The egress requirement is a property of the DISPATCH, not of the lane that
	// happens to jail it, so it is decided here — before any lane gate. A
	// workdir-less dispatch (a shared-mode spawn, a model-supplied empty workdir)
	// never reaches the confinement path, and leaving the check inside it dropped
	// the posture silently and left the host-network tools registered.
	//
	// The isolated lane is exempt: its egress is the container's posture, which
	// this key does not govern (documented in the config reference).
	if opts.workspace == nil {
		hostBackend := jail.Backend()
		hostAvailable := false
		if hostBackend != nil {
			if ok, _ := hostBackend.Available(); ok {
				hostAvailable = true
			}
		}
		if err := requireEnforceableEgress(hostAvailable, hostBackend, opts.subNetwork,
			containmentPlacement(opts.subCfg.Containment), workdir, strings.TrimSpace(opts.subCfg.Containment)); err != nil {
			return nil, nil, err
		}
	}

	reg := tools.NewEmptyRegistry()
	var conf *subAgentConfinement
	isolated := opts.workspace != nil
	if !isolated && (opts.pathValidator != nil || workdir != "") {
		// An isolated sub-agent gets a validator rooted at its own checkout, so
		// its file tools are contained there and its shell runs there. Policy
		// (deny patterns, home-access, approval callback) is inherited; the
		// derived validator starts with a fresh approval cache.
		//
		// A workdir dispatch installs a rooted validator even when the session
		// carries none (the default local mode): without it params.Workdir
		// never reaches the tools, file paths and the shell resolve against
		// the process cwd, and a supervised session's fork variants all write
		// the ONE main tree — worktrees stay pristine, sealed deltas come out
		// empty, and choose has nothing to apply (#312/#318).
		pv := opts.pathValidator
		if workdir != "" {
			pv = pv.WithWorkspaceRoot(workdir)
		}
		reg.SetPathValidator(pv)

		// Confinement seam (Plan 7 Phase 1): on the sub-agent workdir lane the
		// sub-agent's shell runs under the syscall jail when configured, with
		// the worktree as its sole writable root. Guarded on workdir: only that
		// lane has a bound world to jail against. The orchestrator's workspace
		// and a workdir-less (shared-host) sub-agent keep running unconfined.
		if workdir != "" {
			ws, rec, cerr := resolveSubAgentConfinement(pv, opts.subCfg, workdir, opts.subNetwork)
			if cerr != nil {
				return nil, nil, cerr
			}
			// Record the resolution even when there is no workspace: "auto"
			// degrading to advisory returns a nil workspace WITH a record, and
			// that advisory resolution is exactly what the trace must show.
			conf = rec
			if ws != nil {
				if err := reg.SetWorkspace(ws); err != nil {
					slog.Debug("sub-agent confinement unavailable", "workdir", workdir, "err", err)
					conf = nil
				}
			}
		}
	}
	// The jail governs the processes a dispatch starts, and these tools make their
	// requests in THIS process — so a restrictive egress posture omits them rather
	// than leaving a hole in it. (They are named in the prompt, so the sub-agent
	// does not read a missing tool as a broken profile.)
	// Host lanes only. The isolated lane's egress is the container's posture, which
	// this key neither applies nor refuses (the guard above, and the config
	// reference). Omitting tools there would also be wrong twice over: it would
	// describe a rule the container does not have (its loopback is not the host's),
	// and these two tools execute in THIS process — outside the container — so the
	// container's posture does not cover them either. That is a known gap of the
	// isolated lane, not something this key can paper over; it is stated in the
	// docs rather than implied.
	restrictiveEgress := opts.workspace == nil && restrictivePosture(opts.subNetwork)

	// The tools this environment cannot offer are left out (yaah-9n0).
	unavailable := subAgentUnavailableTools(opts, profile, workdir)
	unavailableSet := make(map[string]bool, len(unavailable))
	for _, name := range unavailable {
		unavailableSet[name] = true
	}

	for _, name := range profile.Tools {
		if name == "delegate" {
			if remainingDepth > 0 {
				registerTask(reg)
			}
			continue
		}
		if unavailableSet[name] {
			continue
		}
		if restrictiveEgress && tools.LeafToolUsesHostNetwork(name) {
			continue
		}
		if isolated && tools.LeafToolIsHostOnly(name) {
			// The role profile names a host-only tool; isolated mode
			// omits it for the same reason the curated session
			// registry does. The system prompt names the omission so
			// the sub-agent does not retry a call that cannot succeed.
			continue
		}
		registerLeaf(reg, name)
	}
	if isolated {
		// Isolated session (plan §1.3, decision D2): the sub-agent
		// inherits the session's sandbox workspace, so its file tools
		// and shell run inside the container instead of on the host.
		// SetWorkspace runs after registration — the same ordering as
		// the session registry in the wiring — so the gate validates
		// the tool set the sub-agent actually gets: a profile that
		// named an unmigrated or host-only tool that slipped past the
		// filter fails the dispatch loudly instead of silently
		// running that tool host-side. workdir has no meaning here —
		// the container's root is the workspace — so it is ignored
		// rather than remapped.
		if serr := reg.SetWorkspace(opts.workspace); serr != nil {
			return nil, nil, fmt.Errorf("isolated sub-agent workspace: %w", serr)
		}
	}
	return reg, conf, nil
}

// resolveSubAgentTimeout picks the effective default timeout for a
// sub-agent DelegateTool. Precedence: per-call override (handled by the
// DelegateTool itself) > role-specific config > role profile default >
// global subagent default_timeout.
func resolveSubAgentTimeout(callSeconds int, subCfg config.SubAgentConfig, role subagent.SubAgentRole) time.Duration {
	if callSeconds > 0 {
		return time.Duration(callSeconds) * time.Second
	}
	if rc, ok := subCfg.Roles[string(role)]; ok && rc.Timeout > 0 {
		return time.Duration(rc.Timeout) * time.Second
	}
	if d := subagent.RoleProfileFor(role).Timeout; d > 0 {
		return d
	}
	if subCfg.DefaultTimeout > 0 {
		return time.Duration(subCfg.DefaultTimeout) * time.Second
	}
	return 0
}

// budgetSpecFor assembles the budget.Spec for a sub-agent dispatch from
// its four sources of truth: per-call arguments, per-role config, the
// role profile, and global subagent defaults. It is the single mapping
// between runner inputs and the budget package.
func budgetSpecFor(callIterations, callTurns int, profile subagent.RoleProfile, subCfg config.SubAgentConfig, role subagent.SubAgentRole) budget.Spec {
	rc := subCfg.Roles[string(role)]
	return budget.Spec{
		CallIterations:    callIterations,
		CallTurns:         callTurns,
		RoleMaxIterations: profile.MaxLoopCycles,
		RoleMinIterations: profile.MinLoopCycles,
		RoleMaxTurns:      profile.MaxToolTurns,
		RoleMinTurns:      profile.MinToolTurns,
		CfgMaxIterations:  rc.MaxLoopCycles,
		CfgMinIterations:  rc.MinLoopCycles,
		CfgMaxTurns:       rc.MaxToolTurns,
		CfgMinTurns:       rc.MinToolTurns,
		DefaultTurns:      subCfg.DefaultMaxToolTurns,
		DefaultMinTurns:   subCfg.DefaultMinToolTurns,
		HardCeiling:       budget.SchemaMaxIterations,
	}
}

// resolveSubAgentBudget resolves the effective iteration and turn
// budgets for a sub-agent dispatch in a single call so the two
// dimensions reconcile against each other (floors grow iterations
// rather than shrinking turns). Resolution lives in
// internal/agent/budget.
func resolveSubAgentBudget(callIterations, callTurns int, profile subagent.RoleProfile, subCfg config.SubAgentConfig, role subagent.SubAgentRole) budget.Budget {
	return budget.Resolve(budgetSpecFor(callIterations, callTurns, profile, subCfg, role))
}

// resolveOutputLimit picks the byte cap for sub-agent final output.
// Precedence: per-call override > role-specific config > opts fallback > default 50KB.
func resolveOutputLimit(callLimit int, subCfg config.SubAgentConfig, role subagent.SubAgentRole, optsDefault int) int {
	if callLimit > 0 {
		return callLimit
	}
	if rc, ok := subCfg.Roles[string(role)]; ok && rc.OutputLimit > 0 {
		return rc.OutputLimit
	}
	if optsDefault > 0 {
		return optsDefault
	}
	return 50 * 1024
}

func safeTruncateBytes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	if maxBytes <= 0 {
		return ""
	}
	i := maxBytes
	if i > len(s) {
		i = len(s)
	}
	for i > 0 && i < len(s) {
		if s[i] < 0x80 || s[i]&0xC0 == 0xC0 {
			break
		}
		i--
	}
	return s[:i]
}

// roleHasShell reports whether the role's tool list includes a shell
// (bash or powershell). An empty tool list means the role carries no
// tools at all — buildSubAgentRegistry registers nothing for it — so it
// has no shell either (#329). The legacy "empty means every tool"
// profile is gone: every shipped role file declares its tools.
func roleHasShell(tools []string) bool {
	for _, t := range tools {
		if t == "bash" || t == "powershell" {
			return true
		}
	}
	return false
}

func formatBytes(n int) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div := unit
	exp := 0
	for nn := n / unit; nn >= unit && exp < 3; nn /= unit {
		div *= unit
		exp++
	}
	prefix := []string{"KB", "MB", "GB"}[exp]
	return fmt.Sprintf("%.1f %s", float64(n)/float64(div), prefix)
}

// recordSubAgentUsage persists the child's cumulative usage as a trace fact.
// The orchestrator's session totals fold child usage in, so state.db exposes
// only the aggregate sub-agent share. This fact keeps the per-dispatch and
// per-role split available to benchmark metrics and trace readers.
func recordSubAgentUsage(ctx context.Context, store *shepherdtrace.SQLiteTraceStore, sessionID, role, model string, usage types.Usage) {
	if store == nil || sessionID == "" {
		return
	}
	payload := map[string]any{
		"role":                 role,
		"model":                model,
		"prompt_tokens":        usage.PromptTokens,
		"completion_tokens":    usage.CompletionTokens,
		"total_tokens":         usage.TotalTokens,
		"cached_prompt_tokens": usage.CacheTokens(),
	}
	_, err := store.Append(ctx, shepherdtrace.TrustedAppendContext, shepherdtrace.AppendBatch{
		AppendIntentID: sessionID + ":usage",
		Groups: []shepherdtrace.AppendGroup{{
			TraceOwnerID: sessionID,
			FactDrafts: []shepherdtrace.RecordDraft{{
				Mode:      shepherdtrace.Capture,
				SchemaRef: "yaah.subagent.usage.v1",
				KindLabel: "subagent:usage",
				Payload:   payload,
			}},
		}},
	})
	if err != nil {
		slog.Debug("subagent usage trace failed", "session", sessionID, "err", err)
	}
}

func subagentTraceProfile(ctx context.Context, tracePath, sessionID string) string {
	// Do not open the store unless it already exists: NewSQLiteTraceStore
	// creates the file, so probing speculatively would conjure an empty
	// store at the default path as a side effect of a failed dispatch.
	if _, err := os.Stat(tracePath); err != nil {
		return ""
	}
	store, err := shepherdtrace.NewSQLiteTraceStore(tracePath)
	if err != nil {
		return ""
	}
	// The whole read is deliberately cancel-free, not just the Close: this
	// profile exists to enrich a failed sub-agent, and the failure it
	// diagnoses is often the very cancellation that would abort the read.
	defer store.Close(context.WithoutCancel(ctx))

	slice, err := store.ReadOwnerPrefix(context.WithoutCancel(ctx), shepherdtrace.TrustedReadContext, sessionID, 99999, shepherdtrace.ModeBoth)
	if err != nil || len(slice.FactIDs()) == 0 {
		return ""
	}

	captureByParent := make(map[string]struct {
		success bool
		errMsg  string
	})
	for _, factID := range slice.FactIDs() {
		fact := slice.FactsByID[factID]
		if fact.GetEnvelope().Mode != shepherdtrace.Capture {
			continue
		}
		causedBy := fact.GetEnvelope().CausedByIDs
		if len(causedBy) == 0 {
			continue
		}
		parentID := causedBy[0]
		if _, exists := captureByParent[parentID]; exists {
			continue
		}
		rec, ok := fact.(shepherdtrace.Record)
		if !ok {
			continue
		}
		success, _ := rec.Body.Payload["success"].(bool)
		errMsg := ""
		if e, ok2 := rec.Body.Payload["error"].(string); ok2 {
			errMsg = e
		}
		captureByParent[parentID] = struct {
			success bool
			errMsg  string
		}{success, errMsg}
	}

	var lines []string
	seq := 0
	for _, factID := range slice.FactIDs() {
		fact := slice.FactsByID[factID]
		if fact.GetEnvelope().Mode != shepherdtrace.Declaration {
			continue
		}
		kind := fact.GetView().KindLabel
		if kind == "turn:created" || kind == "turn:started" || kind == "turn:completed" || kind == "turn:failed" {
			continue
		}
		seq++
		cap := captureByParent[factID]
		status := "ok"
		if cap.errMsg != "" {
			status = "error: " + cap.errMsg
		}
		lines = append(lines, fmt.Sprintf("  T%d %s  %s", seq, kind, status))
	}

	if len(lines) == 0 {
		return ""
	}
	return fmt.Sprintf("%d tool calls:\n%s", seq, strings.Join(lines, "\n"))
}

// recordConfinementFact appends the resolved jail placement to a sub-agent's
// scope (Plan 7 Phase 3), so a reader can tell a jailed run from an advisory one.
// Best-effort: a tracing failure must not fail the dispatch. It is a no-op when
// either argument is nil (tracing off, or no confinement to record).
func recordConfinementFact(ctx context.Context, scope *shepherd.Scope, conf *subAgentConfinement) {
	if scope == nil || conf == nil {
		return
	}
	if _, err := scope.AppendFact(ctx, "yaah.subagent.confinement.v1", "confinement", conf.payload()); err != nil {
		slog.Debug("confinement fact: append failed", "scope", scope.ID(), "err", err)
	}
}

// subAgentConfinement records how one dispatch resolved its jail placement, for
// the sub-agent's trace (Plan 7 Phase 3): a reader can tell a jailed run from an
// advisory one, and see the single writable root the kernel bounded it to.
type subAgentConfinement struct {
	Requested string // the configured agents.subagent.containment value
	Resolved  string // "paddock" (jailed) or "advisory" (host path)
	Backend   string // backend name when jailed (e.g. "landlock", "winacl")
	Tier      string // enforcement tier, e.g. "native-syscall-deny"
	Enforced  bool   // a usable backend will actually establish the jail
	Writable  string // the bound world — the sole writable root
	Network   string // the egress posture that rode the jail ("allow_all", …)
	Broker    int    // the pinned egress port, when the posture is "broker"
}

func (c *subAgentConfinement) payload() map[string]any {
	return map[string]any{
		"requested":     c.Requested,
		"resolved":      c.Resolved,
		"backend":       c.Backend,
		"tier":          c.Tier,
		"enforced":      c.Enforced,
		"writable_root": c.Writable,
		"network":       c.Network,
		"broker_port":   c.Broker,
	}
}

// resolveSubAgentConfinement builds the syscall-jailed workspace for a sub-agent
// workdir dispatch when agents.subagent.containment asks for one, and describes
// what it resolved to (for the trace). It returns a nil workspace to leave the
// host path. "" / "auto" (the default) jail on the platform backend (Landlock
// on Linux, winacl on Windows) and degrade to advisory elsewhere; "off" (or
// "none"/"advisory") mean no jail;
// "paddock" (alias "jail") fails closed. The writable root is the worktree — the
// sub-agent's bound world — so the kernel boundary and the run's own boundary
// agree.
func resolveSubAgentConfinement(pv *tools.PathValidator, cfg config.SubAgentConfig, workdir string, network containment.NetworkPolicy) (tools.Workspace, *subAgentConfinement, error) {
	requested := strings.TrimSpace(cfg.Containment)
	placement := containmentPlacement(requested)

	// Normalize the zero policy to the config default before anything reads it:
	// an unset caller means "no posture configured", and handing an empty mode to
	// the runtime would mean deny_all — a jail that refuses every connect because
	// nobody set a key. The broker port goes with the mode: a port on a non-broker
	// posture is a record saying two things at once.
	if network.Mode == "" {
		network.Mode = containment.NetAllowAll
	}
	if network.Mode != containment.NetBroker {
		network.BrokerPort = 0
	}

	// Availability, not mere presence: a backend that is present but unusable
	// (an LSM that is compiled in but disabled, say) means Exec will degrade.
	backend := jail.Backend()
	available := false
	if backend != nil {
		if ok, _ := backend.Available(); ok {
			available = true
		}
	}

	if placement == "" || workdir == "" {
		return nil, nil, nil
	}

	rec, confine := confinementRecord(requested, placement, available, backend, workdir, network)
	if !confine {
		// "auto" degrades visibly to advisory: the host path *is* advisory.
		return nil, rec, nil
	}

	// The write jail, with the configured egress posture. A restrictive posture
	// the host cannot enforce fails closed inside the backend, so it is surfaced
	// rather than silently downgraded.
	ws := tools.NewConfinedLocalWorkspace(pv, backend, placement, network)

	// Redirect housekeeping writes into the worktree (the one writable root):
	// HOME, TMPDIR, and the build caches. The scratch is scrubbed before the
	// workspace is sealed, so it never reaches a proposal's delta.
	//
	// A failure here fails the dispatch. The redirect is not decoration: without
	// it the jailed body's HOME/TMPDIR writes land outside the writable root and
	// are denied, so the run would fail confusingly — and dropping the jail
	// instead would silently lose containment. The refusal is therefore surfaced
	// rather than downgraded. (An operator who wants no jail sets
	// `containment: off`.)
	env, err := tools.ConfinementScratchEnv(workdir)
	if err != nil {
		return nil, nil, fmt.Errorf("confinement redirect for %s: %w", workdir, err)
	}
	env = append(env, brokerProxyEnv(network)...)
	return ws.WithEnv(env), rec, nil
}

// confinementRecord is the pure decision behind what a dispatch records, split
// out (like doctor's containmentCheck) so the two cases a live backend cannot
// produce are testable: a backend that is present but unusable with "auto"
// (which must record advisory, NOT the paddock it will not enter), and the same
// with "paddock" (which must record paddock with Enforced false — the dispatch
// refuses rather than running unconfined, so "advisory" would be a lie).
//
// confine reports whether a confined workspace should be built: true whenever
// the placement is paddock (so Exec can fail closed) or a usable backend exists.
func confinementRecord(requested string, placement confinement.Placement, available bool, backend containment.Backend, workdir string, network containment.NetworkPolicy) (*subAgentConfinement, bool) {
	if !available && placement != confinement.PlacementPaddock {
		// No writable root is claimed — nothing bounded it.
		return &subAgentConfinement{Requested: requested, Resolved: "advisory"}, false
	}
	rec := &subAgentConfinement{Requested: requested, Resolved: "paddock", Writable: workdir}
	if available && backend != nil {
		// An egress posture rides the jail, so it is recorded with it. An
		// advisory run has no jail to put a rule in, and its record says nothing
		// about the network rather than claiming one.
		rec.Network = networkMode(network)
		rec.Broker = network.BrokerPort
		rec.Enforced = true
		rec.Backend = backend.Name()
		rec.Tier = backend.EnforcementTier()
	}
	return rec, true
}

// SubAgentNetworkPolicy lowers agents.subagent.network to the confinement policy
// a confined dispatch carries. Exported because the composition root owns the
// broker and therefore knows its port, while the mapping belongs next to the
// code that applies it — two copies drifting is exactly how a config key quietly
// stops meaning what it says. The broker port is required for "broker": a policy
// that cannot be applied is not applied quietly.
func SubAgentNetworkPolicy(mode string, brokerPort int) (containment.NetworkPolicy, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "allow_all":
		return containment.NetworkPolicy{Mode: containment.NetAllowAll}, nil
	case "deny_all":
		return containment.NetworkPolicy{Mode: containment.NetDenyAll}, nil
	case "broker":
		if brokerPort <= 0 {
			return containment.NetworkPolicy{}, fmt.Errorf(
				"agents.subagent.network is %q but no egress broker is running", mode)
		}
		return containment.NetworkPolicy{Mode: containment.NetBroker, BrokerPort: brokerPort}, nil
	default:
		return containment.NetworkPolicy{}, fmt.Errorf(
			"agents.subagent.network %q is not one of: allow_all, deny_all, broker", mode)
	}
}

// restrictivePosture reports whether a policy denies or brokers egress — the
// modes whose rules a dispatch has to be jailed for. It reads the posture, not
// whether a jail was established: a refused jail still must not leave the
// host-network tools registered.
func restrictivePosture(network containment.NetworkPolicy) bool {
	mode := networkMode(network)
	return mode != "" && mode != string(containment.NetAllowAll)
}

// hostNetworkToolsIn lists the profile's tools that would reach the network from
// this process — omitted from a restrictive dispatch's registry, and named in its
// prompt so the omission is legible.
func hostNetworkToolsIn(profile subagent.RoleProfile) []string {
	var out []string
	for _, name := range profile.Tools {
		if tools.LeafToolUsesHostNetwork(name) {
			out = append(out, name)
		}
	}
	return out
}

// requireEnforceableEgress refuses a restrictive posture this host cannot apply.
//
// "allow_all" needs nothing. "deny_all" and "broker" are kernel rules: they need
// a usable backend AND its network support (Landlock ABI >= 4 on Linux; winacl
// has no network axis). Without both, the
// dispatch would either fail deep inside the jail with a kernel message, or — far
// worse on the advisory fallback — run with open egress while the config, the
// trace and the sub-agent's own prompt said otherwise.
func requireEnforceableEgress(available bool, backend containment.Backend, network containment.NetworkPolicy, placement confinement.Placement, workdir, requested string) error {
	mode := networkMode(network)
	if mode == string(containment.NetAllowAll) {
		return nil
	}
	// "This dispatch is not jailed at all" is the primary reason the posture
	// cannot be applied, and it is checked here rather than left to an early
	// return — a dropped posture is a rule the user believes is in force.
	if workdir == "" {
		return fmt.Errorf(
			"agents.subagent.network is %q, but this dispatch has no workdir to jail against, so the posture cannot be applied: "+
				"set network: allow_all to accept open egress", mode)
	}
	if placement == "" {
		configured := requested
		if configured == "" {
			configured = "off"
		}
		return fmt.Errorf(
			"agents.subagent.network is %q, which needs a jail, but agents.subagent.containment is %q: "+
				"set containment: auto (or paddock) to enforce it, or network: allow_all to accept open egress",
			mode, configured)
	}
	if placement == confinement.PlacementPaddock && !available {
		// paddock already fails closed at Exec; let that message stand.
		return nil
	}
	if !available {
		return fmt.Errorf(
			"agents.subagent.network is %q, which needs a jail, but this host has no usable syscall-jail backend: "+
				"the dispatch would run with open egress. Set network: allow_all to accept that, or run where confinement is available",
			mode)
	}
	if !backend.SupportsNetwork(network.Mode) {
		return fmt.Errorf(
			"agents.subagent.network is %q, which this host's syscall jail cannot enforce (no network-rule support on this backend: Landlock needs ABI >= 4 on Linux, winacl has none): "+
				"set network: allow_all, or remove the jail requirement", mode)
	}
	return nil
}

// brokerProxyEnv routes well-behaved clients through the broker. A pin without
// routing is half a control: the jail would permit only the broker's port while
// every ordinary HTTPS fetch still went straight to :443 and was refused — so the
// broker is advertised the way HTTP proxies are advertised, and clients that
// honour the convention (curl, git-http, npm, pip, Go's net/http) reach it
// without being told twice.
func brokerProxyEnv(network containment.NetworkPolicy) []string {
	if network.Mode != containment.NetBroker || network.BrokerPort <= 0 {
		return nil
	}
	addr := fmt.Sprintf("http://127.0.0.1:%d", network.BrokerPort)
	return []string{
		"HTTP_PROXY=" + addr,
		"HTTPS_PROXY=" + addr,
		"http_proxy=" + addr,
		"https_proxy=" + addr,
		// The broker itself is on loopback; never proxy the loopback hop.
		"NO_PROXY=localhost,127.0.0.1",
		"no_proxy=localhost,127.0.0.1",
	}
}

// networkMode names a policy's posture for the trace and the prompt. It is read
// after resolveSubAgentConfinement has normalized an unset mode, so it reports the
// posture actually applied rather than a default nobody chose.
func networkMode(np containment.NetworkPolicy) string {
	if np.Mode == "" {
		return string(containment.NetAllowAll)
	}
	return string(np.Mode)
}

// containmentPlacement maps agents.subagent.containment to a placement, or "" for
// "run on the host" (the explicit opt-out: "off"/"none"/"advisory"). An unset
// setting is the default and means auto.
func containmentPlacement(v string) confinement.Placement {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "auto":
		// "auto" is the default (Plan 7 Phase 3): jail where the host can, and
		// degrade visibly to advisory where it cannot. The opt-out is explicit
		// — "off" (or "none"/"advisory") — so the unconfined host path is a
		// choice, not an omission.
		return confinement.PlacementAuto
	case "paddock", "jail":
		return confinement.PlacementPaddock
	default: // "off", "none", "advisory"
		return ""
	}
}
