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
//
// The package is split by concern: this file holds the delegate entry point
// (DelegateToolOpts/NewDelegateTool); task_runner.go the per-dispatch task
// runner closure; registry.go the per-role tool registry; subagent_prompt.go
// the system prompt; scope.go the scope tree; budget.go the timeout and
// iteration/turn adapters; usage.go the trace facts; confinement.go the
// jail/egress posture; delegate_roles.go role discovery; checkpoint.go and
// role_resolver.go their own adapters.
package runner

import (
	"log/slog"
	"os"

	goatherd "github.com/buchenberg/goatherd-runtime"
	"github.com/buchenberg/goatherd-runtime/sandbox/confinement"
	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/agent/supervision"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/tools"
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
	ScopeManager *goatherd.ScopeManager

	// MaxDispatchScopes caps concurrently ACTIVE dispatch scopes; 0 =
	// jobs.DefaultMaxDispatchScopes (yaah-95ri.4).
	MaxDispatchScopes int

	// ResumedScopes is the scope-ID set startup recovery re-admitted from
	// the trace (yaah-95ri.7). They hold no loop in this process, so the
	// dispatch cap must not count them (jobs.DispatchScopeCount) — dead
	// work from a crashed run must never refuse a live dispatch. Nil when
	// no recovery adopted anything.
	ResumedScopes map[string]bool

	// TraceStore is the session's Shepherd trace store (plan 07, H4):
	// the sub-agent loops' shepherd_trace middleware writes through it.
	// Nil when tracing is off.
	TraceStore pipeline.TraceSink

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
	SubNetwork confinement.NetworkPolicy

	// DispatchWorlds is the per-dispatch container setup (yaah-ihfh.3). Non-nil
	// only in a host session that can offer a container world: the retained
	// runtime then creates one per report dispatch, and the runner runs the
	// sub-agent's tools inside it. Nil leaves every dispatch on the worktree
	// (or the session's own substrate).
	DispatchWorlds *tools.DispatchWorlds
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
		maxDispatchScopes:     opts.MaxDispatchScopes,
		resumedScopes:         opts.ResumedScopes,
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
		// One proposal registry per constructed runtime, so isolation is a
		// property of construction: any composition path that builds the tool
		// gets its own registry, and startRetainedRun refuses one without it
		// (yaah-yox). The caller hands this same registry to ProposalTool.
		//
		// Container proposals persist their manifest and host basis under
		// <repo>/.yaah/proposals. Rehydrate this session's entries so a
		// restart does not strand a sealed proposal; bad artifacts are
		// reported and skipped rather than guessed at.
		repoPath := opts.RetainedRepoPath
		if repoPath == "" {
			repoPath, _ = os.Getwd()
		}
		proposals, warnings := tools.LoadProposalRegistry(repoPath, opts.ScopeManager, opts.RootScopeID, opts.SessionID)
		for _, warning := range warnings {
			slog.Warn("proposal rehydration", "warning", warning)
		}
		tool.Retained = &tools.RetainedRuntime{
			Runner:            runnerFn,
			RepoPath:          repoPath,
			ScopeManager:      opts.ScopeManager,
			MaxDispatchScopes: opts.MaxDispatchScopes,
			ResumedScopes:     opts.ResumedScopes,
			RootScopeID:       opts.RootScopeID,
			SessionID:         opts.SessionID,
			WorktreeRoot:      opts.RetainedWorktreeRoot,
			WorktreeBootstrap: opts.RetainedBootstrap,
			Proposals:         proposals,
			World:             opts.SubCfg.World,
			WorldForRole: func(role string) string {
				if rc, ok := opts.SubCfg.Roles[role]; ok {
					return rc.World
				}
				return ""
			},
			Containment:  opts.SubCfg.Containment,
			Network:      opts.SubCfg.Network,
			NetworkHosts: append([]string(nil), opts.SubCfg.NetworkHosts...),
			Grants:       opts.SubCfg.Grants,
			GrantsForRole: func(role string) []config.GrantConfig {
				if rc, ok := opts.SubCfg.Roles[role]; ok {
					return rc.Grants
				}
				return nil
			},
			// A per-dispatch container world is offerable only where the
			// composition root wired its setup; the ladder then decides per
			// lane what may actually use it (yaah-ihfh.3).
			ContainerWorlds: opts.DispatchWorlds != nil,
			DispatchWorlds:  opts.DispatchWorlds,
		}
		// Before the runtime is copied into the dispatch paths: every copy
		// must share one in-flight live set (yaah-95ri.6).
		tool.Retained.EnsureInFlight()
	}
	return tool
}
