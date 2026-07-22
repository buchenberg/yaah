package yaah

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/prompts"
	"github.com/buchenberg/yaah/internal/tools"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

func newTaskTool(provider agent.Provider, systemPrompt, modelName string, db *memory.DB, sessionID string, subAgentProvider agent.Provider, subAgentModel string, subCfg config.SubAgentConfig, roleNames []string, otelEnabled bool, otelVerbose bool, tracker *tools.ConflictTracker) *tools.TaskTool {
	// Map a config "0 = unlimited" MaxDepth to a sentinel so the
	// structural nesting decrement in makeTaskRunner does not disable
	// spawning for an "unlimited" setting.
	depth := subCfg.MaxDepth
	if depth <= 0 {
		depth = math.MaxInt32
	}
	return &tools.TaskTool{
		Runner: makeTaskRunner(taskRunnerOpts{
			provider:         provider,
			systemPrompt:     systemPrompt,
			modelName:        modelName,
			db:               db,
			parentSession:    sessionID,
			subCfg:           subCfg,
			subAgentProvider: subAgentProvider,
			subAgentModel:    subAgentModel,
			SubToolCallback:  subToolDisplay,
			OtelEnabled:      otelEnabled,
			OtelVerbose:      otelVerbose,
			tracker:          tracker,
		}, depth),
		ResolveTimeout: subAgentTimeoutResolver(subCfg),
		RoleNames:      roleNames,
		Tracker:        tracker,
	}
}

// subToolDisplay prints sub-agent tool calls indented under the
// parent's sub-agent banner so they are visually distinct.
func subToolDisplay(info agent.ToolInfo) {
	if info.Duration == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "    tool: %s", Bold(info.Name))
	if info.Args != "" {
		args := info.Args
		if len(args) > 40 {
			args = args[:37] + "..."
		}
		fmt.Fprintf(os.Stderr, "(%s)", Dim(args))
	}
	fmt.Fprintf(os.Stderr, " (%s)\n", Dim(formatDuration(info.Duration)))
	if info.Error != "" {
		fmt.Fprintf(os.Stderr, "      %s\n", replYellow("error: "+info.Error))
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

	// SubToolCallback is set on the sub-loop's OnTool so sub-agent
	// tool calls can be rendered indented in the CLI.
	SubToolCallback agent.ToolCallback

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
}

// subAgentSeq guarantees unique sub-session IDs across concurrent
// goroutines without relying on wall-clock resolution.
var subAgentSeq atomic.Int64

// subagentEnvironmentHeader returns a concise environment block that
// anchors shell choice for sub-agents. It is prepended to every sub-agent
// system prompt so the model sees OS/shell info before role guidance.
func subagentEnvironmentHeader() string {
	shell := "bash"
	if runtime.GOOS == "windows" {
		shell = "powershell"
	}
	cwd, _ := os.Getwd()
	return fmt.Sprintf(
		"## Environment\nOS: %s/%s. Default shell: %s. Use %s for all shell commands. Working directory: %s.",
		runtime.GOOS, runtime.GOARCH, shell, shell, cwd,
	)
}

