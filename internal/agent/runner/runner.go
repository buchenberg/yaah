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
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
	shepherdsandbox "github.com/buchenberg/shepherd-kernel-go/sandbox"
	shepherdtrace "github.com/buchenberg/shepherd-kernel-go/trace"
	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/agent/budget"
	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/agent/supervision"
	"github.com/buchenberg/yaah/internal/config"
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

		sysPrompt := buildSubAgentSysPrompt(opts, role, profile, jsonMode)

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
		if mgr := tools.SharedScopeManager; mgr != nil {
			subScope, _ = createSubAgentScope(mgr, opts, subTraceID, params.Workdir)
		}

		// Supervision bridge (adoption plan A2): the run registers its
		// steering channel under the scope ID, so the supervisor tool's
		// inject reaches this loop between turns instead of only
		// recording a trace fact. The deferred unregister keeps the map
		// honest on every exit path — normal, error, and cancellation;
		// inject into a finished run then reports "no active run"
		// rather than silently vanishing.
		var steerCh chan string
		if subScope != nil && tools.SharedRunRegistry != nil {
			steerCh = make(chan string, supervision.SteerBuffer)
			tools.SharedRunRegistry.Register(subScope.ID(), steerCh)
			defer tools.SharedRunRegistry.Unregister(subScope.ID())
		}

		// Per-turn checkpointing is enabled per role (subagent.roles.<name>.
		// turn_checkpoints). When active, the checkpointer lives on this
		// sub-agent's own scope so turn rewinds never touch the supervised
		// tool's attempt-level checkpoints.
		var turnCk agent.TurnCheckpointer
		if opts.subCfg.Roles[string(role)].TurnCheckpoints && subScope != nil && subScope.Sandbox() != nil {
			turnCk = NewShepherdTurnCheckpointer(tools.SharedScopeManager, subScope.ID(), subScope.Sandbox())
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
			SteerCh:                steerCh,

			TurnCheckpointer:      turnCk,
			TurnCheckpointEnabled: turnCk != nil,
			TurnCheckpointMax:     opts.defaults.TurnCheckpointMax,
			MaxTurnRestores:       opts.defaults.MaxTurnRestores,

			InitialMessages: params.SeedMessages,
		})

		result, runErr := subLoop.Run(ctx, prompt)

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
func buildSubAgentSysPrompt(opts taskRunnerOpts, role subagent.SubAgentRole, profile subagent.RoleProfile, jsonMode bool) string {
	cwd, _ := os.Getwd()

	// The environment header must advertise the directory the sub-agent's
	// tools actually resolve against. In an isolated session that is the
	// sandbox workdir, not the host cwd — the header's working-directory
	// line is what shell-cd'ing and relative paths key off, and a sandboxed
	// tool rejects the advertised host path as an escape. The shell line
	// comes from the workspace too: the container's shell is sh, not the
	// host's bash/powershell.
	envOS, envArch, envShell, envCwd := runtime.GOOS, runtime.GOARCH, "", cwd
	if opts.workspace != nil {
		envShell, _ = opts.workspace.Shell()
		envCwd = opts.workspace.WorkDir()
	} else {
		if runtime.GOOS == "windows" {
			envShell = "powershell (pwsh 7+ or Windows PowerShell)"
		} else {
			envShell = "bash"
		}
	}
	sysPrompt := prompts.EnvironmentHeader(envOS, envArch, envShell, envCwd) + "\n\n" + opts.systemPrompt

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
		return shepherdsandbox.NewWorktreeSandbox(repoPath, workdir)
	}
	return shepherdsandbox.NewLocalGitSandbox(repoPath)
}

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
	var sb shepherdsandbox.Sandbox
	if !isolated {
		if repoPath == "" {
			repoPath, _ = os.Getwd()
		}
		if repoPath != "" {
			candidate := shepherdsandbox.NewLocalGitSandbox(repoPath)
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
			return mgr.ForkIsolated(opts.rootScopeID, subTraceID, nil, shepherdsandbox.NewWorktreeSandbox(repoPath, workdir))
		}
	}
	return mgr.Fork(opts.rootScopeID, subTraceID, nil)
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
	if !isolated && opts.pathValidator != nil {
		// An isolated sub-agent gets a validator rooted at its own checkout, so
		// its file tools are contained there and its shell runs there. Policy
		// (deny patterns, home-access, approval callback) is inherited; the
		// derived validator starts with a fresh approval cache.
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
// (bash or powershell). Empty tool lists (legacy full-tool profile) are
// considered to have all tools, including both shells.
func roleHasShell(tools []string) bool {
	if len(tools) == 0 {
		return true
	}
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
