// Package runner wires sub-agent execution: it builds the per-role tool
// registries, resolves provider/model/timeout/iteration budgets, assembles
// the sub-agent system prompt, and constructs the top-level TaskTool that
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

	shepherd "github.com/buchenberg/shepherd-kernel-go"
	shepherdsandbox "github.com/buchenberg/shepherd-kernel-go/sandbox"
	shepherdgit "github.com/buchenberg/shepherd-kernel-go/sandbox/git"
	shepherdtrace "github.com/buchenberg/shepherd-kernel-go/trace"
	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/agent/budget"
	"github.com/buchenberg/yaah/internal/agent/llm"
	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/agent/supervision"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/gitsandbox"
	"github.com/buchenberg/yaah/internal/jobs"
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/prompts"
	"github.com/buchenberg/yaah/internal/tools"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// TaskToolOpts carries everything NewTaskTool needs. A struct beats a
// 20-parameter constructor: new sub-agent knobs no longer grow the
// signature, and call sites read as keyed literals (finding C3).
type TaskToolOpts struct {
	Provider              agent.Provider
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
}

// NewTaskTool builds the planner's TaskTool: it resolves the advertised
// role names/descriptions and returns a TaskTool whose Runner is a
// makeTaskRunner closure bounded to one level of nesting.
func NewTaskTool(opts TaskToolOpts) *tools.TaskTool {
	// Sub-agent spawning depth is hard-coded at 1: the top-level agent
	// can spawn one level of sub-agents; sub-agents cannot spawn further
	// sub-agents (remainingDepth reaches 0).
	depth := 1
	return &tools.TaskTool{
		Runner: makeTaskRunner(taskRunnerOpts{
			provider:              opts.Provider,
			systemPrompt:          opts.SystemPrompt,
			modelName:             opts.ModelName,
			db:                    opts.DB,
			parentSession:         opts.SessionID,
			subCfg:                opts.SubCfg,
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
		}, depth),
		ResolveTimeout:   subAgentTimeoutResolver(opts.SubCfg),
		RoleNames:        opts.RoleNames,
		RoleDescriptions: RoleDescriptionsFor(opts.RoleNames),
		Tracker:          opts.Tracker,
	}
}

// RoleDescriptionsFor maps each role name to a one-line description (or
// specialty fallback) drawn from the role registry. Used to populate the
// role parameter descriptions of the spawn_subagent and supervised_task
// schemas so the orchestrator can choose roles without calling
// list_subagents.
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
	provider      agent.Provider
	systemPrompt  string
	modelName     string
	db            *memory.DB
	parentSession string
	subCfg        config.SubAgentConfig

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

		subReg, rerr := buildSubAgentRegistry(opts, profile, remainingDepth, params.Workdir)
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

		sysPrompt := buildSubAgentSysPrompt(opts, role, profile, jsonMode, params.Workdir)

		// Record the role's directives on the sub-agent span so traces
		// show which policy statements this child received (if any).
		if ds := opts.subCfg.Roles[string(role)].Directives; len(ds) > 0 {
			if span := trace.SpanFromContext(ctx); span.IsRecording() {
				span.SetAttributes(attribute.String("subagent.directives", strings.Join(ds, " | ")))
			}
		}

		subProvider := opts.subAgentProvider
		subModel := opts.subAgentModel
		if rc, ok := opts.subCfg.Roles[string(role)]; ok {
			if rc.Provider != "" {
				if opts.resolveProviderByName != nil {
					subProvider = opts.resolveProviderByName(opts.providerMap, rc.Provider)
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
			MaxLoopCycles:          maxIter,
			MaxToolTurns:           maxTurns,
			BudgetIterationsSource: b.IterationsSource.String(),
			BudgetTurnsSource:      b.TurnsSource.String(),
			MaxRetries:             opts.defaults.MaxRetries,
			RetryBackoffSecs:       opts.defaults.RetryBackoffSecs,
			MaxToolConcurrency:     opts.defaults.MaxToolConcurrency,
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

			InitialMessages: params.SeedMessages,
		})

		result, runErr := subLoop.Run(ctx, prompt)

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

		// Hand the final conversation to the spawner's capture pointer (set
		// via jobs.WithConversationCapture) so supervised review sessions can
		// seed the next dispatch from this history.
		jobs.WriteConversationCapture(ctx, subLoop.State.Messages)

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
		// can see exactly which tool calls led to the failure.
		if runErr != nil && opts.defaults.ShepherdTraceDir != "" {
			tracePath := filepath.Join(opts.defaults.ShepherdTraceDir, "trace.sqlite")
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
func buildSubAgentSysPrompt(opts taskRunnerOpts, role subagent.SubAgentRole, profile subagent.RoleProfile, jsonMode bool, workdir string) string {
	cwd, _ := os.Getwd()

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
	if !roleHasShell(profile.Tools) {
		envShell = ""
	}

	// A zero-tool role gets the no-tools identity variant: the directive
	// sits before the identity text so it lands early in the prompt, and
	// the tool-batching cardinal rule and named-tool guidance disappear.
	base := opts.systemPrompt
	if len(profile.Tools) == 0 {
		base = prompts.InjectNoToolsIdentity(base)
	}
	sysPrompt := prompts.EnvironmentHeader(envOS, envArch, envShell, envCwd) + "\n\n" + base

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

	if roleHasShell(profile.Tools) {
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
			// the header above already advertised. The two statements
			// must agree, or the model flips between them.
			shell, _ = opts.workspace.Shell()
			otherShell = "bash or powershell — only " + shell + " is guaranteed in the container"
		}
		sysPrompt += "\n\n## Available Shell\n"
		sysPrompt += fmt.Sprintf("Your environment only has %s available. ", shell)
		sysPrompt += fmt.Sprintf("Use %s for ALL command execution. ", shell)
		sysPrompt += fmt.Sprintf("There is no %s.", otherShell)
	}

	return sysPrompt
}

// subAgentTimeoutResolver returns a TaskTool timeout resolver that folds
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
	repoPath := opts.defaults.SupervisedRepoPath
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
		repoPath := opts.defaults.SupervisedRepoPath
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
// remainingDepth > 0, a nested TaskTool is registered so the sub-agent
// can spawn further workers. When remainingDepth == 0 the task tool is
// omitted entirely.
//
// An isolated session (opts.workspace set) installs the session's sandbox
// workspace — the sub-agent inherits the container (decision D2) — and
// skips any host-only tool the profile names; a SetWorkspace refusal
// surfaces as an error rather than a silently host-side sub-agent.
func buildSubAgentRegistry(opts taskRunnerOpts, profile subagent.RoleProfile, remainingDepth int, workdir string) (*tools.Registry, error) {
	resolveTimeout := subAgentTimeoutResolver(opts.subCfg)
	registerTask := func(reg *tools.Registry) {
		reg.Register(&tools.TaskTool{
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

	reg := tools.NewEmptyRegistry()
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
	}
	for _, name := range profile.Tools {
		if name == "spawn_subagent" {
			if remainingDepth > 0 {
				registerTask(reg)
			}
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
			return nil, fmt.Errorf("isolated sub-agent workspace: %w", serr)
		}
	}
	return reg, nil
}

// resolveSubAgentTimeout picks the effective default timeout for a
// sub-agent TaskTool. Precedence: per-call override (handled by the
// TaskTool itself) > role-specific config > role profile default >
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

func subagentTraceProfile(ctx context.Context, tracePath, sessionID string) string {
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
