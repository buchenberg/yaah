# Development Plan: Supervised Task Tool (yaah side)

**Goal**: Add a `supervised_task` tool to yaah that runs a sub-agent
with checkpoint/rollback/retry — blocking by construction, foolproof
against parallel filesystem contention.

**Existing `spawn_subagent` tool**: unchanged. Keeps parallel,
background, unsupervised execution.

**shepherd-kernel-go**: no changes needed. This plan wires the engine
we already built into yaah's tool layer.

---

## Tracing Strategy

Not all agent levels need shepherd tracing. The decision per level:

| Level | Default | Why |
|-------|---------|-----|
| Supervised agents | ON (required) | Tracing IS the mechanism — no checkpoint/rollback without it |
| Regular subagents | ON (debugging) | `subagent_trace` tool + error enrichment give real value when subagents fail |
| Orchestrator | OFF (opt-in) | Conversation history + OTel already cover it; tracing adds hot-path SQL latency for marginal gain |

**Orchestrator tracing**: Currently `"shepherd_trace"` is in
`defaultPipelineNames` (`pipeline/config.go:156-166`), which means every
orchestrator turn does two synchronous SQL inserts (declaration +
capture) on the tool dispatch path. The orchestrator's actions are
already fully recorded in `LoopState.Messages` — the trace store adds
latency without adding information. Remove it from the default pipeline.

**Subagent tracing**: Keep. Two concrete consumers depend on it:
1. `internal/tools/subagent_trace.go` — orchestrator profiles failed subagent traces
2. `internal/agent/runner/runner.go:311-316` — error enrichment appends trace to subagent failures

**SQLite contention fix**: `subagent_loop.go:113-118` opens a *separate*
SQLite connection to the same `trace.sqlite` file. Under concurrent
writes (orchestrator + parallel subagents), this causes contention with
5-second `busy_timeout` stalls. Fix: use the shared store from
`SharedScopeManager` instead of opening a new one.

---

## Architecture

```
Orchestrator (LLM)
  │
  ├── spawn_subagent("fix db", background=true)     ← EXISTING, unchanged
  │     └── jobs.BackgroundJobs → goroutine          (parallel, unsupervised)
  │
  └── supervised_task("fix auth")                    ← NEW, blocking
        │
        │ ┌── SupervisedTaskTool.Execute ────────────────────────────┐
        │ │                                                           │
        │ │  1. Create scope + checkpoint                             │
        │ │     mgr.Create(scopeID)                                   │
        │ │     mgr.CreateCheckpoint(scope, repoPath, messages)       │
        │ │                                                           │
        │ │  2. FOR attempt = 0..max_retries:                         │
        │ │     a. Run sub-agent synchronously                        │
        │ │        result = runner(ctx, prompt, params)               │
        │ │                                                           │
        │ │     b. Evaluate result                                    │
        │ │        if result.Success → return result                  │
        │ │                                                           │
        │ │     c. If last attempt → return failure summary           │
        │ │                                                           │
        │ │     d. Rollback workspace + conversation                  │
        │ │        snapshot = mgr.RestoreCheckpoint(cp.ID)            │
        │ │        messages = json.Unmarshal(snapshot)                │
        │ │                                                           │
        │ │     e. Inject failure guidance into prompt                │
        │ │        prompt = buildRetryPrompt(prompt, result)          │
        │ │                                                           │
        │ └───────────────────────────────────────────────────────────┘
        │
        ← orchestrator unblocks
```

The orchestrator is blocked inside step 2a. It **cannot** issue another
tool call until `supervised_task` returns. Parallel filesystem
contention is impossible — not guarded against, structurally impossible.

---

## Codebase grounding

### What exists (references for the implementer)

**`internal/jobs/types.go:12`** — TaskRunner type signature:
```go
type TaskRunner func(ctx context.Context, prompt string, params SubAgentParams) (string, error)
```

**`internal/jobs/output.go:14`** — SubAgentParams:
```go
type SubAgentParams struct {
    Role           string
    TimeoutSeconds int
    MaxLoopCycles  int
    MaxToolTurns   int
    JSONMode       bool
    OutputLimit    int
}
```

**`internal/tools/task.go:29`** — existing TaskTool struct. Key fields:
- `Runner jobs.TaskRunner` — the sub-agent runner closure
- `ResolveTimeout func(SubAgentParams) time.Duration`
- `RoleNames []string` / `RoleResolver func() []string`
- `RoleDescriptions map[string]string`
- `Tracker *ConflictTracker`
- `BackgroundJobs *jobs.BackgroundJobs`

**`internal/tools/supervisor_shared.go:11`** — shared scope manager:
```go
var SharedScopeManager *shepherd.ScopeManager
```
Set by the pipeline config builder (`pipeline/config.go:130`).

**`internal/agent/runner/runner.go:201`** — `makeTaskRunner` builds the
TaskRunner closure. It constructs the sub-agent loop, runs it, enriches
errors with trace data, trims output. The closure at line 271-277
already creates a shepherd scope for each sub-agent.

**`internal/agent/types.go:152`** — LoopState.Messages:
```go
type LoopState struct {
    Messages    []types.Message
    TotalTokens Usage
    // ...
}
```

**`cmd/yaah/wiring.go:196`** — where tools are registered:
```go
taskTool := runner.NewTaskTool(...)
toolReg.Register(taskTool)
toolReg.Register(&tools.SupervisorTool{...})
```

### What we build

One new file + modifications to wiring.

---

## Phase 0: Tracing Architecture Fix

Three problems to fix before building on top:

