# Prompt Injection Architecture

This document maps every point in the yaah agent loop where prompt text is
injected — from the initial system prompt assembly through every turn and
sub-agent dispatch.

## Architecture overview

```
┌─────────────────────────────────────────────────────┐
│ CLI layer (cmd/yaah/)                               │
│ prompts.Build() = identity + env + user + project   │
└──────────────────┬──────────────────────────────────┘
                   │ l.SystemPrompt
                   ▼
┌─────────────────────────────────────────────────────┐
│ Agent loop (agent.go:Run)                           │
│ System message → User message → Assistant → Tools   │
│ Middleware injects: steer, followup, compaction     │
│ Post-turn injects: tool results, conflicts          │
└───────────────────────┬─────────────────────────────┘
                        │ task
                        ▼
            ┌───────────────────────────┐
            │ Sub-agent (isolated)       │
            │ subagent_runner.go         │
            │ System prompt + guidance   │
            │ (inherits orchestrator)    │
            └───────────────────────────┘
```

## Layer 1: System prompt assembly

**File**: `cmd/yaah/agent_frame.go` (and `cmd/yaah/tui.go` for the TUI)

The agent's full system prompt is assembled once at startup via
`prompts.Build()` (`internal/prompts/prompts.go`):

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
| Skills | `prompts.BuildSkillsIndex` | `## Available Skills` | Name + one-line description per discovered skill |

### Where environment info goes

`prompts.DetectEnvironment()` (`prompts/prompts.go`) renders the embedded
`environment_header.md` template, producing e.g.:
```
OS: windows/amd64. Default shell: powershell (pwsh 7+ or Windows PowerShell). Working directory: C:\Code\Personal\yaah.
```

This is injected into the agent's system prompt via the `Environment` layer.
Sub-agents receive the **same** environment block — `EnvironmentHeader` is the
single source of truth shared by the main agent and sub-agents, which is why
role guidance can refer to "the shell specified in the Environment section".

## Layer 2: Loop initialization

**File**: `internal/agent/agent.go`

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

**File**: `internal/agent/agent.go` (middleware in `internal/agent/pipeline/`)

Before each turn, middleware can inject or modify messages in `step.Messages`:

| Middleware | File | Injects | When |
|---|---|---|---|
| `steer` | `pipeline/steer.go` | `[STEER] <msg>` as user message | High-priority mid-turn input |
| `followup` | `pipeline/followup.go` | Queued follow-up as user message | Between-turn messages |
| `compaction` | `agent_context.go` | Summary as system message | Context window overflow |
| `sub_agent` | `pipeline/subagent.go` | `[system] depth limit reached` | Sub-agent depth cap hit |

### Compaction details (`agent_context.go`)

When the estimated context exceeds the window, old messages are summarized:

```go
// Injects a new system message with the summary:
newMsgs = append(newMsgs, types.SystemMsg("Previous conversation summary:\n"+summary))
```

This replaces the full history, keeping only the system prompt + summary + recent
messages in the LLM context.

## Layer 4: Chat request assembly

**File**: `internal/agent/agent.go`

Each turn, the chat request is assembled from the running message history plus
tool definitions:

```go
req := types.ChatRequest{
    Model:    l.Model,
    Messages: messages,              // running conversation
    Tools:    l.buildToolsForLevel(), // registry tools
}
```

`buildToolsForLevel()` returns all registered tools for the current tool level.

## Layer 5: Post-turn message injection

After the model responds each turn, messages are appended to the history:

### 5a. Assistant response (`agent.go`)

```go
messages = append(messages, msg)  // model's response with content + tool calls
```

### 5b. Tool results (`agent_tools.go`)

Each tool executed produces a tool result message:

```go
*messages = append(*messages, types.ToolResultMsg(callID, name, result))
```

### 5c. Conflict detection (`agent.go`)

When external file modifications are detected, a user message is injected:

```go
conflictMsg := types.UserMsg(report)
messages = append(messages, conflictMsg)
```

## Layer 6: Sub-agent prompt

**File**: `cmd/yaah/subagent_runner.go`

Sub-agents spawned via the `spawn_subagent` tool inherit the full orchestrator
system prompt plus a role guidance suffix:

```go
sysPrompt := opts.systemPrompt  // same as the orchestrator's l.SystemPrompt
// plus role-specific guidance:
Loop{
    SystemPrompt: sysPrompt + RoleGuidance(role),
    Tools:        role-limited subset,
}
```

Role guidance is the markdown body of each role definition
(`internal/agent/subagent/role.go` → `RoleGuidance`, loaded from the embedded
`internal/prompts/roles/*.md` and any `.agents/roles/*.md`). The built-in
roles and their personas:

| Role | Persona | Guidance (role markdown body) |
|---|---|---|
| `developer` | Charley | Implement features, fix bugs, make code changes; read before editing |
| `analyst` | Jack | Research and gather information from web/docs/code; do not modify files |
| `tester` | Casey | Run test suites, analyze failures, measure coverage; do not modify source |
| `reviewer` | Tim | Inspect code, count files/lines, measure complexity; do not modify files |

Sub-agents are full agents with the orchestrator's identity, just with
restricted toolsets and role-specific guidance.

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
├── [3] ChatRequest(..., buildToolsForLevel())
│
├── [4] AssistantMsg(response)  ← model output
│
├── If tool calls:
│   └── [5] ToolResultMsg(tool output)
│
├── If conflict detected:
│   └── [6] UserMsg(conflict report)
│
├── If spawn_subagent calls:
│   └── subagent_runner
│       └── [T1] SystemMsg(orchestrator prompt + role guidance)
│
└── Loop continues until model outputs no tool calls
```

## Key design decisions

1. **Sub-agents inherit the orchestrator prompt.** This means they carry the
   full identity, principles, and tool knowledge — they are full agents with
   restricted toolsets. Role guidance (the role's markdown body) is appended to
   steer behavior.

2. **Environment is assembled from one shared template.** `DetectEnvironment`
   renders `environment_header.md` once for the main agent's system prompt, and
   sub-agents receive the same block via `EnvironmentHeader`. It is not
   re-injected per-turn.

3. **Tool set is consistent.** The agent gets the full tool set based on the
   current tool level. The model chooses per-action which tools to use.

## Common pitfalls

- **Modifying identity.md without considering sub-agents.** Sub-agents inherit
  the full identity prompt, so changes affect ALL sub-agent behavior — not
  just the main agent.

- **Compaction removing the system prompt.** The compaction middleware at
  `agent_context.go` preserves the system prompt and recent messages. If the
  summary is too aggressive, the model may lose important context.