// makeTaskRunner creates a sub-agent runner that honours roles, timeouts,
// iteration caps, and nesting depth.
//
// remainingDepth bounds how many levels of nested task calls the
// returned runner may itself issue. When it reaches zero the task tool
// is omitted from the sub-loop's registry, so nesting is bounded
// structurally without relying on middleware alone. A zero/negative
// config MaxDepth is mapped to a sentinel so "unlimited" is preserved.
func makeTaskRunner(opts taskRunnerOpts, remainingDepth int) tools.TaskRunner {
	return func(ctx context.Context, prompt string, params tools.SubAgentParams) (string, error) {
		role := subagent.SubAgentRole(params.Role)
		profile := subagent.RoleProfileFor(role)

		subReg := buildSubAgentRegistry(opts, profile, remainingDepth)

		maxIter := resolveSubAgentIterations(params.MaxIterations, profile, opts.subCfg, role)
		sysPrompt := subagentEnvironmentHeader() + "\n\n" + opts.systemPrompt
		if g := subagent.RoleGuidance(role); g != "" {
			if sysPrompt != "" {
				sysPrompt += "\n\n"
			}
			sysPrompt += g
		}
		if profile.Contract.Heading != "" && len(profile.Contract.Fields) > 0 {
			var b strings.Builder
			b.WriteString("\n\n## Response contract\n\n")
			b.WriteString("Always end your response with a structured block:\n\n```\n")
			b.WriteString(profile.Contract.Heading + "\n")
			for _, f := range profile.Contract.Fields {
				b.WriteString("- **" + f + "**: <value>\n")
			}
			b.WriteString("```")
			sysPrompt += b.String()
		}

		// Persist the sub-agent transcript under a child session. The ID
		// combines wall-clock time with a process-wide atomic counter so
		// parallel task calls cannot collide; if session creation fails
		// the sub-agent runs in-memory rather than polluting the parent
		// transcript.
		subDB := opts.db
		subSessionID := opts.parentSession
		if opts.db != nil {
			subSessionID = fmt.Sprintf("%s-sub-%d-%d", opts.parentSession, time.Now().UnixNano(), subAgentSeq.Add(1))
			cwd, _ := os.Getwd()
			if err := opts.db.CreateSession(memory.Session{
				ID:        subSessionID,
				StartedAt: time.Now().Unix(),
				CWD:       cwd,
				Model:     opts.modelName,
			}); err != nil {
				subDB = nil
			}
		}

		subProvider := opts.subAgentProvider
		if subProvider == nil {
			subProvider = opts.provider
		}
		subModel := opts.subAgentModel
		if subModel == "" {
			subModel = opts.modelName
		}

		subLoop := &agent.Loop{
			Provider:               subProvider,
			Registry:               subReg,
			SystemPrompt:           sysPrompt,
			Model:                  subModel,
			MaxIterations:          maxIter,
			MaxRetries:             2,
			ApprovalMode:           "allow",
			DB:                     subDB,
			SessionID:              subSessionID,
			MaxSubAgentDepth:       opts.subCfg.MaxDepth,
			MaxSubAgentConcurrency: opts.subCfg.MaxConcurrency,
			MaxSubAgentDepthByRole: subAgentDepthByRole(opts.subCfg),
			OtelEnabled:            opts.OtelEnabled,
			OtelVerbose:            opts.OtelVerbose,
			OnTool:                 opts.SubToolCallback,
		}

		result, runErr := subLoop.Run(ctx, prompt)

		tools.WriteSubAgentModel(ctx, subModel)

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

// subAgentTimeoutResolver returns a TaskTool timeout resolver that folds
// in the actual per-call role, so role-profile and per-role config
// timeouts are honoured rather than a single construction-time default.
func subAgentTimeoutResolver(subCfg config.SubAgentConfig) func(tools.SubAgentParams) time.Duration {
	return func(p tools.SubAgentParams) time.Duration {
		return resolveSubAgentTimeout(0, subCfg, subagent.SubAgentRole(p.Role))
	}
}

// subAgentDepthByRole builds the per-role depth cap map from role
// profile defaults, overridden by per-role config. Roles absent from
// the map fall back to the global MaxDepth in the middleware.
func subAgentDepthByRole(subCfg config.SubAgentConfig) map[subagent.SubAgentRole]int {
	out := make(map[subagent.SubAgentRole]int)
	for _, name := range subagent.Names() {
		if d := subagent.RoleProfileFor(name).MaxDepth; d > 0 {
			out[name] = d
		}
	}
	for name, rc := range subCfg.Roles {
		if rc.MaxDepth > 0 {
			out[subagent.SubAgentRole(name)] = rc.MaxDepth
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// buildSubAgentRegistry constructs a tool registry for the given role
// profile. If the profile includes the task tool and remainingDepth > 0,
// a nested TaskTool is registered so the sub-agent can spawn further
// workers. When remainingDepth == 0 the task tool is omitted entirely.
//
// The RoleDefault profile (empty Tools) falls back to the full built-in
// tool set to preserve the legacy task tool behaviour.
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

	if len(profile.Tools) == 0 {
		reg := tools.NewRegistry()
		if opts.tracker != nil {
			for _, name := range []string{"write", "edit", "delete"} {
				if t := tools.NewLeafTool(name); t != nil {
					reg.Register(tools.NewRecordingTool(t, opts.tracker))
				}
			}
		}
		if remainingDepth > 0 {
			registerTask(reg)
		}
		return reg
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
// default > a sane floor of 1. The result is never allowed to exceed the
// role profile's MaxIterations ceiling, so a per-call override cannot
// neutralize the role's cap.
func resolveSubAgentIterations(callMax int, profile subagent.RoleProfile, subCfg config.SubAgentConfig, role subagent.SubAgentRole) int {
	var v int
	switch {
	case callMax > 0:
		v = callMax
	case subCfg.Roles[string(role)].MaxIterations > 0:
		v = subCfg.Roles[string(role)].MaxIterations
	case profile.MaxIterations > 0:
		v = profile.MaxIterations
	default:
		return 1
	}
	if profile.MaxIterations > 0 && v > profile.MaxIterations {
		v = profile.MaxIterations
	}
	return v
}