1. **Orchestrator tracing is useless and on the hot path** — every orchestrator turn does two synchronous SQL inserts for information already in `LoopState.Messages`. Remove it entirely.
2. **`buildPipeline` is either/or** — when subagent loop appends trace middleware to `l.Middleware`, it takes a branch that returns *only* the trace middleware, silently losing loop detection, tool concurrency, staleness, pruning. Subagents need their own pipeline config.
3. **Subagents open separate SQLite connections** — contention on the same `trace.sqlite` under concurrent writes.

### 0.1 Hard-disable orchestrator tracing

**File**: `internal/agent/pipeline/config.go` (modify)

Remove `"shepherd_trace"` from `defaultPipelineNames` AND from
`builtinBuilders`. Orchestrator tracing is not opt-in — it's gone.

```go
// Before:
var defaultPipelineNames = []string{
    "steer",
    "followup",
    "compaction",
    "soft_prune",
    "approval",
    "tool_concurrency",
    "loop_detection",
    "staleness",
    "shepherd_trace",  // ← REMOVE
}

// After:
var defaultPipelineNames = []string{
    "steer",
    "followup",
    "compaction",
    "soft_prune",
    "approval",
    "tool_concurrency",
    "loop_detection",
    "staleness",
}
```

Also remove the `"shepherd_trace"` entry from `builtinBuilders` (lines
105-139). The builder logic (store + bus + scope manager creation) moves
to `InitShepherdInfrastructure` (§0.3).

The orchestrator's actions are fully recorded in `LoopState.Messages`
and OTel spans. Adding a trace store on the orchestrator's hot path
adds SQL latency to every tool call without adding information.

### 0.2 Add subagent pipeline config + fix buildPipeline

**File**: `internal/agent/pipeline/config.go` (modify)

Add a separate default pipeline for subagents:

```go
// subAgentPipelineNames is the curated middleware pipeline for sub-agent
// loops. Sub-agents are ephemeral workers with fixed turn budgets — they
// don't need orchestrator-level middleware.
//
// Deliberately excluded:
// - steer/followup: orchestrator REPL channels, subagents never receive them
// - approval: subagents auto-approve (the orchestrator gates dispatch)
// - compaction: subagents use CtxMgr pruning internally, not pipeline compaction
// - loop_detection: redundant with MaxLoopCycles/MaxToolTurns/WrapUpThreshold/ErrStuckChild
// - staleness: orchestrator-specific (tracks steer/followup context shifts)
// - soft_prune: CtxMgr.EnsurePruner() already handles context for short-lived loops
//
// Included:
// - tool_concurrency: prevents uncontrolled parallel tool dispatch
// - shepherd_trace: records tool calls for error enrichment and supervised rollback
var subAgentPipelineNames = []string{
    "tool_concurrency",
    "shepherd_trace",
}
```

Add a function to resolve the subagent pipeline, respecting disabled overrides:

```go
// SubAgentPipelineNames returns the middleware names for a sub-agent loop,
// honouring the disabled list (for opt-out of specific middleware).
func SubAgentPipelineNames(disabled []string) []string {
    disabledSet := make(map[string]bool, len(disabled))
    for _, name := range disabled {
        disabledSet[name] = true
    }
    names := make([]string, 0, len(subAgentPipelineNames))
    for _, name := range subAgentPipelineNames {
        if !disabledSet[name] {
            names = append(names, name)
        }
    }
    return names
}
```

Add a builder for subagent pipelines:

```go
// NewSubAgentPipeline builds a pipeline for a sub-agent loop from the
// given config. Uses subAgentPipelineNames instead of the orchestrator
// default. The shepherd_trace builder here uses the shared store from
// SharedScopeManager (set by InitShepherdInfrastructure).
func NewSubAgentPipeline(cfg PipelineConfig) *Pipeline {
    names := SubAgentPipelineNames(cfg.PipelineDisabled)
    mws := make([]Middleware, 0, len(names))
    for _, name := range names {
        if build, ok := subAgentBuilders[name]; ok {
            mws = append(mws, build(cfg))
        }
    }
    return NewPipeline(mws...)
}
```

Where `subAgentBuilders` includes the same builders as `builtinBuilders`
for the shared middleware names, plus a subagent-specific `shepherd_trace`
builder that uses the shared store (see §0.4).

**File**: `internal/agent/loop.go` (modify — fix the either/or bug)

```go
// Before (broken — either/or):
func (l *Loop) buildPipeline() *pipeline.Pipeline {
    if len(l.Middleware) > 0 {
        return pipeline.NewPipeline(l.Middleware...)
    }
    return pipeline.NewFromConfig(l.toPipelineConfig())
}

// After (fixed — merge):
func (l *Loop) buildPipeline() *pipeline.Pipeline {
    if l.IsSubAgent {
        return pipeline.NewSubAgentPipeline(l.toPipelineConfig())
    }
    return pipeline.NewFromConfig(l.toPipelineConfig())
}
```

The `l.Middleware` field is no longer used for pipeline construction.
Instead, the subagent's trace middleware is built inside
`NewSubAgentPipeline` using the shared store. The `Middleware` field
can be removed or repurposed (for ad-hoc middleware injection by tests).

Add `IsSubAgent bool` to `LoopConfig` (set by `NewSubAgentLoop`).

### 0.3 Initialize shepherd infrastructure (standalone)

**File**: `internal/agent/pipeline/scope_init.go` (NEW, ~40 lines)

