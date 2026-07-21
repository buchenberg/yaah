# Prompt Injection Architecture

This document maps every point in the yaah agent loop where prompt text is
injected — from the initial system prompt assembly through every turn, the
dual-loop executor, and sub-agents.

## Architecture overview

```
┌─────────────────────────────────────────────────────┐
│ CLI layer (cmd/yaah/)                               │
│ prompts.Build() = identity + env + user + project   │
└──────────────────┬──────────────────────────────────┘
                   │ l.SystemPrompt
                   ▼
┌─────────────────────────────────────────────────────┐
│ Planner loop (agent.go:Run)                         │
│ System message → User message → Assistant → Tools   │
│ Middleware injects: steer, followup, compaction     │
│ Post-turn injects: delegate results, conflicts      │
└───────┬─────────────────────────────┬───────────────┘
        │ delegate                    │ task
        ▼                             ▼
┌───────────────────┐    ┌───────────────────────────┐
│ Executor loop     │    │ Sub-agent (isolated)       │
│ executor.go:111   │    │ subagent_runner.go:181     │
│ System + User     │    │ System prompt + guidance   │
│ (separate prompt) │    │ (inherits planner prompt)  │
└───────────────────┘    └───────────────────────────┘
```

## Layer 1: System prompt assembly

**File**: `cmd/yaah/agent_frame.go:124` / `cmd/yaah/tui.go:207`

The planner's full system prompt is assembled once at startup via
`prompts.Build()` at `internal/prompts/prompts.go:44`:

```go
systemPrompt := prompts.Build(layers)
```

| Layer | Source | Injected as | Format |
|---|---|---|---|
| Identity | `identity.md` (embedded) | Root block | Defines yaah's personality, principles, tools |
| Environment | `prompts.DetectEnvironment(cwd)` | `## Runtime Environment` | `OS: windows/amd64. Default shell: powershell.` |
| User context | `~/.yaah/AGENTS.md` | `<user-preferences>` block | Cross-project user preferences |
| Project context | walked-up `AGENTS.md`/`CLAUDE.md` | Inlined | Project-specific instructions and conventions |
| Memory | SQLite `memory_search` | `## Memory` | Stored facts from past sessions |

### Where environment info goes

`prompts.DetectEnvironment()` at `prompts/prompts.go:72` produces:
```
OS: windows/amd64. Default shell: powershell (pwsh 7+ or Windows PowerShell). Working directory: C:\Code\Personal\yaah.
```

This is injected into the **planner's** system prompt via the `Environment` layer.
It is NOT automatically injected into the executor or sub-agents — those have their
own injection points (see below).

## Layer 2: Loop initialization

**File**: `internal/agent/agent.go:344-352`

```go
// Fresh session
l.Messages = []types.Message{
    types.SystemMsg(l.SystemPrompt),   // [1] full assembled system prompt
    types.UserMsg(userInput),          // [2] user's input
}

// Session resume (l.Messages already exists)
l.Messages = append(l.Messages, types.UserMsg(userInput)) // [2]
```

| Inject | Type | Content |
|---|---|---|
| [1] | `system` | Full `l.SystemPrompt` (Layer 1 output) |
| [2] | `user` | Raw user input string |

## Layer 3: Middleware prepare step

**File**: `internal/agent/agent.go:392-397`

Before each turn, middleware can inject or modify messages in `step.Messages`:

| Middleware | File | Injects | When |
|---|---|---|---|
| `steer` | `middleware_steer.go:24` | `[STEER] <msg>` as user message | High-priority mid-turn input |
| `followup` | `middleware_followup.go:23` | Queued follow-up as user message | Between-turn messages |
| `compaction` | `agent_context.go:150-168` | Summary as system message | Context window overflow |
| `sub_agent` | `middleware_subagent.go:58` | `[system] depth limit reached` | Sub-agent depth cap hit |

### Compaction details (`agent_context.go`)

When the estimated context exceeds the window, old messages are summarized:

```go
// Injects a new system message with the summary:
newMsgs = append(newMsgs, types.SystemMsg("Previous conversation summary:\n"+summary))
```

