# yaah Architecture

## High-level overview

```
┌──────────────────────────────────────────────────┐
│  CLI (cmd/yaah/)                                 │
│  root_cmd.go / tui.go / repl/                    │
│  ─────────────                                   │
│  Parses config, wires providers, builds tool     │
│  registry, creates agent.Loop, calls Run()       │
└──────────────────┬───────────────────────────────┘
                   │
┌──────────────────▼───────────────────────────────┐
│  agent.Loop                                      │
│  agent.go                                        │
│  ─────────────                                   │
│  Manages conversation history, iteration loop,   │
│  tool execution, streaming, context compaction,  │
│  hook events, approval gates.                    │
└──────────────────┬───────────────────────────────┘
                   │
     ┌─────────────┼─────────────┐
     ▼             ▼             ▼
┌─────────┐ ┌──────────┐ ┌──────────────┐
│Provider │ │Tools     │ │Middleware    │
│(LLM)    │ │Registry  │ │Pipeline      │
└─────────┘ └──────────┘ └──────────────┘
```

yaah is structured as a single Go binary. The entry point is `main.go`, which delegates to `cmd/yaah.Execute()` (cobra). The CLI layer (`cmd/yaah/`) wires together providers, tools, memory, MCP clients, and creates an `agent.Loop` for each conversation turn. The `internal/agent/` package contains all agent loop logic, including the middleware pipeline, tool execution, streaming, and context compaction.

---

## Agent loop

The agent loop lives in `internal/agent/agent.go`. The entry point is `Loop.Run(ctx, userInput)`. A `Loop` holds:

| Field | Purpose |
|---|---|---|
| `Provider` | LLM backend implementing `Provider` or `StreamProvider` |
| `Registry` | Tool registry mapping names to `Tool` implementations |
| `SystemPrompt` | Assembled system prompt (identity + env + project + memory) |
| `Model` | Model name string (e.g. `gpt-4o-mini`) |
| `MaxIterations` | Safety cap on loop turns (default 50) |
| `Messages` | Conversation history, persisted across multiple `Run()` calls |
| `ContextWindow` | Token budget for context compaction (0 = disabled) |
| `ApprovalMode` | `"allow"`, `"ask"`, or `"deny"` for destructive tools |
| `DB` | Optional SQLite database for per-message persistence (nil = in-memory only) |
| `MsgIdx` | Next message index for DB inserts, initialized to `len(Messages)` on resume |
| `OnToken` | Streaming token callback (set by CLI/TUI for display) |
| `OnTool` | Before/after tool execution callback |
| `OnSubAgent` | Sub-agent start/completion callback (fires for `task` tools) |
| `OnThinking` | Reasoning/thinking text callback (DeepSeek, Claude) |
| `OnFlush` | Callback when model finishes a streaming segment |
| `MaxToolConcurrency` | Cap on concurrent tool goroutines (0 = unlimited) |
| `MaxSubAgentConcurrency` | Cap on concurrent task tool calls (0 = unlimited, default 3) |
| `MaxSubAgentDepth` | SubAgentMiddleware cap on task calls per Loop |
| `MaxSubAgentDepthByRole` | Optional per-role caps; falls back to `MaxSubAgentDepth` |
| `PermissionRules` | Path-pattern rules for the `permission` middleware |
| `MCPServers` | Attached MCP servers whose tools are added to the registry |
| `Pipe` | Write stream during one-shot; nil in REPL/TUI |

### Agent loop (`runMiddleware`)

The agent loop runs via a single middleware pipeline. One iteration follows this sequence:

```
for iter := 0; iter < MaxIterations; iter++ {
    1. emitHook(TurnStart)
    2. Build Step{ Messages, Tools, Iteration, Model, SystemPrompt }
    3. pipeline.RunPrepareStep(ctx, step)      ← middleware hook
    4. getAssistantMessage(ctx, req)            ← LLM call (+ retry)
    5. persistMessage(msg)                      ← DB persistence (no-op if DB==nil)
    6. If no tool calls → return content        ← terminal
    7. If streamed → OnFlush(content)           ← flush streamed content
    8. pipeline.RunPostModel(ctx, &msg, step)   ← middleware hook
    9. executeAndCollect(ctx, toolCalls, &messages)  ← run tools, persist results
   10. pipeline.RunPostTool(ctx, results, step) ← middleware hook
   11. Update messages ← step.Messages
   12. Persist any new messages from middleware (e.g. compaction summaries)
}
```

**Key decisions:**

- `executeAndCollect` returns `[]ToolResult` so middleware can inspect tool outcomes.
- `l.Messages` is updated inside `executeAndCollect` (tool results are appended) AND after `RunPostTool` (middleware may have modified `step.Messages`). Both copies are kept in sync so `CompactionMiddleware` sees the latest messages.
- The `Step` struct is the mutable per-iteration state bag. Middleware reads and writes it freely.

### Dual-loop executor (always-on delegate)

File: `internal/agent/executor.go`, `internal/agent/agent.go`

yaah uses a planner/executor dual-loop where the planner delegates tool-intensive work to a dedicated executor running on a potentially cheaper model. The architecture is always-on: the `delegate` tool is always present alongside the full tool set (additive, no config gate). The planner chooses per-action whether to work inline or delegate.

**Key properties:**