```go
// InitShepherdInfrastructure creates the trace store, effect bus, and
// scope manager for the session. Called from wiring.go when
// ShepherdTraceDir is set, regardless of pipeline membership.
func InitShepherdInfrastructure(traceDir string, sessionID string, busBuffer int) (
    store *shepherd.SQLiteTraceStore,
    bus *shepherd.EffectBus,
    mgr *shepherd.ScopeManager,
    err error,
) {
    if err := os.MkdirAll(traceDir, 0o755); err != nil {
        return nil, nil, nil, fmt.Errorf("shepherd: mkdir: %w", err)
    }
    store, err = shepherd.NewSQLiteTraceStore(filepath.Join(traceDir, "trace.sqlite"))
    if err != nil {
        return nil, nil, nil, fmt.Errorf("shepherd: open store: %w", err)
    }
    bus = shepherd.NewEffectBus(busBuffer)
    store.WithBus(bus)
    mgr = shepherd.NewScopeManager(store)
    return store, bus, mgr, nil
}
```

### 0.4 Subagent trace builder using shared store

**File**: `internal/agent/pipeline/config.go` (modify)

The subagent pipeline's `shepherd_trace` builder uses the shared store
from `SharedScopeManager` instead of opening a new connection:

```go
var subAgentBuilders = map[string]func(PipelineConfig) Middleware{
    "tool_concurrency": func(cfg PipelineConfig) Middleware {
        max := cfg.MaxToolConcurrency
        if max <= 0 { max = 5 }
        return &ToolConcurrencyMiddleware{max: max}
    },
    "shepherd_trace": func(cfg PipelineConfig) Middleware {
        // Use the shared store from the session-level scope manager.
        // This avoids opening a separate SQLite connection.
        mgr := tools.SharedScopeManager
        if mgr == nil {
            return &noopShepherdTraceMiddleware{}
        }
        return &ShepherdTraceMiddleware{
            store:        mgr.Store(),
            sessionID:    cfg.SessionID,
            ordinal:      int(nextOrdinal.Add(1 << 20)),
        }
    },
}
```

### 0.5 Simplify NewSubAgentLoop

**File**: `internal/agent/subagent_loop.go` (modify)

Remove the manual trace store opening and middleware appending — the
pipeline builder handles it now:

```go
// Before (broken — opens separate store, breaks buildPipeline):
if cfg.TraceDir != "" {
    store, err := pipeline.NewShepherdTraceStore(filepath.Join(cfg.TraceDir, "trace.sqlite"))
    if err == nil {
        traceMw := pipeline.NewShepherdTraceMiddleware(store, cfg.TraceSessionID)
        l.Middleware = append(l.Middleware, traceMw)
    }
}

// After (clean — pipeline builder handles tracing via shared store):
// No trace code here. buildPipeline → NewSubAgentPipeline → shepherd_trace
// builder uses SharedScopeManager.Store(). TraceDir/TraceSessionID are
// no longer needed on SubAgentConfig — the session ID comes from
// PipelineConfig.SessionID.
```

Set `IsSubAgent: true` in the LoopConfig:

```go
l := &Loop{
    // ...
    Config: LoopConfig{
        // ...
        IsSubAgent: true,
    },
}
```

### 0.6 shepherd-kernel-go: Store() accessor

**File**: `shepherd-kernel-go/scope_manager.go` (modify)

```go
// Store returns the trace store backing this scope manager.
func (m *ScopeManager) Store() *SQLiteTraceStore {
    return m.store
}
```

### 0.7 Update wiring

**File**: `cmd/yaah/wiring.go` (modify)

Call `InitShepherdInfrastructure` directly instead of relying on the
pipeline builder:

```go
if cfg.Agent.Default.ShepherdTraceDir != "" {
    _, _, scopeMgr, err := pipeline.InitShepherdInfrastructure(
        cfg.Agent.Default.ShepherdTraceDir,
        sessionID,
        cfg.Agent.Default.ShepherdBusBuffer,
    )
    if err != nil {
        slog.Debug("shepherd infrastructure init failed", "err", err)
    } else {
        tools.SharedScopeManager = scopeMgr
    }
}
```

### 0.8 Phase 0 Test Plan

| Test | Description |
|------|-------------|
| `TestPipeline_OrchestratorNoTraceBuilder` | `builtinBuilders` does not contain `"shepherd_trace"` |
| `TestPipeline_SubAgentPipelineNames` | `subAgentPipelineNames` contains tool_concurrency, shepherd_trace (only) |
| `TestPipeline_SubAgentExcludesOrchestrator` | Subagent pipeline does not contain steer, followup, approval, compaction |
| `TestBuildPipeline_SubAgentUsesSubAgentPipeline` | `IsSubAgent=true` → uses `NewSubAgentPipeline`, not `NewFromConfig` |
| `TestBuildPipeline_OrchestratorUsesDefault` | `IsSubAgent=false` → uses `NewFromConfig` with default names |
| `TestSubAgentTrace_UsesSharedStore` | When SharedScopeManager is set, subagent trace middleware uses `mgr.Store()` |
| `TestSubAgentTrace_NoSharedManager` | When SharedScopeManager is nil, subagent gets noop trace middleware |
| `TestInitShepherdInfrastructure` | Returns non-nil store, bus, manager; store has bus attached |

---

## Phase 1: SupervisedTaskTool

**File**: `internal/tools/supervised_task.go` (~200 lines)

### 1.1 Struct

