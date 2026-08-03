package yaah

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/prompts"
	"github.com/buchenberg/yaah/internal/tools"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

func newTaskTool(provider agent.Provider, systemPrompt, modelName string, db *memory.DB, sessionID string, subAgentProvider agent.Provider, subAgentModel string, subCfg config.SubAgentConfig, roleNames []string, otelEnabled bool, otelVerbose bool, tracker *tools.ConflictTracker, estimateFactor float64, subContextWindow int, outputLimit int, providerMap map[string]config.Provider, defaults config.Defaults, parentPermissionRules []pipeline.PermissionRule) *tools.TaskTool {
	// Sub-agent spawning depth is hard-coded at 1: the top-level agent
	// can spawn one level of sub-agents; sub-agents cannot spawn further
	// sub-agents (remainingDepth reaches 0).
	depth := 1
	roleDescs := make(map[string]string, len(roleNames))
	for _, name := range roleNames {
		p := subagent.RoleProfileFor(subagent.SubAgentRole(name))
		if p.Description != "" {
			roleDescs[name] = p.Description
		} else if p.Specialty != "" {
			roleDescs[name] = p.Specialty
		}
	}
	return &tools.TaskTool{
		Runner: makeTaskRunner(taskRunnerOpts{
			provider:              provider,
			systemPrompt:          systemPrompt,
			modelName:             modelName,
			db:                    db,
			parentSession:         sessionID,
			subCfg:                subCfg,
			subAgentProvider:      subAgentProvider,
			subAgentModel:         subAgentModel,
			OtelEnabled:           otelEnabled,
			OtelVerbose:           otelVerbose,
			tracker:               tracker,
			estimateFactor:        estimateFactor,
			subContextWindow:      subContextWindow,
			outputLimit:           outputLimit,
			providerMap:           providerMap,
			defaults:              defaults,
			parentPermissionRules: parentPermissionRules,
		}, depth),
		ResolveTimeout:   subAgentTimeoutResolver(subCfg),
		RoleNames:        roleNames,
		RoleDescriptions: roleDescs,
		Tracker:          tracker,
	}
}

// builtinRoleFiles reads the embedded roles/*.md files shipped in the
// binary and returns them keyed by file name (e.g. "worker.md").
func builtinRoleFiles() map[string][]byte {
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

// roleSearchPaths returns directories to scan for user-defined role
// definitions. Mirrors the skill search hierarchy: project-level
// (walked up from cwd) then user-level (~/.agents/roles/).
func roleSearchPaths(cwd string) []string {
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
// (planner → worker) inherit the same provider, prompt base, and config.
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
}

// subAgentSeq guarantees unique sub-session IDs across concurrent
// goroutines without relying on wall-clock resolution.
var subAgentSeq atomic.Int64

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

		subReg := buildSubAgentRegistry(opts, profile, remainingDepth)

		maxIter := resolveSubAgentIterations(params.MaxIterations, profile, opts.subCfg, role)
		maxTurns := resolveSubAgentTurns(params.MaxTurns, profile, opts.subCfg, role, maxIter)

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
				subProvider = resolveProviderByName(opts.providerMap, rc.Provider)
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

		subLoop := agent.NewSubAgentLoop(subProvider, subReg, subModel, sysPrompt, agent.SubAgentConfig{
			MaxIterations:      maxIter,
			MaxTurns:           maxTurns,
			MaxRetries:         opts.defaults.MaxRetries,
			RetryBackoffSecs:   opts.defaults.RetryBackoffSecs,
			MaxToolConcurrency: opts.defaults.MaxToolConcurrency,
			JSONMode:           jsonMode,
			ToolResultMaxLines: opts.defaults.ToolResultMaxLines,
			ToolResultMaxBytes: opts.defaults.ToolResultMaxBytes,
			PruneProtectTokens: opts.defaults.PruneProtectTokens,
			PruneMinReclaim:    opts.defaults.PruneMinReclaim,
			PruneMinTurns:      opts.defaults.PruneMinTurns,
			PermissionRules:    opts.parentPermissionRules,
			ContextWindow:      effectiveCW,
			OtelEnabled:        opts.OtelEnabled,
			OtelVerbose:        opts.OtelVerbose,
		})

		result, runErr := subLoop.Run(ctx, prompt)

		// Whitespace-only output is a silent failure (observed when a
		// forced final turn degenerates after tools are stripped). Turn
		// it into an explicit error so the orchestrator can retry with
		// a narrower task instead of seeing an empty result.
		if runErr == nil && strings.TrimSpace(result) == "" {
			runErr = fmt.Errorf("sub-agent %s produced no output — its final turn returned empty content (likely hit its turn cap mid-task; consider raising max_turns)", role)
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

		tools.AddSubAgentUsage(ctx, subLoop.TotalTokens)

		if span := trace.SpanFromContext(ctx); span.IsRecording() {
			span.SetAttributes(
				attribute.Int("subagent.prompt_tokens", subLoop.TotalTokens.PromptTokens),
				attribute.Int("subagent.completion_tokens", subLoop.TotalTokens.CompletionTokens),
				attribute.String("subagent.model", subModel),
			)
		}

		return result, runErr
	}
}

