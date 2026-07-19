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
|---|---|
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

Enforces sub-agent depth limits. Tracks `depth` across iterations and blocks `task` tool calls when `depth >= MaxSubAgentDepth`. Denied task calls are removed from the message and a system notice is injected.

#### PromptCachingMiddleware (`middleware_promptcaching.go`)

Injects Anthropic `cache_control: {type: "ephemeral"}` breakpoints on system messages and tool results in `PrepareStep`. When `PromptCaching` is `true`, the system message and the last tool result before a user turn are marked for caching. The `CacheControl` field uses `omitempty` so it is a no-op for non-Anthropic providers.

---

## Tool execution

### Registry

File: `internal/tools/tools.go`

`NewRegistry()` pre-registers 12 built-in tools:

| Tool | Category | Destructive |
|---|---|---|
| `read` | Filesystem | No |
| `write` | Filesystem | Yes |
| `edit` | Filesystem | Yes |
| `delete` | Filesystem | Yes |
| `grep` | Search | No |
| `glob` | Search | No |
| `ls` | Filesystem | No |
| `bash` | Shell | Yes |
| `powershell` | Shell | Yes |
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

Defined in `dangerousTools` map at `agent.go:1038`:

```go
var dangerousTools = map[string]bool{
    "bash": true, "powershell": true,
    "write": true, "edit": true, "delete": true,
}
```

All paths support `~` expansion via `expandHomeDir()` in `tools.go`. Tools that accept file paths (read, write, edit, delete, grep, glob, ls) call this before opening files.

---

## Context compaction

File: `internal/agent/agent.go` (`compactContext`, `trimContext`)

Triggered when estimated tokens exceed 80% of `ContextWindow` (character count / 4).

### LLM-powered compaction (`compactContext`)

1. Check token budget: if `estimated_tokens <= ContextWindow * 0.8`, return.
2. Preserve system message (index 0) and the 6 most recent messages.
3. Send older messages to the LLM for summarization with a structured prompt:

```
Summarize the following conversation excerpt.
## Goal
## Completed Work
## Active Work
## Pending Tasks
## Key Decisions
## Files Modified
```

4. Replace old messages with a system message containing the summary.
5. If the LLM call fails or returns empty, fall back to `trimContext()`.

### Truncation fallback (`trimContext`)

Removes oldest messages one at a time until the token budget is met. Always preserves the system message at index 0.

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
    ToolError  string        `json:"tool_error,omitempty"`
    ExitReason string        `json:"exit_reason,omitempty"`
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