This replaces the full history, keeping only the system prompt + summary + recent
messages in the LLM context.

## Layer 4: Chat request assembly

**File**: `internal/agent/agent.go:401-404`

Each turn, the chat request is assembled from the running message history plus
tool definitions:

```go
req := types.ChatRequest{
    Model:    l.Model,
    Messages: messages,              // running conversation
    Tools:    l.buildPlannerToolDefs(), // registry tools + delegate
}
```

`buildPlannerToolDefs()` at `executor.go:318` always includes the `delegate` tool
as well as all registered tools. The model chooses per-action whether to delegate
or work inline.

## Layer 5: Post-turn message injection

After the model responds each turn, messages are appended to the history:

### 5a. Assistant response (`agent.go:435`)

```go
messages = append(messages, msg)  // model's response with content + tool calls
```

### 5b. Delegate executor results (`agent.go:526-528`)

When the model calls `delegate`, the executor's summary is wrapped and appended
as a tool result:

```go
content := wrapExecutorResult(summary, exhausted, innerErr, truncated, fellBack)
tr := types.Message{Role: "tool", Content: content, ToolCallID: tc.ID, Name: delegateToolName}
messages = append(messages, tr)
```

The wrapped result includes `state`, `truncated`, and `fallback` attributes in XML:
```xml
<executor_result state="completed" truncated="false" fallback="false">summary text</executor_result>
```

### 5c. Inline tool results (`agent_tools.go:196`)

Each tool executed inline produces a tool result message:

```go
*messages = append(*messages, types.ToolResultMsg(callID, name, result))
```

### 5d. Conflict detection (`agent.go:584`)

When external file modifications are detected, a user message is injected:

```go
conflictMsg := types.UserMsg(report)
messages = append(messages, conflictMsg)
```

## Layer 6: Executor — dual-loop inner prompt

**File**: `internal/agent/executor.go:111-151`

The executor runs with a completely separate prompt from the planner. The planner
hands it an intent-level directive; the executor owns tool selection.

### Executor system prompt (`internal/prompts/identity-executor.md`)

A purpose-built, compact system prompt — much smaller than the planner's identity:

```
You are a tool executor. You receive a task directive, the user's original request,
the working directory, and the runtime environment (OS, architecture, default shell).
You run in the same filesystem as the planner. Select and run the built-in tools
needed to accomplish the directive. On Windows, prefer the powershell tool over bash
— bash requires a POSIX shell (sh) which is not available on Windows.
```

Key differences from the planner prompt:
- No user-facing identity (no "You are yaah")
- No vendor-free/principals block
- No sub-agent or skill instructions
- No approval guidance
- No memory or planning tools
- Explicit OS/shell preference guidance

### Executor user payload (`executor.go:138-145`)

Assembled per-delegation from four sources:

| Component | Source | Example |
|---|---|---|
| Directive | Planner's `delegate(task=...)` | `count .go files in internal/tools/` |
| Original intent | `l.lastUserMessage()` | `use delegate to count .go files...` |
| Working directory | `os.Getwd()` | `C:\Code\Personal\yaah` |
| Runtime environment | `detectRuntimeEnv()` | `OS: windows/amd64. Default shell: powershell.` |

```go
messages := []types.Message{
    	types.SystemMsg(executorPrompt),
    types.UserMsg(payload),
}
```

### Executor message flow within a delegation

```
executorPrompt              ← embedded identity-executor.md
    +
UserMsg(directive+intent+cwd+env)  ← reassembled per delegation
    ↓
llm.stream (executor model)   ← first iteration
    ↓ (tool calls)
executeOneTool                ← glob, powershell, bash, etc.
    ↓
tool result appended          ← to executor's messages
    ↓
llm.stream (same model)       ← next iteration, sees tool results
    ↓
(no more tool calls)
    ↓
return summary to planner     ← wrapped in <executor_result>
```

## Layer 7: Sub-agent prompt

**File**: `cmd/yaah/subagent_runner.go:150-181`

Sub-agents spawned via the `task` tool inherit the full planner system prompt plus
a role guidance suffix:

```go
sysPrompt := opts.systemPrompt  // same as planner's l.SystemPrompt
// plus role-specific guidance:
Loop{
    SystemPrompt: sysPrompt + RoleGuidance(role),
    Tools:        role-limited subset,
}
```

Role guidance examples (`agent/subagent.go:84-103`):
| Role | Guidance |
|---|---|
| worker | "Implement the assigned task directly using filesystem and shell tools" |
| reviewer | "You have read-only tools. Analyze, review, research and report" |
| planner | "You may decompose the work and dispatch worker sub-agents" |

Sub-agents do NOT get the executor's separate prompt — they are full agents with
the planner's identity, just with restricted toolsets and role-specific guidance.

## Complete injection map

```
CLI startup
├── [1] SystemMsg(full prompt from prompts.Build)
│       = identity.md + DetectEnvironment + AGENTS.md + memory
│
Loop.Run(userInput)
├── [2] UserMsg(userInput)
│
├── [MW] middleware.PrepareStep
│   ├── [S] steer: UserMsg("[STEER] ...")
│   ├── [F] followup: UserMsg(queued msg)
│   ├── [C] compaction: SystemMsg("Previous summary: ...")
│   └── [D] sub_agent: UserMsg("[system] depth limit")
│
├── [3] ChatRequest(..., buildPlannerToolDefs())
│
├── [4] AssistantMsg(response)  ← model output
│
├── If delegate calls:
│   ├── runExecutor
│   │   ├── [E1] SystemMsg(identity-executor.md)
│   │   ├── [E2] UserMsg(directive + intent + cwd + runtimeEnv)
│   │   ├── [E3] AssistantMsg(executor response)  ← per iteration
│   │   └── [E4] ToolResultMsg(tool output)        ← per tool
│   └── [5] ToolResultMsg(<executor_result>)        ← back to planner
│
├── If inline tool calls:
│   └── [6] ToolResultMsg(tool output)
│
├── If conflict detected:
│   └── [7] UserMsg(conflict report)
│
├── If task (sub-agent) calls:
│   └── subagent_runner
│       └── [T1] SystemMsg(planner prompt + role guidance)
│
└── Loop continues until model outputs no tool calls
```

## Key design decisions

1. **Executor gets its own prompt, not the planner's.** The executor prompt
   at `internal/prompts/identity-executor.md` is purpose-built for tool execution — it is much smaller
   (~700 chars vs ~4,000+ for the planner) and avoids identity, principles,
   memory, skills, and approval instructions. This keeps executor context small
   and prevents it from behaving like a user-facing agent.

2. **Executor gets runtime environment info per-delegation.** The
   `detectRuntimeEnv()` function at `executor.go:336` injects OS/shell info into
   the executor's user payload so it knows when to prefer PowerShell over bash.

3. **Sub-agents inherit the planner prompt.** This means they carry the full
   identity, principles, and tool knowledge — they are full agents with
   restricted toolsets. Role guidance is appended to steer behavior.

4. **Planner gets environment once at startup.** `DetectEnvironment` runs once
   and is embedded in the system prompt. It is not re-injected per-turn.

5. **The `delegate` tool is additive, not substitutive.** The planner always
   gets the full tool set PLUS `delegate`. The model chooses per-action
   whether to delegate or work inline. The executor gets the full tool set
   MINUS `delegate` (no recursive delegation).

## Common pitfalls

- **Changing the executor prompt without considering OS/shell.** The executor
  prompt at `identity-executor.md` must mention Windows/`powershell` preference.
  Generic shell guidance will cause the model to favor `bash` on Windows,
  wasting iterations on `sh: executable not found`.

- **Forgetting to inject environment into executor.** The `detectRuntimeEnv()`
  call at `executor.go:145` is the executor's only source of OS/shell info.
  Without it, the executor model doesn't know it's on Windows.

- **Modifying identity.md without considering sub-agents.** Sub-agents inherit
  the full identity prompt, so changes affect ALL sub-agent behavior — not
  just the planner.

- **Compaction removing the system prompt.** The compaction middleware at
  `agent_context.go` preserves the system prompt and recent messages. If the
  compaction summary is too long or too short, the model loses context.