```go
// SupervisedTaskTool runs a sub-agent with checkpoint/rollback/retry.
//
// Unlike spawn_subagent, this tool is BLOCKING — the orchestrator's
// loop is stuck inside Execute until the sub-agent completes (or all
// retries are exhausted). This guarantees sequential execution: the
// orchestrator cannot dispatch a second supervised task while the
// first is running, preventing filesystem contention.
//
// The tool creates a workspace checkpoint (via shepherd-kernel-go's
// git-based checkpoint) before running the sub-agent. If the sub-agent
// fails, the workspace is rolled back and the sub-agent is retried
// with guidance derived from the failure.
type SupervisedTaskTool struct {
    // Runner is the sub-agent execution closure (same type as TaskTool).
    Runner jobs.TaskRunner

    // ResolveTimeout returns the default timeout for a call.
    ResolveTimeout func(params jobs.SubAgentParams) time.Duration

    // RoleNames / RoleResolver / RoleDescriptions — same purpose as
    // TaskTool. The supervised tool exposes the same role selection.
    RoleNames        []string
    RoleResolver     func() []string
    RoleDescriptions map[string]string

    // RepoPath is the git repo for workspace checkpoints. If empty,
    // defaults to the current working directory at Execute time.
    RepoPath string

    // MaxRetries caps the rollback-and-retry cycles. 0 means one shot
    // (no retry). The tool adds 1 to get total attempts (1 + max_retries).
    // Default 1 if not set by config.
    MaxRetries int
}
```

### 1.2 Schema

```go
func (*SupervisedTaskTool) Name() string { return "supervised_task" }

func (*SupervisedTaskTool) Description() string {
    return "Run a sub-agent with automatic rollback and retry on failure. " +
        "BLOCKING — this call does not return until the sub-agent completes. " +
        "If the sub-agent fails, its workspace changes are rolled back via git " +
        "and it is retried with guidance from the failure. Use this instead of " +
        "spawn_subagent when you want checkpoint safety and can tolerate the " +
        "blocking call. Do NOT use for parallel work — use spawn_subagent for that."
}

func (t *SupervisedTaskTool) Schema() json.RawMessage {
    // Dynamic schema: role enum is built from RoleResolver/RoleNames,
    // same pattern as TaskTool.
    return json.RawMessage(`{
        "type": "object",
        "properties": {
            "description": {
                "type": "string",
                "description": "Short label for this task (shown in UI)"
            },
            "prompt": {
                "type": "string",
                "description": "The task to accomplish"
            },
            "role": {
                "type": "string",
                "description": "Sub-agent role"
                // enum built dynamically (see buildRoleEnum helper)
            },
            "timeout_seconds": {
                "type": "integer",
                "minimum": 10,
                "maximum": 600,
                "description": "Per-attempt timeout"
            },
            "max_iterations": {
                "type": "integer",
                "minimum": 1,
                "maximum": 50
            }
        },
        "required": ["prompt", "role"]
    }`)
}
```

Key differences from spawn_subagent schema:
- **No `background` parameter** — always blocking
- **No `max_turns` exposed** — managed internally
- **No `json_mode` / `output_limit`** — managed by role profile
- **No `parallel` anything** — by design

### 1.3 Execute

```go
func (t *SupervisedTaskTool) Execute(ctx context.Context, args string) (string, error) {
    var raw map[string]any
    if err := json.Unmarshal([]byte(args), &raw); err != nil {
        return "", fmt.Errorf("supervised_task: invalid arguments: %w", err)
    }

    // Parse (same coercion + clamping as TaskTool)
    var params struct {
        Description    string `json:"description"`
        Prompt         string `json:"prompt"`
        Role           string `json:"role"`
        TimeoutSeconds int    `json:"timeout_seconds"`
        MaxLoopCycles  int    `json:"max_iterations"`
    }
    fixed, _ := json.Marshal(raw)
    if err := json.Unmarshal(fixed, &params); err != nil {
        return "", fmt.Errorf("supervised_task: invalid arguments: %w", err)
    }
    if params.Prompt == "" {
        return "", fmt.Errorf("supervised_task: prompt is required")
    }

    // Role validation (same pattern as TaskTool)
    known := t.roleNames()
    if params.Role == "" {
        return "", fmt.Errorf("supervised_task: role is required — pick one of: %s",
            strings.Join(known, ", "))
    }
    if len(known) > 0 && !slices.Contains(known, params.Role) {
        return "", fmt.Errorf("supervised_task: unknown role %q — valid: %s",
            params.Role, strings.Join(known, ", "))
    }

    if t.Runner == nil {
        return "", fmt.Errorf("supervised_task: sub-agent runner not configured")
    }

    // Check shepherd scope manager is available
    mgr := tools.SharedScopeManager
    if mgr == nil {
        return "", fmt.Errorf("supervised_task: shepherd tracing not enabled " +
            "(set shepherd_trace_dir in config)")
    }

    // Resolve repo path
    repoPath := t.RepoPath
    if repoPath == "" {
        repoPath, _ = os.Getwd()
    }

    // Clamp params (reuse TaskTool helpers)
    clampedTimeout := clampTimeoutSeconds(params.TimeoutSeconds)
    clampedIter := clampMaxLoopCycles(params.MaxLoopCycles)

    subParams := jobs.SubAgentParams{
        Role:           params.Role,
        TimeoutSeconds: clampedTimeout,
        MaxLoopCycles:  clampedIter,
    }

    timeout := resolveTaskTimeout(clampedTimeout, t.ResolveTimeout, subParams)

    // Create scope + checkpoint
    scopeID := fmt.Sprintf("supervised:%s:%d", params.Role, time.Now().UnixNano())
    scope, err := mgr.Create(scopeID)
    if err != nil {
        return "", fmt.Errorf("supervised_task: create scope: %w", err)
    }

    // The checkpoint captures the workspace state + an empty conversation
    // snapshot. The conversation snapshot for retry guidance is managed
    // internally (we don't need to restore the parent's conversation —
    // each sub-agent starts fresh from the prompt).
    cp, err := mgr.CreateCheckpoint(scope.ID(), repoPath, nil)
    if err != nil {
        return "", fmt.Errorf("supervised_task: checkpoint: %w", err)
    }

    maxRetries := t.MaxRetries
    if maxRetries < 0 {
        maxRetries = 1
    }

    currentPrompt := params.Prompt

    for attempt := 0; attempt <= maxRetries; attempt++ {
        // Apply timeout per attempt
        runCtx := ctx
        var cancel context.CancelFunc
        if timeout > 0 {
            runCtx, cancel = context.WithTimeout(ctx, timeout)
        }

        result, runErr := t.Runner(runCtx, currentPrompt, subParams)

        if cancel != nil {
            cancel()
        }

        // Success path
        if runErr == nil && strings.TrimSpace(result) != "" {
            suffix := ""
            if attempt > 0 {
                suffix = fmt.Sprintf(" (succeeded on attempt %d after rollback)", attempt+1)
            }
            return result + suffix, nil
        }

        // Last attempt — return failure without rollback
        if attempt == maxRetries {
            return structuredSupervisedResult("failed", attempt+1, result, runErr), nil
        }

        // Rollback workspace
        _, restoreErr := mgr.RestoreCheckpoint(cp.ID)
        if restoreErr != nil {
            // Can't rollback — return the original failure
            return structuredSupervisedResult("rollback_failed", attempt+1, result, runErr), nil
        }

        // The checkpoint is now "used" — create a new one for the next attempt
        // so we can rollback again if needed.
        cp, err = mgr.CreateCheckpoint(scope.ID(), repoPath, nil)
        if err != nil {
            return structuredSupervisedResult("recheckpoint_failed", attempt+1, result, runErr), nil
        }

        // Inject failure guidance into the retry prompt
        currentPrompt = buildRetryPrompt(params.Prompt, attempt, result, runErr)
    }

    return structuredSupervisedResult("exhausted", maxRetries+1, "", nil), nil
}
```