- **Additive tool set**: `buildPlannerToolDefs()` returns `buildToolDefs() + delegateToolDef()` — always. The planner sees every tool AND `delegate`.
- **Per-action choice**: the planner decides whether to delegate a task or handle it inline. A single turn may contain both delegate and inline calls (mixed dispatch).
- **Executor owns tool selection**: when the planner calls `delegate(task, executor_type?)`, the executor selects, runs, and chains tools to accomplish the directive. The planner never names specific tools in a delegate call.
- **Recursion guard by schema**: `buildExecutorToolDefs()` returns `buildToolDefs()` only — `delegate` is never registered as a tool, so the executor structurally cannot delegate.
- **Model tiering**: `resolveExecutor(executorType)` returns the dedicated `ExecutorProvider`/`ExecutorModel` if configured, else falls back to the main provider/model.
- **Working directory injection**: the executor receives `## Working directory` via `os.Getwd()` in its payload, preventing the "different workspace" hallucination that caused wasted turns.
- **Auto-approval**: the executor runs tools via `executeOneTool` which bypasses the approval pipeline — tools like `bash` run without user prompts when delegated.

**Delegate call flow:**

```
1. Planner emits delegate(task="directive", executor_type="default")
2. splitDelegateCalls(msg.ToolCalls) → delegateCalls, inlineCalls
3. For each delegate call:
   a. parseDelegateCall(args) → directive, executorType
   b. lastUserMessage(messages) → originalIntent (forwarded to executor)
   c. runExecutor(ctx, directive, originalIntent, executorType):
      - resolveExecutor(executorType) → provider, model
      - Build messages: executorSystemPrompt + UserMsg(directive + intent + workdir)
      - Loop up to MaxInnerIterations:
        * getExecutorMessage(ctx, provider, req) — streaming or REST
        * Execute tools via executeOneTool (no approval checks)
        * Feed results back to executor for chaining
      - Return summary, exhausted, error
   d. wrapExecutorResult(summary, exhausted, err, truncated) →
      <executor_result state="completed|error|exhausted" truncated="true|false">…</executor_result>
   e. Inject as tool message (Role=tool, Name=delegate, ToolCallID=delegateCall.ID)
4. If only delegate calls (no inline): continue → next iteration (planner sees results)
5. If inline calls exist: executeAndCollect → normal middleware pipeline
```

**Structured result envelope:**

The executor's summary is wrapped in an XML envelope for programmatic state detection:

```xml
<executor_result state="completed" truncated="false">
  glob: 47 files; bash(wc -l): 7452 lines
</executor_result>
```

States: `completed`, `error` (executor hit a fatal error), `exhausted` (hit MaxInnerIterations). The `truncated` flag indicates the summary exceeded `maxInnerSummaryLen` (8000 chars) and was trimmed.

**Token attribution:**

Each inner.loop span carries `inner.prompt_tokens`, `inner.completion_tokens`, and `inner.iterations` — the executor's token usage, isolated from the planner. Each agent.turn span carries `turn.prompt_tokens` and `turn.completion_tokens` (per-turn deltas) alongside cumulative `llm.total_*`. Dispatch events (`dispatch.delegate`, `dispatch.inline`) log the tool names and counts at each routing decision.

**Benchmarks documented in** [`docs/BENCHMARK.md`](./BENCHMARK.md). Complex tasks push ~65% of tokens to the executor on a 3x cheaper model.

### LLM call with retry (`getAssistantMessage`)

Both paths use the same `getAssistantMessage` method, which:

1. Checks if the provider implements `StreamProvider`. If so and `OnToken` is set, uses streaming via `runStream()`.
2. Otherwise uses synchronous `Provider.Send()`.
3. On failure, retries with exponential backoff (`RetryBackoff * 2^attempt`) up to `MaxRetries`.
4. Rejects responses where `finish_reason == "length"` and tool calls are present (truncated tool calls are unsafe to execute).

---

## Middleware pipeline

File: `internal/agent/pipeline.go`, `internal/agent/middleware.go`

### Middleware interface

```go
type Middleware interface {
    Name() string
    PrepareStep(ctx context.Context, step *Step) (*Step, error)
    PostModel(ctx context.Context, msg *types.Message, step *Step) (*Step, error)
    PostTool(ctx context.Context, results []ToolResult, step *Step) (*Step, error)
}
```

Three hook points in each iteration:

| Hook | Called | Purpose |
|---|---|---|
| `PrepareStep` | Before LLM call | Message injection (steer, follow-ups), compaction |
| `PostModel` | After LLM response, before tool execution | Inspect/modify the assistant message |
| `PostTool` | After all tools complete | Inspect results, detect loops, modify messages |

The `Step` struct is the shared mutable state:

```go
type Step struct {
    Messages     []types.Message
    Tools        []types.ToolDef
    Iteration    int
    Model        string
    SystemPrompt string
}
```

### Pipeline architecture

`Pipeline` wraps an ordered list of `Middleware`. Each hook method iterates through every middleware in registration order. Errors are wrapped with the middleware name and halt the pipeline.

The middleware list is resolved from the `builtinMiddleware` registry (a `map[string]MiddlewareBuilder` in `middleware.go`). Each builder is a `func(l *Loop) Middleware` that constructs the implementation from `Loop` fields. The `resolvedPipelineNames()` function applies the `enabled`/`disabled` config overrides to produce the final ordered list.

```go
func (p *Pipeline) RunPrepareStep(ctx context.Context, step *Step) (*Step, error) {
    for _, mw := range p.middleware {
        step, err = mw.PrepareStep(ctx, step)
        if err != nil { return step, fmt.Errorf("%s: %w", mw.Name(), err) }
    }
    return step, nil
}
```