// buildSubAgentSysPrompt assembles the full system prompt for a sub-agent
// loop. Section order: environment header, identity-derived base prompt,
// per-role directives (from agent.subagent.roles.<role>.directives), role
// guidance, response contract, escalation format, and shell anchoring.
// Directives sit immediately after the base prompt so they stay prominent
// instead of being buried behind role guidance and format blocks.
func buildSubAgentSysPrompt(opts taskRunnerOpts, role subagent.SubAgentRole, profile subagent.RoleProfile, jsonMode bool) string {
	cwd, _ := os.Getwd()
	sysPrompt := prompts.DetectEnvironment(cwd) + "\n\n" + opts.systemPrompt

	if ds := opts.subCfg.Roles[string(role)].Directives; len(ds) > 0 {
		sysPrompt += "\n\n" + prompts.FormatDirectives(ds)
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

// buildSubAgentRegistry builds the tool registry for a sub-agent from
// its role profile. If the profile includes the task tool and
// remainingDepth > 0, a nested TaskTool is registered so the sub-agent
// can spawn further workers. When remainingDepth == 0 the task tool is
// omitted entirely.
func buildSubAgentRegistry(opts taskRunnerOpts, profile subagent.RoleProfile, remainingDepth int) *tools.Registry {
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
	for _, name := range profile.Tools {
		if name == "spawn_subagent" {
			if remainingDepth > 0 {
				registerTask(reg)
			}
			continue
		}
		registerLeaf(reg, name)
	}
	return reg
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

// resolveSubAgentIterations picks the iteration cap for a sub-agent Loop.
// Precedence: per-call override > role-specific config > role profile
// default > legacy hardcoded default. The role profile's MaxIterations
// acts as a ceiling only for per-call overrides (so a task call cannot
// neutralize the role's cap). Config-level overrides are authoritative
// and bypass the ceiling.
func resolveSubAgentIterations(callMax int, profile subagent.RoleProfile, subCfg config.SubAgentConfig, role subagent.SubAgentRole) int {
	var v int
	switch {
	case callMax > 0:
		v = callMax
		if profile.MaxIterations > 0 && v > profile.MaxIterations {
			v = profile.MaxIterations
		}
	case subCfg.Roles[string(role)].MaxIterations > 0:
		v = subCfg.Roles[string(role)].MaxIterations
	case profile.MaxIterations > 0:
		v = profile.MaxIterations
	default:
		v = subagent.RoleProfileFor(role).MaxIterations
		if v <= 0 {
			v = 25
		}
	}
	return v
}

// resolveSubAgentTurns picks the soft turn cap for a sub-agent Loop.
// Precedence: per-call override > role-specific config > role profile
// default > config-level default > hardcoded floor.
// The result is clamped so it never reaches the MaxIterations ceiling,
// guaranteeing at least one iteration for the forced-text turn.
func resolveSubAgentTurns(
	callMax int,
	profile subagent.RoleProfile,
	subCfg config.SubAgentConfig,
	role subagent.SubAgentRole,
	maxIter int,
) int {
	var v int
	switch {
	case callMax > 0:
		v = callMax
	case subCfg.Roles[string(role)].MaxTurns > 0:
		v = subCfg.Roles[string(role)].MaxTurns
	case profile.MaxTurns > 0:
		v = profile.MaxTurns
	case subCfg.DefaultMaxTurns > 0:
		v = subCfg.DefaultMaxTurns
	default:
		v = 3
	}
	if maxIter > 0 && v >= maxIter {
		v = maxIter - 1
	}
	if v < 1 {
		v = 1
	}
	return v
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

// resolveSubAgentConcurrency picks the max concurrent sub-agent spawns for a
// sub-loop. Precedence: per-role override > global config > default of 3.
func resolveSubAgentConcurrency(subCfg config.SubAgentConfig, role subagent.SubAgentRole) int {
	if rc, ok := subCfg.Roles[string(role)]; ok && rc.MaxConcurrency > 0 {
		return rc.MaxConcurrency
	}
	if subCfg.MaxConcurrency > 0 {
		return subCfg.MaxConcurrency
	}
	return 3
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