### 1.4 Helpers

```go
// buildRetryPrompt constructs a retry prompt that carries forward the
// failure context so the sub-agent doesn't repeat the same mistakes.
func buildRetryPrompt(originalPrompt string, attempt int, result string, runErr error) string {
    var sb strings.Builder
    sb.WriteString(originalPrompt)
    sb.WriteString("\n\n--- SUPERVISOR GUIDANCE (attempt ")
    sb.WriteString(fmt.Sprintf("%d failed, retrying) ---\n", attempt+1))

    if runErr != nil {
        sb.WriteString("Previous attempt failed with error:\n")
        sb.WriteString(runErr.Error())
        sb.WriteString("\n\n")
    }

    if strings.TrimSpace(result) != "" {
        sb.WriteString("Previous attempt output (before rollback):\n")
        // Truncate to avoid context bloat
        truncated := result
        if len(truncated) > 2000 {
            truncated = truncated[:2000] + "\n...[truncated]"
        }
        sb.WriteString(truncated)
        sb.WriteString("\n\n")
    }

    sb.WriteString("Your previous changes have been rolled back. Take a different approach.\n")
    return sb.String()
}

// structuredSupervisedResult builds a JSON payload for non-success outcomes.
func structuredSupervisedResult(status string, attempts int, partial string, runErr error) string {
    errMsg := ""
    if runErr != nil {
        errMsg = runErr.Error()
    }
    out := struct {
        Status  string `json:"status"`
        Attempts int   `json:"attempts"`
        Error   string `json:"error,omitempty"`
        Partial string `json:"partial,omitempty"`
    }{
        Status:   status,
        Attempts: attempts,
        Error:    errMsg,
        Partial:  partial,
    }
    data, _ := json.Marshal(out)
    return string(data)
}

// roleNames resolves the advertised role list (same pattern as TaskTool).
func (t *SupervisedTaskTool) roleNames() []string {
    if t.RoleResolver != nil {
        if names := t.RoleResolver(); len(names) > 0 {
            return names
        }
    }
    return t.RoleNames
}
```

---

## Phase 2: Wiring

**File**: `cmd/yaah/wiring.go` (modify ~20 lines)

### 2.1 Register the tool

After the existing `taskTool` registration (line 230), add:

```go
// Supervised task tool — blocking sub-agent with checkpoint/rollback/retry.
// Shares the same runner, role resolver, and timeout logic as the task tool,
// but runs synchronously and wraps each attempt in a git checkpoint.
if cfg.Agent.Default.ShepherdTraceDir != "" {
    supervisedTool := &tools.SupervisedTaskTool{
        Runner:           taskTool.Runner,        // reuse the same runner
        ResolveTimeout:   taskTool.ResolveTimeout, // reuse timeout resolution
        RoleNames:        taskTool.RoleNames,
        RoleResolver:     taskTool.RoleResolver,
        RoleDescriptions: taskTool.RoleDescriptions,
        RepoPath:         "",  // defaults to cwd at Execute time
        MaxRetries:       1,   // one rollback-and-retry by default
    }
    toolReg.Register(supervisedTool)
}
```

The supervised tool is **only registered when shepherd tracing is
enabled** (same condition as the SupervisorTool). When tracing is off,
the model never sees `supervised_task` — it only sees `spawn_subagent`.

### 2.2 Config (optional)

Add a config field for max retries and repo path:

```go
// In config struct (config/load.go or wherever Defaults is defined):
type Defaults struct {
    // ... existing fields ...
    SupervisedMaxRetries int    `yaml:"supervised_max_retries"`
    SupervisedRepoPath   string `yaml:"supervised_repo_path"`
}
```

Wire in wiring.go:
```go
MaxRetries: cfg.Agent.Default.SupervisedMaxRetries,
RepoPath:   cfg.Agent.Default.SupervisedRepoPath,
```

Defaults: MaxRetries=1, RepoPath="" (cwd). These are optional — the
tool works without config changes.

---

## Phase 3: System Prompt Guidance