The pipeline is built in `buildPipeline()` with a config-driven order:

1. `SteerMiddleware` — high-priority mid-turn messages
2. `FollowupMiddleware` — queued between-turn messages
3. `CompactionMiddleware` — context window enforcement
4. `ApprovalMiddleware` — no-op placeholder (approval moved to `executeAndCollect`)
5. `LoopDetectionMiddleware` — detect stuck loops

The default set is defined in `defaultPipelineNames` in `middleware.go`. Users can override which middleware runs via `config.yaml`:

```yaml
agent:
  middleware:
    enabled: [steer, followup, compaction, loop_detection]
    disabled: [approval]
```

If `enabled` is set, only those middleware run (in the specified order). If `enabled` is empty, the default set runs minus any names in `disabled`. Middleware not registered in `builtinMiddleware` is silently skipped.

Custom middleware can be injected via `Loop.Middleware`. If set, `buildPipeline()` uses the custom list instead of the config-driven chain.

### Individual middleware

#### SteerMiddleware (`middleware_steer.go`)

Drains the `Loop.Steer` channel in `PrepareStep`. Messages are prepended with `[STEER]` and injected as user messages. If a compaction hook is provided, it runs after injection.

#### FollowupMiddleware (`middleware_followup.go`)

Drains the `Loop.FollowUps` channel in `PrepareStep`. Messages are injected as user messages. This is used for queuing follow-up prompts while the agent is busy.

#### CompactionMiddleware (`middleware_compaction.go`)

Triggers context compaction at both `PrepareStep` (preflight) and `PostTool` (post-iteration) hooks when `ContextWindow > 0`. Delegates to `Loop.compactContext(ctx, threshold)`. The `threshold` parameter (default 0.8) controls what fraction of the window triggers compaction. The middleware now accepts a configurable `CompactionThreshold` from the `Loop` struct.

#### ApprovalMiddleware (`middleware_approval.go`)

All three hooks are no-ops. Approval logic was moved into `executeAndCollect`. The middleware struct is retained for future use and pipeline ordering.

#### LoopDetectionMiddleware (`middleware_loopdetect.go`)

Tracks a sliding window of SHA-256 hashes of `(tool_name, tool_result)` pairs. If any hash appears `count` or more times within the last `window` executions, the pipeline halts with an error. Uses the package-level `toolCallHash()` function.

#### PermissionMiddleware (`middleware_permission.go`)

Filters tool calls in `PostModel` based on path-pattern rules. Rules are configured as `PermissionRule` structs with `Tool` (name), `Path` (glob), and `Mode` (allow/deny). Denied tool calls are silently removed from the assistant message before execution. Rule matching uses `filepath.Match` for glob support. Paths are extracted from tool arguments by key (`path`, `filePath`, or `command` for shell tools).

#### ToolConcurrencyMiddleware (`middleware_toolconcurrency.go`)

Limits concurrent tool goroutines via a buffered channel semaphore. When `MaxToolConcurrency > 0`, `buildPipeline()` initializes the semaphore and `executeAndCollect` acquires/releases it around each tool goroutine. The middleware itself has no-op hooks — the semaphore lives on `Loop.toolSem`.

#### SubAgentMiddleware (`middleware_subagent.go`)

Enforces sub-agent depth limits and lifecycle tracking. Two depth mechanisms are supported:

1. A legacy cumulative `MaxDepth` that caps the total number of `task` calls a single `Loop` may issue across its lifetime.
2. An optional per-role `MaxDepthByRole` map that caps `task` calls per role independently. A role absent from the map falls back to `MaxDepth`.

`PostModel` walks the assistant message's tool calls, parses each `task` call's `role` argument, and drops any `task` call whose role budget is exhausted (a system notice is injected). Non-task calls are always preserved.