**File**: `internal/prompts/` (modify the tool descriptions section)

The orchestrator's system prompt needs to know when to use
`supervised_task` vs `spawn_subagent`. Add to the tool guidance:

```
## Sub-agent tools

You have two sub-agent tools:

- **spawn_subagent**: Non-blocking. Supports parallel and background
  execution. Use for independent tasks that can run concurrently.
  No rollback safety — if a sub-agent makes bad changes, they persist.

- **supervised_task**: BLOCKING. Runs a single sub-agent to completion
  with automatic git checkpoint and rollback. If the sub-agent fails,
  its changes are reverted and it's retried with guidance. Use this
  for tasks where correctness matters more than speed, and where a
  failed attempt should not leave the workspace in a broken state.

Do NOT use supervised_task for parallel work. It blocks your loop.
Use spawn_subagent for parallel tasks, supervised_task for tasks that
must succeed or roll back cleanly.
```

---

## Phase 4: Tests

**File**: `internal/tools/supervised_task_test.go` (~200 lines)

### 4.1 Test cases

| Test | Description |
|------|-------------|
| `TestSupervisedTask_Success` | Runner succeeds on first attempt → returns result, no rollback |
| `TestSupervisedTask_RetrySucceeds` | Attempt 1 fails, attempt 2 succeeds → result with "attempt 2" suffix |
| `TestSupervisedTask_AllRetriesFail` | All attempts fail → structured "failed" result |
| `TestSupervisedTask_RollbackCalled` | Failure triggers RestoreCheckpoint (verify via mock scope manager) |
| `TestSupervisedTask_NoScopeManager` | SharedScopeManager nil → error |
| `TestSupervisedTask_RoleValidation` | Empty role → error; unknown role → error |
| `TestSupervisedTask_MissingPrompt` | Empty prompt → error |
| `TestSupervisedTask_BuildRetryPrompt` | Verify guidance includes error + partial output |

### 4.2 Test approach

Tests use a fake `jobs.TaskRunner` that tracks call count and returns
configured results:

```go
func makeFakeRunner(responses []runnerResponse) jobs.TaskRunner {
    var callCount int
    var mu sync.Mutex
    return func(ctx context.Context, prompt string, params jobs.SubAgentParams) (string, error) {
        mu.Lock()
        defer mu.Unlock()
        idx := callCount
        callCount++
        if idx >= len(responses) {
            return "default", nil
        }
        r := responses[idx]
        return r.result, r.err
    }
}
```

For checkpoint tests, use a real temp git repo (same pattern as
shepherd-kernel-go's `newTestRepo`). Set `tools.SharedScopeManager`
to a real ScopeManager backed by a `:memory:` store.

---

## File Inventory

| File | Status | LOC (est.) | Phase |
|------|--------|------------|-------|
| `internal/agent/pipeline/config.go` | MODIFY | ~100 (remove builder, add subagent pipeline + builders) | 0.1, 0.2, 0.4 |
| `internal/agent/pipeline/scope_init.go` | NEW | ~40 | 0.3 |
| `internal/agent/loop.go` | MODIFY | ~10 (fix buildPipeline either/or) | 0.2 |
| `internal/agent/subagent_loop.go` | MODIFY | ~-15 (remove manual store, set IsSubAgent) | 0.5 |
| `shepherd-kernel-go/scope_manager.go` | MODIFY | +5 (Store accessor) | 0.6 |
| `cmd/yaah/wiring.go` | MODIFY | ~20 (standalone init + tool registration) | 0.7, 2 |
| `internal/tools/supervised_task.go` | NEW | ~200 | 1 |
| `internal/tools/supervised_task_test.go` | NEW | ~200 | 4 |
| `internal/prompts/` | MODIFY | +15 | 3 |
| `internal/config/load.go` | MODIFY | +5 (optional config fields) | 2 |
| **Total** | | **~580** | |

---

## Why this is foolproof

| Threat | Prevention |
|--------|------------|
| Two supervised agents on same filesystem | Impossible — Execute blocks until completion. Orchestrator can't call it twice. |
| Model calls restore at wrong time | Impossible — restore is internal to Execute, not a separate tool action |
| Checkpoint/restore interleaving | Impossible — single goroutine within the blocking call |
| Accidental parallel via spawn_subagent | That's existing behavior (unsupervised). Supervised doesn't make it worse. |
| Model bypasses supervision by using spawn_subagent | Possible but expected — spawn_subagent has no rollback guarantees |
| Runner panics mid-attempt | The checkpoint from before the attempt is still valid; next attempt gets a fresh sub-agent |

## What the model sees

```
Tool: supervised_task
Description: Run a sub-agent with automatic rollback and retry on failure.
             BLOCKING. Use instead of spawn_subagent when you want
             checkpoint safety.

Parameters:
  prompt (required): The task to accomplish
  role (required): Sub-agent role
  description: Short label
  timeout_seconds: Per-attempt timeout (10-600)
  max_iterations: Max loop cycles (1-50)

Returns:
  On success: the sub-agent's final output
  On failure: { "status": "failed", "attempts": N, "error": "...", "partial": "..." }
```

No checkpoint IDs. No scope IDs. No fork/merge/discard. No
background/parallel options. The complexity is fully hidden.

---

## Execution Order

```
Phase 0: Tracing Architecture Fix (prerequisites)
  0.1 Hard-disable orchestrator tracing
       └── config.go — remove "shepherd_trace" from defaultPipelineNames
                       AND from builtinBuilders
  0.2 Add subagent pipeline config + fix buildPipeline
       ├── config.go — subAgentPipelineNames + SubAgentPipelineNames()
       ├── config.go — NewSubAgentPipeline() builder
       └── loop.go — fix either/or: IsSubAgent → NewSubAgentPipeline
  0.3 Create standalone shepherd infrastructure initializer
       └── scope_init.go — InitShepherdInfrastructure()
  0.4 Subagent trace builder using shared store
       └── config.go — subAgentBuilders map with shared-store shepherd_trace
  0.5 Simplify NewSubAgentLoop
       └── subagent_loop.go — remove manual store opening, set IsSubAgent: true
  0.6 shepherd-kernel-go Store() accessor
       └── scope_manager.go — +5 lines
  0.7 Update wiring
       └── wiring.go — call InitShepherdInfrastructure, set SharedScopeManager
  0.8 Tests (8 cases)
       ├── TestPipeline_OrchestratorNoTraceBuilder
       ├── TestPipeline_SubAgentPipelineNames
       ├── TestPipeline_SubAgentExcludesOrchestrator
       ├── TestBuildPipeline_SubAgentUsesSubAgentPipeline
       ├── TestBuildPipeline_OrchestratorUsesDefault
       ├── TestSubAgentTrace_UsesSharedStore
       ├── TestSubAgentTrace_NoSharedManager
       └── TestInitShepherdInfrastructure

Phase 1: SupervisedTaskTool
  ├── Struct + Schema
  ├── Execute (checkpoint → run → evaluate → rollback → retry)
  ├── buildRetryPrompt helper
  ├── structuredSupervisedResult helper
  ├── roleNames helper (same pattern as TaskTool)
  └── Reuse clampTimeoutSeconds, clampMaxLoopCycles, resolveTaskTimeout from task.go

Phase 2: wiring.go
  ├── Register SupervisedTaskTool after taskTool
  ├── Only when ShepherdTraceDir != ""
  ├── Share runner + role resolver from taskTool
  └── Optional: config fields for max_retries + repo_path

Phase 3: system prompt
  └── Add supervised_task vs spawn_subagent guidance

Phase 4: tests
  ├── Fake runner with configured responses
  ├── Real temp git repo for checkpoint verification
  ├── 8 test cases
  └── Run: go test ./internal/tools/ -run TestSupervisedTask -v
```

---

## Non-Goals

- **Pre-tool interception (CheckCall)** — the shepherd-kernel-go
  `Supervisor.CheckCall` method exists, but wiring it into the sub-agent's
  tool dispatch path requires a new pipeline middleware hook (`PreTool`).
  That's a deeper change to the agent loop. The supervised_task tool
  achieves safety through rollback+retry instead of pre-execution denial.
  CheckCall can be added later as a complementary layer.

- **Worktree isolation for parallel supervised agents** — git worktrees
  (`git worktree add`) would let multiple supervised agents run in
  parallel with isolated working directories. That's ~50 lines of
  WorktreeManager code in shepherd-kernel-go. Not needed for the
  sequential-only supervised_task tool.

- **Conversation snapshot restoration** — each sub-agent attempt starts
  fresh from the (possibly augmented) prompt. We don't restore the
  sub-agent's conversation history across retries — the retry prompt
  carries the failure context. This is simpler and avoids serialization
  complexity. If needed later, `CreateCheckpoint` already accepts a
  `[]byte` snapshot. (Superseded for turn-level restores: see the
  per-turn checkpoint appendix below, which does restore conversation
  snapshots — but only within an attempt, not across attempt retries.)

---

# Appendix: Per-Turn Checkpoint & Restore

Added on top of the attempt-level design above. Plan of record:
`.agents/plans/per-turn-checkpoint-restore/PLAN.md`.

Attempt-level rollback (above) rewinds the whole sub-agent run when the
run fails. Per-turn checkpointing adds a finer granularity: the loop
snapshots the workspace **and** the conversation before each model turn,
so a single failed turn can be rewound without discarding the progress
the attempt already made.

## Per-attempt vs per-turn

| | Per-attempt (supervised_task) | Per-turn (loop) |
|---|---|---|
| Scope | `supervised:<role>:<nanos>` | `sub-<role>-<session>-<nanos>` |
| Checkpoint taken | Once, before the retry loop | Before every model turn |
| Snapshot payload | `nil` (files only) | Serialized conversation (`LoopState.Messages`) |
| Restore trigger | Sub-agent run failed → retry | Hard tool-phase error, or iteration exhaustion |
| Restore effect | Reverts files; conversation restarts fresh from the retry prompt | Reverts files **and** rewinds conversation to the pre-turn state, then appends guidance |
| Visibility | The orchestrator sees attempts in the envelope | Invisible to the orchestrator except via `restores`/`restored_from` envelope fields |
| Gating | Role `supervised: true` (attempt checkpointing always on for supervised roles) | Role `turn_checkpoints: true` (default false) |

## Restore policy and triggers

Conservative, automatic restore fires only on:

1. **Hard `executeToolPhase` error** — e.g. a PostTool middleware
   failure such as loop detection halting on repeated identical calls.
   The loop restores the last checkpoint, appends guidance ("the
   previous turn failed... take a different approach"), and continues.
2. **Iteration exhaustion** — the inner loop hits `MaxLoopCycles` with
   no final answer and no continuation. The loop restores the last
   checkpoint, appends wrap-up guidance, and retries with a fresh
   budget.

Silent textual failure (a sub-agent that merely *says* it failed) never
triggers a restore — that is a normal result the orchestrator handles.

## Guards

- `MaxTurnRestores` (default 3, `max_turn_restores`) bounds restores per
  run so a deterministically broken turn cannot rewind forever.
- `TurnCheckpointMax` (`turn_checkpoint_max`, 0 = unlimited) caps live
  checkpoints; when reached, the loop prunes the scope before taking a
  new checkpoint (v1 only ever restores the most recent checkpoint, so
  older ones are dead weight).
- Unconsumed checkpoints are pruned when the run ends.

## Single-use semantics

Git checkpoints are single-use (a restore consumes the checkpoint). This
matches "rewind once, re-run the turn." Branching — trying several
alternatives from the same pre-turn snapshot via `Scope.Fork` — is
deferred (plan phase 7).

## Envelope diagnostics

`supervised_task` now returns these additional optional fields:

- `restores` — number of turn-level restores performed across all
  attempts (0 when turn checkpointing is off).
- `restored_from` — the checkpoint ID of the most recent restore.

These flow from `LoopState` through the context
(`jobs.WithTurnRestoreStats` / `jobs.RecordTurnRestore`) into the
uniform JSON envelope, keeping the `status`/`attempts`/`result`/`error`/
`partial` shape intact.

## Configuration (per-role shepherding)

Shepherding is configured **per role** under `agents.subagent.roles.<name>`
(except the trace directory, which stays global). Two booleans control it:

```yaml
agents:
  default:
    shepherd_trace_dir: ~/.yaah/traces/   # global — shared store location
    supervised_max_retries: 1             # global — attempt retries
    supervised_repo_path: ""              # global — repo to checkpoint
    turn_checkpoint_max: 0                # global — live turn-checkpoint cap; 0 = unlimited
    max_turn_restores: 3                  # global — turn restores per run; 0 = default (3)
  subagent:
    roles:
      developer:
        supervised: true          # → supervised_task ONLY; attempt checkpointing ON
        turn_checkpoints: false   # per-turn checkpoint/restore (default false)
      counter:
        supervised: false         # → spawn_subagent ONLY; no checkpoints (default)
```

**Routing is exclusive.** A role dispatches through exactly one tool:

- `supervised: false` (default) — plain sub-agent via `spawn_subagent`, no
  checkpointing. Hidden from `supervised_task`. (e.g. `counter`)
- `supervised: true` — routed only through `supervised_task`, which always
  applies attempt-level checkpoint/rollback/retry. Hidden from
  `spawn_subagent`. (e.g. `developer`)

`turn_checkpoints` (default `false`) additionally enables the loop-level
per-turn checkpoint/restore for that role. Numeric tuning
(`supervised_max_retries`, `turn_checkpoint_max`, `max_turn_restores`) and
`supervised_repo_path` remain global.

Per-turn checkpointing runs `git stash create` before every turn. It is
off by default because the measured overhead is significant (plan phase
9): on Windows, ~240–385 ms per checkpoint and up to ~1.5 s for a
restore on a 500-file repo, dominated by spawning three git subprocesses
per checkpoint. Enable only on repos where correctness outweighs that
throughput cost. See `internal/agent/runner/checkpoint_bench_test.go`
for the numbers.

---

# Appendix: Supervised Review Sessions

Interactive orchestrator-driven supervision on top of the attempt-level
tool. Plan of record: `.kilo/plans/1786988112765-supervised-review-fork.md`.

## What it adds

`supervised_task` gains `review: true`: instead of the automatic
rollback+retry loop, the tool runs ONE work unit and returns a **review
envelope** (bounded diff, sub-agent report, `session_id`, allowed
verdicts). The orchestrating agent then drives the verdict cycle via the
`supervisor` tool:

| Action | Effect |
|---|---|
| `continue` | Accept the unit; run the next one. Optional `guidance` becomes the next prompt. Conversation is seeded from the prior unit. |
| `rollback` | Restore the unit-start checkpoint — files **and** conversation rewind — then run the next unit from `guidance` (the more specific prompt). |
| `fork` | Restore the unit-start checkpoint, run `prompt_a` and `prompt_b` as two competing variants from that exact tree, capture both trees + diffs. |
| `choose` | Apply the winning variant's tree + conversation (`winner: "a"\|"b"`), discard the loser, resume the review cycle from a fresh checkpoint. |
| `review_diff` | Re-fetch the current diff/report without running anything. |
| `accept` | Keep the work, prune checkpoints, close the session. |
| `abort` | Rewind the unaccepted unit (or the fork), close the session. |

## Mechanics

- **Continuation is conversation-seeded.** Each dispatch carries the
  prior unit's message history via `jobs.SubAgentParams.SeedMessages`
  (runner → `LoopConfig.InitialMessages`). Rollback restores the
  checkpoint's serialized conversation, so a corrected unit continues
  from the last good point with full fidelity.
- **Tree states are non-consuming.** shepherd-kernel-go v0.3.x adds
  `Scope.CaptureTree` / `Scope.ApplyTree` (reusable stash+HEAD pairs)
  and `DiffSince` — fork/choose does not consume checkpoints the way
  restore does.
- **One session at a time.** Starting a second review session while one
  is open returns an error naming the active session. This matches the
  blocking-tool guarantee that supervised execution is sequential.
- **Cancellation is resumable.** A cancelled unit reports
  `status: "cancelled"` but keeps the session and checkpoint registered;
  the orchestrator can roll back or continue later.
- **Review mode never auto-retries.** A hard unit error or empty result
  surfaces in the envelope (`error` / `status: "empty"`) for the
  orchestrator to judge.

## Workspace custody

While a review session is open, the session owns the working tree:
`rollback` and `choose` overwrite it. Do not interleave unrelated edits
between verdicts — they will be clobbered. Accept or abort to release
custody.

## Config

No new config: review mode is a per-call `review: true` boolean on
`supervised_task`. Role routing (`supervised: true`) is unchanged.

## Kernel dependency

Requires shepherd-kernel-go ≥ v0.3.1 (TreeState/DiffSince + unique
trace intent IDs).