Actual nesting depth (planner → worker → …) is bounded structurally rather than by this middleware alone: only the `planner` role registers the `task` tool, and `makeTaskRunner` decrements the remaining depth on each level so a sub-loop eventually loses its `task` tool entirely (see [Sub-Agent Lifecycle](#sub-agent-lifecycle)).

Sub-agent concurrency is **not** enforced here — it lives on `Loop.subAgentSem` (see `executeAndCollect`), mirroring how `ToolConcurrencyMiddleware` relates to `Loop.toolSem`.

#### PromptCachingMiddleware (`middleware_promptcaching.go`)

Injects Anthropic `cache_control: {type: "ephemeral"}` breakpoints on system messages and tool results in `PrepareStep`. When `PromptCaching` is `true`, the system message and the last tool result before a user turn are marked for caching. The `CacheControl` field uses `omitempty` so it is a no-op for non-Anthropic providers.

---

## Sub-agent lifecycle

Files: `internal/agent/subagent.go`, `internal/agent/role_def.go`, `internal/tools/task.go`, `cmd/yaah/root_cmd.go`, `internal/agent/middleware_subagent.go`

The `task` tool spawns a sub-agent: a fresh `agent.Loop` with a curated tool registry, its own iteration budget, deadline, and system prompt. Sub-agents let the parent agent delegate isolated work and fan out independent subtasks in parallel.

### Roles and tool profiles

Each sub-agent runs under a **role** that selects its tool set and default limits.

| Role | Tools | Max iterations | Default timeout | Can spawn |
|---|---|---|---|---|
| `worker` | read, write, edit, delete, grep, glob, ls, bash, powershell, webfetch | 25 | 120s | no |
| `reviewer` | read, grep, glob, ls | 10 | unlimited | no |
| `planner` | worker set + `task` | 50 | 300s | yes |
| _(default)_ | full built-in set (legacy) | — | — | depth permitting |

`RoleProfileFor(role)` delegates to the global `RoleRegistry` if one has been installed via `SetDefaultRoleRegistry`; otherwise it falls back to hardcoded legacy profiles in `legacyProfileFor`. `RoleDefault` returns a zero-value profile, signalling `makeTaskRunner` to use the legacy full tool set.

### RoleRegistry

File: `internal/agent/role_def.go`

The `RoleRegistry` is the central store for role definitions. It holds built-in roles (embedded in the binary) and user-defined roles (discovered from the filesystem at startup), with built-in roles taking precedence on name conflict.

**Core types:**

- `RoleDef` — the persistent format for a role, parsed from YAML frontmatter: `Tools []string`, `MaxIterations`, `Timeout` (seconds), `MaxDepth`, and `Body` (the markdown guidance text). Its `ToProfile()` method converts to the runtime `RoleProfile` struct.
- `RoleRegistry` — a `sync.RWMutex`-protected `map[SubAgentRole]RoleDef`. Thread-safe for concurrent reads during sub-agent dispatch.

**Loading built-in roles:**

Built-in roles are embedded via `//go:embed roles/*.md` in `internal/prompts/prompts.go` as `BuiltinRolesFS` (an `embed.FS`). At startup, `builtinRoleFiles()` in `cmd/yaah/root_cmd.go` reads the directory and passes each file's content to `reg.LoadBytes(files)`. The file name minus `.md` becomes the role name (e.g. `worker.md` → `"worker"`).

**Loading user-defined roles:**

`roleSearchPaths(cwd)` returns directories to scan: every `.agents/roles/` directory walked up from `cwd`, then `~/.agents/roles/`. `reg.LoadDir(dir)` reads every `.md` file in each directory, parses it with `parseRoleFile`, and adds the role — but only if the role name isn't already registered (built-in always wins).

**Parsing: `parseRoleFile`** splits the markdown at the first `---\n...\n---` YAML frontmatter block, unmarshals the YAML portion into `RoleDef`, and stores the remaining markdown in `Body`. It returns an error on missing or unterminated frontmatter.

**Atomic installation:**

`SetDefaultRoleRegistry` stores the loaded registry in a package-level `atomic.Pointer[RoleRegistry]` in `subagent.go`. `RoleProfileFor` and `RoleGuidance` read from this pointer; when it's `nil` (e.g. in unit tests), they fall back to hardcoded `legacyProfileFor` / `legacyGuidance`.

**Key methods:**

| Method | Purpose |
|---|---|
| `LoadBytes(files map[string][]byte)` | Parse and register built-in roles (takes precedence) |
| `LoadDir(dir string)` | Discover and register user-defined roles (skipped if name already registered) |
| `ProfileFor(role SubAgentRole) RoleProfile` | Return runtime profile; zero-value for `RoleDefault` or unknown |
| `Guidance(role SubAgentRole) string` | Return role-specific system-prompt text (the markdown body) |
| `Names() []string` | All registered role names, for dynamic schema generation |

### Dynamic task tool schema

File: `internal/tools/task.go`

`BuildTaskSchema(roleNames []string) json.RawMessage` constructs the `task` tool's JSON Schema at startup from the active role list, using `encoding/json` marshalling to avoid injection. The `TaskTool.Schema()` method checks `RoleNames`: when non-empty it calls `BuildTaskSchema`; when empty it returns a legacy static schema with `["worker", "reviewer", "planner"]`. This means user-defined roles are only visible to the model when a registry is configured (the default for both CLI and TUI sessions).

Wiring chain: `reg.Names()` → `newTaskTool(…, roleNames)` → `TaskTool.RoleNames` → `TaskTool.Schema()`.

### CLI lifecycle display

Files: `internal/agent/agent.go`, `cmd/yaah/root_cmd.go`

Sub-agent activity is rendered with `╭─` / `╰─` box-drawing corners in the CLI, distinct from ordinary tool calls.

**Callbacks:**

- `SubAgentInfo` struct — `Role`, `Prompt` (abbreviated description), `Duration` (0 = start), `Error`.
- `SubAgentCallback` — `func(info SubAgentInfo)`. Set as `Loop.OnSubAgent`.
- `OnTool` for `task` tools is suppressed (line 484-486 of `root_cmd.go`) — all task lifecycle rendering goes through `OnSubAgent`.

**Emission:** In `executeAndCollect`, when a tool named `"task"` is about to run, `parseTaskArgs` extracts the `role` and `description` from tool arguments JSON. `OnSubAgent` fires with `Duration=0` (start marker). After execution, it fires again with the actual duration and any error.

**Rendering:** The REPL's `runPrompt` sets `OnSubAgent` to print:
```
╭─ sub-agent: worker — List directory contents
╰─ sub-agent: worker — completed (6.8s)
```
If a sub-agent errors, status shows the error string (styled in yellow) instead of "completed".

### Sub-agent tool callbacks

File: `cmd/yaah/root_cmd.go`

Sub-agent tool calls are displayed indented beneath the parent's activity.

**`taskRunnerOpts.SubToolCallback`** is a `ToolCallback` field captured in the `taskRunnerOpts` closure and set on every sub-loop's `OnTool` via `makeTaskRunner`. The callback, `subToolDisplay`, renders with 4-space indent (vs. the parent's 2-space) and 40-char arg abbreviation (vs. the parent's 60-char):

```
    tool: glob({"pattern": "**/*.go",...}) (19ms)
```

Since `taskRunnerOpts` is reused for all nesting levels, every sub-agent (regardless of depth) uses the same callback. The indentation is achieved by the callback itself, not by positional tracking.

### TUI lifecycle display

Files: `cmd/yaah/tui.go`, `internal/tui/tui.go`, `internal/tui/render.go`

The TUI mirrors the CLI's bracketed sub-agent display through its own event model.

**Data flow:** `OnSubAgent` in `runAgentForTUI` translates `agent.SubAgentInfo` into `AgentMsg` fields (`SubAgentStart`/`SubAgentEnd`, `SubAgentRole`, `SubAgentLabel`, `SubAgentDur`, `SubAgentErr`). These are sent over the `agentCh` channel to the TUI event loop.

**`HandleAgentMsg`** converts sub-agent events into `Message` entries: `"subagent-start"` role with the task label (e.g. `"worker — List files"`), and `"subagent-end"` role with the completion label (e.g. `"worker — completed (6.8s)"`). The `SubAgentBracket` component renders these as `╭─` / `╰─` container corners (see [TUI component system](./tui-components.md)).

**`renderMessages`** handles `"subagent-start"` and `"subagent-end"` roles with `subAgentStartStyle` (bold tool color) and `subAgentEndStyle` (tool color). The task tool header in the `"tool"` case uses `matchJSONField` to extract `role` and `description` from tool args JSON, displaying e.g. `"sub-agent: worker — List files"` instead of the generic `"sub-agent"`.

### Timeout enforcement

`TaskTool.Execute` (`internal/tools/task.go`) wraps the runner call in `context.WithTimeout`. The effective timeout is the per-call `timeout_seconds` argument (10–600), falling back to the resolved role default, then the tool's `DefaultTimeout`. On `DeadlineExceeded` or `Canceled` the tool returns a **structured JSON result** (`{"error":"timed out","partial":"..."}`) instead of a Go error, so the parent agent can decide whether to retry, continue, or report. Any non-empty string returned alongside the context error is surfaced as `partial`.

### Parallel dispatch and concurrency cap

Multiple `task` tool calls issued in a single turn are dispatched concurrently by `executeAndCollect` like any other tool. Sub-agent fan-out is bounded independently by `Loop.MaxSubAgentConcurrency` (default 3), realised as a separate `subAgentSem` semaphore acquired only for tool calls named `task`. This lets a planner dispatch many workers without exceeding the configured concurrency, and is independent of `MaxToolConcurrency`.

### Interrupt propagation

The parent context flows through `executeAndCollect` → `TaskTool.Execute` → `Runner` → `subLoop.Run`. Cancelling the parent (e.g. Ctrl-C) propagates via the context: each sub-agent's `Loop.Run` checks `ctx.Done()` at the top of every iteration and the underlying provider HTTP call respects the context. A cancelled sub-agent returns the structured cancellation result described above rather than failing the parent turn.

Semaphore acquisitions in `executeAndCollect` (`subAgentSem` and `toolSem`) use `select` on `ctx.Done()` alongside the channel send, so goroutines queued on concurrency caps immediately return a cancellation result instead of blocking until a slot opens.

### Nesting depth

Two mechanisms bound nesting:

1. **Structural**: only the `planner` role registers the `task` tool. A `worker` or `reviewer` sub-agent physically cannot spawn further sub-agents.
2. **Depth budget**: `makeTaskRunner` receives a `remainingDepth` counter (seeded from `subagent.max_depth`, default 3). Each nested `task` tool is built with `remainingDepth-1`; when it reaches zero the `task` tool is omitted from the sub-loop's registry entirely. This bounds planner → planner chains without relying on runtime checks.

The `SubAgentMiddleware` additionally caps the number of `task` calls a single `Loop` may issue (globally via `MaxSubAgentDepth`, or per-role via `MaxSubAgentDepthByRole`).

### Configuration

```yaml
agent:
  subagent:
    max_depth: 3          # global nesting depth (default)
    max_concurrency: 3    # simultaneous task calls per iteration
    default_timeout: 120  # seconds; used when no role default applies
    roles:
      worker:
        timeout: 120
        max_iterations: 25
      reviewer:
        timeout: 60
        max_iterations: 10
      planner:
        timeout: 300
        max_iterations: 50
```

### Agent conflict reconciliation

Files: `internal/tools/conflict_tracker.go`, `internal/tools/recording.go`, `internal/agent/agent.go`

When a parent agent dispatches multiple parallel worker sub-agents in a single turn, those workers may independently modify the same files. If worker B's write clobbers worker A's changes, the parent has no way of knowing without explicit conflict detection.

**Tracker:** `ConflictTracker` is a thread-safe `[]FileRecord` store on `Loop`. Sub-agent write/edit/delete tools are wrapped with `RecordingTool` (via `buildSubAgentRegistry`), which extracts the `filePath` from JSON args and records `(subAgentLabel, filePath, toolName)` to the shared tracker.

**Label propagation:** `TaskTool.Execute` builds a label from the task's `role` and `description` (e.g. `"worker — Add login handler"`) and injects it into the request context via `WithConflictLabel()`. `RecordingTool.Execute` reads this label from the context, so each record is tagged with which sub-agent produced it.

**Detection:** After `executeAndCollect` returns (all parallel tasks complete), `runMiddleware` calls `ConflictTracker.DetectAndReset()`. Files touched by more than one distinct sub-agent label are flagged. If conflicts are found, a structured `CONFLICT:` user message is injected into the conversation before the next LLM call. The report lists each conflicting file, which sub-agents touched it, and which tools they used.

**Observability:** Two hook event types are emitted: `conflict.check` (every turn when a tracker is present) and `conflict.detect` (when conflicts are found, with `conflict_files` count). When OTel is enabled, a `conflict.check` span appears in the trace waterfall as a child of the turn span.

---

## Tool execution

### Registry

File: `internal/tools/tools.go`

`NewRegistry()` pre-registers 15 built-in tools:

| Tool | Category | Dangerous |
|---|---|---|
| `read` | Filesystem | No |
| `write` | Filesystem | Always |
| `edit` | Filesystem | Always |
| `replace` | Filesystem | Always |
| `delete` | Filesystem | Always |
| `json_query` | Filesystem | Per-action |
| `grep` | Search | No |
| `glob` | Search | No |
| `ls` | Filesystem | No |
| `bash` | Shell | Always |
| `powershell` | Shell | Always |
| `git` | VCS | Per-action |
| `question` | Interactive | No |
| `webfetch` | Network | No |

Additional tools are registered by the CLI layer after `NewRegistry()`:
- `memory_search`, `memory_add`, `memory_delete`, `memory_update`, `memory_search_sessions`
- `skill`
- `todowrite`
- `background_process`
- `task`
- Any MCP tools from connected servers

### Tool execution flow (`executeAndCollect` — middleware path)

```
executeAndCollect(ctx, calls, messages):
  for each tool call:
    1. If ApprovalMode=="deny" && dangerous → emit hooks, return error
    2. If ApprovalMode=="ask" && dangerous → prompt user, deny if rejected
    3. Spawn goroutine:
       a. Call OnTool (before) — Duration=0
       b. emitHook(ToolStart)
       c. Registry.Execute(ctx, name, args)
       d. Truncate result to ToolResultMaxLen (8192 chars) if needed
       e. Call OnTool (after) — with Duration, Result, Error
       f. emitHook(ToolEnd)
       g. Send toolExecResult to channel
    4. Collect results in order
     5. Build []ToolResult for middleware
     6. Append tool result messages to *messages
```

### Dangerous tools

Tools implement the `DangerClassifier` interface (`internal/tools/tools.go`) to
declare whether they require user approval:

```go
type DangerClassifier interface {
    IsDangerous(argsJSON string) bool
}
```

Tools that always require approval (`BashTool`, `PowerShellTool`, `WriteTool`,
`EditTool`, `DeleteTool`, `ReplaceTool`) return `true` unconditionally. Tools with
argument-level classification (`GitTool`) inspect their JSON arguments — `add`
and `commit` are dangerous; `status`, `diff`, `log`, etc. are not. Tools that
are never dangerous (`ReadTool`, `GrepTool`, `GlobTool`, etc.) simply don't
implement the interface.

The `Loop.classifyDanger(name, args)` method in `agent.go` checks whether a
tool implements `DangerClassifier` and calls `IsDangerous` when present.
Approval checks (`executeAndCollect` at line 570) use this single code path
for all tools.

All file-path-accepting tools support `~` expansion via `expandHomeDir()`
in `helpers.go`. Shared tool utilities (`rgAvailable`, `commonIgnoreDirs`,
`truncateOutput`) also live there.

---

## Context compaction

File: `internal/agent/agent.go` (`compactContext`, `trimContext`, `pruneMessages`, `messageTokens`)

yaah uses a multi-layered context management system to prevent conversation
history from exceeding the model's context window. Compaction is triggered
proactively when estimated tokens reach 50% of `ContextWindow` (with a 64K
minimum floor), or reactively when the provider signals context overflow.

### Token estimation

Primary: API-reported `LastPromptTokens` from the most recent model call.
Fallback: `EstimatedTokens()` which uses a `len(content) / 4` heuristic.

### Pre-compaction pruning (`pruneMessages`)

Before the LLM summarizer call, old messages outside the preserved tail are
pruned to reduce token load:

- **Tool outputs** (>2K chars): Replaced with compact markers like
  `[tool grep output — 142 lines, 8192 chars]`
- **Assistant summaries** (>2K chars, no tool calls): Truncated with
  head+tail preservation (⅔ head + ⅓ tail)

This reduces the summarizer's input by ~80% while preserving enough
information for a useful summary.

### Preservation budget

Instead of a fixed message count, yaah uses a **token-budget approach**:
keep as many recent messages as fit in 20% of the context window, with:

- A 2K-token minimum floor (capped at half the window)
- A 4-message hard minimum (always keep at least 2 exchanges)
- **Boundary alignment**: never split inside a tool-call/response group
- **User-message anchor**: the most recent user message is always in the
  tail, preventing the active task from being summarized away

### LLM-powered compaction (`compactContext`)

1. Check anti-thrashing guard: skip if last 2 compactions each saved <10%.
2. Check token budget: if `estimatedTokens <= ContextWindow * threshold`, return.
3. Compute the preservation split via token budget + boundary alignment.
4. Prune old messages via `pruneMessages()`.
5. Send pruned history to the LLM summarizer with a structured prompt
   (`## Goal`, `## Completed Work`, `## Active Work`, `## Pending Tasks`,
   `## Key Decisions`, `## Files Modified`), capped at 4,096 output tokens
   via `max_tokens`.
6. On re-compaction (second+), passes the previous summary to the LLM and
   asks it to **update** rather than re-summarize from scratch, preserving
   continuity across multiple compactions.
7. Replace old messages with a system message containing the summary.
8. Track compaction effectiveness: if savings <10%, increment anti-thrashing
   counter. After 2 consecutive ineffective compactions, skip further
   compaction.
9. If the LLM call fails or returns empty, fall back to `trimContext()`.

### Inner executor loop summary cap

The dual-loop architecture's inner executor appends a summary to the outer
conversation each turn. These summaries are capped at 2,000 chars with
head+tail preservation to prevent unbounded context growth from the inner
loop.

### Overflow recovery

When the provider returns a context overflow error, the retry loop in
`getAssistantMessage` triggers an aggressive compaction at 40% of the window
(up to 3 attempts). A pre-flight guard also checks `LastPromptTokens >
ContextWindow` before each LLM call as a last-resort safety net.

### Truncation fallback (`trimContext`)

Removes oldest messages one at a time until the token budget is met. Always
preserves the system message at index 0.

---

## Session persistence

File: `internal/agent/agent.go` (`persistMessage`)

When `Loop.DB` is non-nil, every message is persisted to SQLite as it is
appended to the conversation. This enables session resume across process
restarts and crash recovery.

### Persistence points

| Point | What | Where |
|---|---|---|
| First turn | System prompt + user message | `runMiddleware`, after message construction |
| Subsequent turns | User message only | `runMiddleware`, before loop entry |
| Assistant response | Content + tool calls (serialized as JSON) | `runMiddleware`, after `getAssistantMessage` |
| Tool results | Result content, call ID, tool name | `executeAndCollect`, after each tool execution |
| Compaction summaries | New synthetic system messages | `runMiddleware`, after `step.Messages` assignment |

### Message conversion

`types.Message` → `memory.Message` mapping:

- `Content`: serialized as-is; empty assistant content with tool calls gets a synthetic `[tool:name] args` representation
- `ToolCalls`: serialized as JSON array; deserialized on load
- `ToolCallID`: preserved for tool result messages (required for OpenAI round-trip)
- `ToolName`: the tool name for tool result messages

### Resume flow

```
yaah --resume <session-id> "continue"
  → newAgentSession loads messages from DB
  → Populates s.messages with the full conversation history
  → If last message is an assistant message with pending tool calls,
    injects an interruption notice as a system message
  → runPrompt sets loop.MsgIdx = len(s.messages) so new messages
    get the correct DB indices
  → Loop runs normally, appending new messages to both memory and DB
```

### Error handling

`persistMessage` logs errors to stderr (`warning: db persist: ...`) but never
returns them. The agent loop continues even if the database is unavailable.
`MsgIdx` is not incremented on error, so the failed message slot is skipped.

---

## Hook events

File: `internal/agent/hookevent.go`

yaah emits structured JSONL events to `<HookDir>/<session-id>.jsonl` for external agent integrations (e.g. entire-agent-yaah). Events are fire-and-forget — failures are silent and never break the loop.

### Event types

| Event | Emitted when |
|---|---|
| `session.start` | First `Run()` call for a new session |
| `session.end` | `Run()` returns (with `exit_reason` field) |
| `turn.start` | Start of each iteration (with `turn` number) |
| `tool.start` | Before each tool execution (with `tool_name`, `tool_args`) |
| `tool.end` | After each tool execution (with `tool_result`, `duration_ms`, `tool_error`) |
| `conflict.check` | Every turn when a `ConflictTracker` is present |
| `conflict.detect` | When parallel workers touched the same files (with `conflict_files` count) |

### HookEvent struct

```go
type HookEvent struct {
    Event      HookEventType `json:"event"`
    SessionID  string        `json:"session_id"`
    Timestamp  int64         `json:"timestamp_ms"`
    Model      string        `json:"model,omitempty"`
    Prompt     string        `json:"prompt,omitempty"`
    Turn       int           `json:"turn,omitempty"`
    ToolName   string        `json:"tool_name,omitempty"`
    ToolArgs   string        `json:"tool_args,omitempty"`
    ToolResult string        `json:"tool_result,omitempty"`
    DurationMs int64         `json:"duration_ms,omitempty"`
    ToolError    string        `json:"tool_error,omitempty"`
    ExitReason   string        `json:"exit_reason,omitempty"`
    ConflictFiles int          `json:"conflict_files,omitempty"`
}
```

---

## Streaming

File: `internal/agent/agent.go` (`runStream`, `assembleStreamed`)

When the provider implements `StreamProvider` and `OnToken` is set, `getAssistantMessage` uses `runStream()`. The method:

1. Reads from a `<-chan StreamChunk`.
2. Accumulates `delta.Content` into a string builder, emitting each chunk via `OnToken`.
3. Accumulates `delta.ReasoningContent` separately, emitting via `OnThinking`.
4. Assembles tool calls from streamed deltas (by index), ordering them before returning.
5. Rejects truncated streams (`finish_reason == "length"` with pending tool calls).

Callbacks available on `Loop`:

| Callback | Signature | Purpose |
|---|---|---|
| `OnToken` | `func(string)` | Each streamed content delta |
| `OnThinking` | `func(string)` | Reasoning/thinking content deltas |
| `OnTool` | `func(ToolInfo)` | Called twice per tool: before (Duration=0) and after |
| `OnSubAgent` | `func(SubAgentInfo)` | Called twice per sub-agent: start (Duration=0) and completion |
| `OnFlush` | `func(string)` | Flush accumulated streamed content before next tool/iteration |

---

## Provider interface

File: `internal/agent/agent.go`

```go
type Provider interface {
    Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error)
}

type StreamProvider interface {
    Provider
    SendStream(ctx context.Context, req types.ChatRequest) (<-chan providers.StreamChunk, <-chan error)
}
```

The `internal/providers/` package implements OpenAI Chat Completions-compatible clients. Providers are resolved by the CLI layer from `~/.yaah/config.yaml` and passed to `Loop.Provider`.

---

## Callback notification design

`OnTool` is called twice per tool execution (before and after), giving callers two opportunities:

1. **Before** (`Duration == 0`, `Error == ""`): The caller can display `"tool: bash(args...)"` while the tool runs.
2. **After** (`Duration > 0`): The caller can display the duration and any error.

The `ToolInfo.Duration == 0` check is the canonical way to distinguish before vs. after. The CLI uses this in `root_cmd.go` to show:
- Before: `"  tool: bash(args)"`
- After: `" (1.2s)\n"`

---

## TUI architecture

Files: `cmd/yaah/tui.go`, `internal/tui/`

The TUI is a [bubbletea](https://github.com/charmbracelet/bubbletea) application running the agent loop in a background goroutine. Communication between the agent goroutine and the TUI model goes through a typed `chan AgentMsg` channel.

**Data flow:** Agent events (tokens, tool starts/results, sub-agent lifecycle, thinking text) are converted to `AgentMsg` values by the callbacks in `runAgentForTUI` and sent over the channel. The TUI's `HandleAgentMsg` maps each `AgentMsg` to state transitions on `Model`: appending `Message` entries, toggling spinner state, updating the status bar, or setting approval modals.

**Message rendering:** `renderMessages` in `render.go` switches on `Message.Role` (`"user"`, `"assistant"`, `"tool"`, `"subagent-start"`, `"subagent-end"`, `"system"`). Tool messages are collapsible (▶/▼), styled by `toolStyle`, and display a header extracted from `ToolName` and `ToolArgs`. Sub-agent brackets use `subAgentStartStyle` (bold) and `subAgentEndStyle`.

**Command palette:** Typing `:` in the TUI opens a command palette (`:help`, `:clear`, `:compact`, `:banner`, `:model`, `:quit`). `:model` queries providers' model lists and filters live.

The TUI renders through a component system — stateless renderers for
messages, expandables, palettes, header, and status bar, all styled from the
shared theme. Full reference: [TUI component system](./tui-components.md).

---

## OpenTelemetry observability

Files: `internal/observability/`, `internal/agent/agent.go`, `internal/config/load.go`

yaah emits traces via OTLP gRPC to any OpenTelemetry-compatible backend. Tracing is off by default; enable with `observability.otel.enabled: true` in `config.yaml`.

### Initialisation

`observability.Setup(ctx, cfg)` in `internal/observability/otel.go` creates a `TracerProvider` (and optionally a `MeterProvider`) configured for the OTLP endpoint. It returns a `shutdown` function that the CLI (`root_cmd.go`) and TUI (`tui.go`) defer. Both entrypoints set `Loop.OtelEnabled` so individual spans are gated on the config flag.

### Span hierarchy

| Span type | Location | Contents |
|---|---|---|
| `tool.<name>` | `executeAndCollect` in `agent.go` | Operation name is the tool name. `tool.args` attribute (200-char truncated JSON). `result` event with truncated output. Errors are recorded via `RecordError`. |
| `subagent: <role> — <description>` | `executeAndCollect` in `agent.go` | Role + task description in the operation name. `dispatched` event with role and task text. `completed` / `failed` event on finish. |
| `llm.chat` | `InstrumentedProvider.Send` in `observability/provider.go` | `tokens` event with `llm.prompt_tokens`, `llm.completion_tokens`, `llm.total_tokens`, `llm.messages`, and `llm.system_len`. `llm.duration_ms` attribute. |
| `conflict.check` | `runMiddleware` in `agent.go` | Child of the turn span. `conflict.files` attribute. When conflicts are found, a `conflict.detected` event is added with the file count. |

### Propagation to sub-agents

`Loop.OtelEnabled` is propagated through `taskRunnerOpts.OtelEnabled` → `makeTaskRunner` → `subLoop.OtelEnabled`. The parent span context flows via `context.Context` through `Registry.Execute(runCtx, ...)`, so sub-agent tool spans appear as children of the sub-agent span in the trace waterfall.

### Configuration

```yaml
observability:
  otel:
    enabled: false
    endpoint: "localhost:4317"
    service_name: "yaah"
    traces: true
    metrics: false
```

The OTel SDK also honours standard environment variables for sampling, TLS, and resource attributes (`OTEL_RESOURCE_ATTRIBUTES`, `OTEL_TRACES_SAMPLER`, etc.).

A Docker-based Jaeger setup and trace interpretation guide is at [`docs/otel-setup.md`](./otel-setup.md).

---

## Message types

File: `internal/types/types.go`

```go
type Message struct {
    Role       string     `json:"role"`
    Content    string     `json:"content,omitempty"`
    ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
    ToolCallID string     `json:"tool_call_id,omitempty"`
    Name       string     `json:"name,omitempty"`
}
```

Helper constructors: `SystemMsg(content)`, `UserMsg(content)`, `ToolResultMsg(callID, name, content)`.

Tool definitions sent to the LLM use `ToolDef`/`ToolFn` structs built by `buildToolDefs()` from the registry.
