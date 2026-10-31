# yaah Architecture

## High-level overview

```
┌──────────────────────────────────────────────────┐
│  CLI (cmd/yaah/)                                 │
│  root_cmd.go / tui.go / repl_loop.go / web.go    │
│  ─────────────                                   │
│  Parses config, wires providers, builds tool     │
│  registry, creates agent.Loop, calls Run()       │
└──────────────────┬───────────────────────────────┘
                   │
┌──────────────────▼───────────────────────────────┐
│  agent.Loop                                      │
│  loop.go / turn.go                               │
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

yaah is structured as a single Go binary. The entry point is `main.go`, which delegates to `cmd/yaah.Execute()` (cobra). The CLI layer (`cmd/yaah/`) wires providers, tools, memory, MCP clients, and creates an `agent.Loop` for each conversation turn. `internal/agent/` contains the loop, middleware pipeline, tool dispatch, streaming, and context management.

---

## Agent loop

The loop lives in `internal/agent/loop.go`; turn processing is in `turn.go`. The entry point is `Loop.Run(ctx, userInput)`. A `Loop` has three groups of state:

| Group | Fields |
|---|---|
| `Config` (`LoopConfig`) | `Model`, `ProviderName`, `SystemPrompt`, `MaxLoopCycles`, `MaxToolTurns`, `ContextWindow`, `ApprovalMode`, `MaxToolConcurrency`, `MaxSubAgentConcurrency`, `PermissionRules`, `MCPToolNames`, `MCPApproval`, `QualityGates`, `OtelEnabled` |
| `State` (`LoopState`) | `Messages`, token/usage totals, last finish reason/model, compaction counters, turn checkpoint IDs |
| Direct fields | `Provider`, `Registry`, `View`, `LLM`, `Persister`, `Hooks`, `Steer`, `FollowUps`, `ApproveFn`, `ContinueAfterMaxIter`, `ConflictTracker`, `CtxMgr`, `BackgroundJobs`, `OnUsage`, `OnSubUsage` |

`Persister` owns session persistence; `Persister.MsgIdx()` is the next message index. There is no `DB`/`MsgIdx`/`Pipe` on `Loop`, and MCP tools are registered in `Registry` (policy is `Config.MCPApproval` / `Config.MCPToolNames`).

### Agent loop (`runMiddleware`)

The agent loop runs via a single middleware pipeline. One iteration follows this sequence:

```
for iter := 0; iter < Config.MaxLoopCycles; iter++ {
    1. emitHook(TurnStart)
    2. Build Step{ Messages, Tools, Iteration, Model, SystemPrompt, ... }
    3. pipeline.RunPrepareStep(ctx, step)      ← middleware hook
    4. l.LLM.Call(turnCtx, req)                ← LLM call (+ retry/fallback)
    5. l.Persister.Persist(msg)                ← session persistence
    6. If no tool calls → return content       ← terminal
    7. If streamed → broker.PublishMustDeliver(&FlushEvent{...})
    8. pipeline.RunPostModel(ctx, &msg, step)  ← middleware hook
                                                  (approval / inline_limit strip calls here,
                                                   synthesizing results on step.SynthesizedResults)
    9. executeToolPhase: merge step.SynthesizedResults, then
       executeAndCollect(ctx, toolCalls, &messages)  ← fan out to toolDispatch
   10. pipeline.RunPostTool(ctx, results, step) ← middleware hook
       (conflict_detect appends reports here)
   11. Update messages ← step.Messages
   12. Persist any new messages from middleware (e.g. compaction summaries)
```

Current invariants:

- `executeAndCollect` returns `[]ToolResult` so middleware can inspect tool outcomes.
- Per-call lifecycle (semaphore acquisition, events, spans, truncation) lives in `toolDispatch.run` (`agent_dispatch.go`).
- `State.Messages` is updated inside `executeAndCollect` and again after `RunPostTool`; both copies stay in sync so compaction sees the latest messages.
- `Step` is the mutable per-iteration state bag. Middleware reads and writes it freely.

### Agent coordination model

Files: `internal/agent/loop.go`, `agent_dispatch.go`, `internal/tools/delegate.go`, `internal/agent/runner/runner.go`

yaah uses a two-layer orchestrator → sub-agent architecture. The
orchestrator sees the full tool set plus `delegate` and chooses per task
whether to work inline or dispatch a sub-agent.

**Properties:**

- **Per-action choice.** A turn may contain direct tool calls and
  `delegate` dispatches. Simple `glob`/`read`/`grep` calls skip the
  sub-agent roundtrip.
- **Sub-agents own tool execution.** `delegate(role, description, prompt)`
  runs the role's curated tool set. The orchestrator describes *what* to
  do, not *how*.
- **Role-constrained tool sets.** `analyst` has read/search/web tools,
  `developer` adds write/edit, `tester` has shell + test tools, and
  `reviewer` has inspection/analysis tools. No built-in role registers
  `delegate`; nesting is depth-limited for custom roles that opt in.
- **Model tiering.** Sub-agents can use a different provider/model than
  the orchestrator.
- **Approval.** Sub-agent loops run with approval `allow`; the parent's
  permission rules are still enforced by the `permission` middleware.

**Dispatch flow:**

```
1. Orchestrator emits delegate(role, description, prompt)
2. makeTaskRunner resolves the role, budget, provider/model, and lane
3. Retained lane: fork an isolated dispatch world (worktree or per-dispatch
   container), run the sub-agent, seal a proposal
   Direct lane:  run the sub-agent on the host/shared workspace
4. Result returns as a tool result (or a proposal the orchestrator settles)
5. Orchestrator evaluates the result and may dispatch more sub-agents
```

### LLM call with retry (`llm.Client.Call`)

Files: `internal/agent/llm/client.go`, `internal/agent/llm/stream.go`

`Client.Call` owns the model call and the retry ladder:

1. Uses streaming when the provider implements `StreamProvider` **and**
   `OnToken` is non-nil (a broker/View is active); otherwise it calls
   `Provider.Send`.
2. On failure, classifies the error and applies the configured recovery:
   retry with exponential backoff, compact/trim on context overflow, strip
   reasoning on dialect errors, or rotate to the fallback provider on
   auth/billing/rate-limit failures.
3. Rejects responses where `finish_reason == "length"` and tool calls are
   present (truncated tool calls are unsafe to execute).
4. Maps `finish_reason=length` with no content to an output-budget error
   that names the serving provider/model.

---

## Middleware pipeline

File: `internal/agent/pipeline/pipeline.go`, `internal/agent/pipeline/middleware.go`

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
    Messages           []types.Message
    Tools              []types.ToolDef
    Iteration          int
    Model              string
    SystemPrompt       string
    SynthesizedResults []types.Message // tool results generated by middleware for stripped calls
    InlineDropped      int             // calls dropped by inline_limit this turn
}
```

### Pipeline architecture

`Pipeline` wraps an ordered list of `Middleware`. Each hook method iterates through every middleware in registration order. Errors are wrapped with the middleware name and halt the pipeline.

The middleware list is resolved from the `builtinBuilders` and `subAgentBuilders` registries (maps of `func(PipelineConfig) Middleware` in `config.go`; `permission` and `tool_concurrency` share named builder functions across both). Builders construct implementations from `PipelineConfig` fields, including injected callbacks (approval gating, conflict reporting) so the pipeline stays free of agent-package dependencies. The `ResolvedPipelineNames()` function applies the `enabled`/`disabled` config overrides to produce the final ordered list.

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
1. `steer` — high-priority mid-turn messages
2. `followup` — queued between-turn messages
3. `compaction` — context window enforcement (LLM summarization)
4. `soft_prune` — tier-0 tool-output elision (non-LLM, reclaims context)
5. `approval` — enforces the approval policy in `PostModel`: dangerous calls are stripped and replaced by synthesized denial tool-results (classification, user prompting, and hook emission are injected callbacks from `toPipelineConfig`)
6. `inline_limit` — caps calls dispatched per turn in `PostModel`, synthesizing drop results for calls beyond `MaxInlineToolsPerTurn`
7. `tool_concurrency` — shares the Loop-owned semaphore (`PipelineConfig.ToolConc`); one live instance per loop
8. `shepherd_trace` — records tool declarations, observations, and transcript facts in the kernel trace
9. `loop_detection` — detects identical-call loops
10. `intent_loop` — detects repeated cross-turn plans and can steer or halt
11. `evidence_guard` — nudges or halts exploration that stops producing new evidence
12. `conflict_detect` — `PostTool` conflict reports driven by the injected tracker via `DetectAndReset()`

The default set is defined in `defaultPipelineNames` in `config.go`. Users can override which middleware runs via `config.yaml`:

```yaml
agents:
  middleware:
    enabled: [prompt_caching]
    disabled: [approval]
```

`enabled` is additive for the orchestrator: listed middleware are added to
the default set (duplicates collapse). `disabled` removes names from both the
orchestrator and sub-agent pipelines. Middleware not registered in the
relevant builder set is silently skipped.

Middleware selection is config-driven: `agents.middleware.enabled` / `disabled` are resolved by `ResolvedPipelineNames`, and `buildPipeline()` always builds from that resolved list and the relevant builder registry. There is no arbitrary `Loop.Middleware` injection seam.

### Individual middleware

#### SteerMiddleware (`pipeline/steer.go`)

Drains the `Loop.Steer` channel in `PrepareStep`. Messages are appended as user messages prefixed with `[STEER]`. If a compaction hook is provided, it runs after injection.

#### FollowupMiddleware (`pipeline/followup.go`)

Drains the `Loop.FollowUps` channel in `PrepareStep`. Messages are injected as user messages. This is used for queuing follow-up prompts while the agent is busy.

#### CompactionMiddleware (`pipeline/compaction.go`)

Triggers context compaction at both `PrepareStep` (preflight) and `PostTool` (post-iteration) hooks when `ContextWindow > 0`. Delegates through `Loop.Compact` to `ContextManager`; `Loop.Config.CompactionThreshold` (default 0.5) controls what fraction of the window triggers compaction.

#### SoftPruneMiddleware (`pipeline/softprune.go`)

Soft-prune is a tier-0, non-LLM context-reclaim step. It runs in both
`PrepareStep` and `PostTool`: it walks the message history backwards from the
most recent message, marks stale tool-result messages whose output is large
enough and old enough to elide, and at request-build time `Loop.applyPruning`
stubs those messages' content with a compact placeholder before the provider
sees them.

Current behavior:

- **Non-mutating.** `State.Messages` and the database keep the full original
  content; only the ephemeral `ChatRequest` carries stubs.
- **Tool-call linkage preserved.** Tool messages are never removed — only
  their `Content` string is replaced, so every `tool_call_id` keeps its
  answer.
- **Defaults.** `ProtectTokens` 2000, `MinReclaim` 200, `MinTurns` 1. The
  protect window and minimum reclaim are configurable; `MinTurns` counts
  user messages or assistant messages with tool calls.
- **Protected tools.** `skill` (instruction payloads) and `delegate`
  (sub-agent results) are never pruned.
- **Walk termination.** The backward walk skips already-pruned messages and
  stops at a non-index-0 system message, bounding the per-turn cost to new
  messages.

`SoftPruneMiddleware` is on by default (after `compaction`, before
`approval`). Disable with `agents.middleware.disabled: [soft_prune]`.

#### ApprovalMiddleware (`pipeline/approval.go`)

Enforces the approval policy in `PostModel`, mirroring the call-stripping pattern of `PermissionMiddleware`. For each dangerous tool call (classified via injected callback wired to `Loop.classifyGate` / the `tools.DangerClassifier` interface):

- **deny** mode: the call is stripped and a synthesized error tool-result (`error: tool %q requires approval but approval mode 'deny'`) is appended to `step.SynthesizedResults`.
- **ask** mode: the injected approve callback prompts (or consults `Loop.ApproveFn`); an approval keeps the call, a refusal strips it with `error: tool %q was denied by user`.
- **allow** mode (and sub-agent loops): inert.

Stripped calls emit the `ToolStart`/`ToolEnd` hook pair, and every stripped `tool_call_id` receives a persisted synthesized result so the provider invariant (every tool call answered) holds. Approval runs before `inline_limit` in the default order, so only approved calls count against the per-turn cap.

#### InlineLimitMiddleware (`pipeline/inlinelimit.go`)

Caps tool calls dispatched per turn in `PostModel`. When a batch exceeds `MaxInlineToolsPerTurn`, calls beyond the cap are stripped and each receives a synthesized drop result on `step.SynthesizedResults` ("[dropped: this call exceeded the inline tool limit ...]"), preserving the provider invariant. A `max` of 0 means unlimited. The drop count is surfaced as the verbose OTel `inline.truncated` event via `Step.InlineDropped`.

#### LoopDetectionMiddleware (`pipeline/loopdetect.go`)

Tracks a sliding window of SHA-256 hashes of `(tool_name, tool_arguments, tool_result)` triples. If any hash appears `count` or more times within the last `window` executions, the pipeline halts with an error. Uses the package-level `toolCallHash()` function.

#### PermissionMiddleware (`pipeline/permission.go`)

Filters tool calls in `PostModel` based on path-pattern rules. Rules are configured as `PermissionRule` structs with `Tool` (name), `Path` (glob), and `Mode` (allow/deny). Denied tool calls are removed from the assistant message and replaced with a synthesized denial tool result, so every `tool_call_id` is answered. Rule matching uses `filepath.Match` for glob support. Paths are extracted from tool arguments by key (`path`, `filePath`, or `command` for shell tools).

#### ConflictDetectMiddleware (`pipeline/conflictdetect.go`)

Runs after each tool batch in `PostTool`. When a `ConflictTracker` is configured, the middleware emits the `conflict.check` hook, calls `DetectAndReset()`, and — when a report is produced — emits `conflict.detect`, decorates the turn span (`conflict.files`), appends the structured `CONFLICT:` user message to `step.Messages`, and persists it. Appending in `PostTool` is safe because nothing overwrites `step.Messages` after `RunPostTool`; the loop adopts it at the end of the iteration. Inert when no tracker is configured (and absent from sub-agent pipelines).

#### ToolConcurrencyMiddleware (`pipeline/toolconcurrency.go`)

Limits concurrent tool goroutines via a buffered channel semaphore. The Loop creates exactly one instance in `applyDefaults()` (`Loop.toolConcurrency`) and passes it to the pipeline via `PipelineConfig.ToolConc`, so `buildPipeline()` shares the same semaphore rather than allocating a second one; `toolDispatch` acquires/releases it around each tool call. The middleware hooks themselves are no-ops — the semaphore is driven directly by the dispatch loop.

#### Sub-agent depth limiting (structural, no middleware)

Nesting depth is bounded structurally. No built-in role registers the
`delegate` tool, so built-in sub-agent loops cannot dispatch further
sub-agents at all. A custom role that lists `delegate` gets the task
tool only while `makeTaskRunner`'s `remainingDepth` is positive; the runner
decrements the remaining depth per level, so a nested sub-loop eventually
loses its `delegate` tool entirely (see
[Sub-Agent Lifecycle](#sub-agent-lifecycle)). Depth limiting is structural;
there is no middleware that drops excess `delegate` calls.

Sub-agent concurrency is enforced on `Loop.subAgentSem` (see
`executeAndCollect`), mirroring how `ToolConcurrencyMiddleware` relates to
`Loop.toolConcurrency`.

#### PromptCachingMiddleware (`pipeline/promptcaching.go`)

Injects Anthropic `cache_control: {type: "ephemeral"}` breakpoints on system messages and tool results in `PrepareStep`. When `PromptCaching` is `true`, the system message and the last tool result before a user turn are marked for caching. The middleware only decides **whether breakpoints are stamped** — never whether they are legal on the wire. `cache_control` is an Anthropic wire concept, not an OpenAI one, so the OpenAI-compatible client emits it only for providers that opt in via the `supports_cache_control` provider key (GitHub Copilot is opted in implicitly); the native Anthropic client always emits it. Strict compatible endpoints such as Mistral and ZAI/GLM answer `422 extra_forbidden` for the unknown field, which is why the gate lives in `lowerMessage`'s `lowerOptions.CacheControl` rather than in the middleware or in `omitempty`.

---

## Sub-agent lifecycle

Files: `internal/agent/subagent/role_def.go`, `internal/tools/delegate.go`, `internal/jobs/` (`TaskRunner`, `SubAgentParams`, escalation contract, context-key helpers, `BackgroundJobs` manager), `internal/agent/runner/runner.go`

The `delegate` tool spawns a sub-agent: a fresh `agent.Loop` with a curated tool registry, its own iteration budget, deadline, and system prompt. Sub-agents let the main agent delegate isolated work and fan out independent subtasks in parallel.

### Roles and tool profiles

Each sub-agent runs under a **role** that selects its tool set and default limits.

| Role | Iterations | Turns (floor) | Timeout | Tools |
|---|---|---|---|---|
| `analyst` (Jack) | 75 (cap 50) | 24 (5) | 600s | read/search/web + shell, git, diff, go_outline |
| `developer` (Charley) | 75 (cap 50) | 24 (6) | 900s | analyst set + write/edit/delete/patch + Go tools |
| `tester` (Casey) | 30 | 8 (4) | 300s | read/search + shell + go_test + git/diff |
| `reviewer` (Tim) | 25 | 12 (8) | 600s | read/search + git/diff/staticcheck; no shell or web |

Role iteration limits above the tool schema's hard ceiling of 50 resolve to 50.

There is no default role and no full-tools fallback. `RoleProfileFor(role)`
reads from the global `RoleRegistry` installed at startup via
`SetDefaultRoleRegistry`. Unknown roles return the zero-value profile; a
tool-less profile is valid. `delegate` rejects unknown role names when the
registry-supplied role list is available.

### RoleRegistry

File: `internal/agent/subagent/role_def.go`

The `RoleRegistry` is the central store for role definitions. It holds built-in roles (embedded in the binary) and user-defined roles (discovered from the filesystem at startup), with built-in roles taking precedence on name conflict.

**Core types:**

- `RoleDef` — the persistent format for a role, parsed from YAML frontmatter: `Tools []string`, `MaxIterations`, `MaxTurns`, `Timeout` (seconds), `JSONMode`, and `Body` (the markdown guidance text). Its `ToProfile()` method converts to the runtime `RoleProfile` struct.
- `RoleRegistry` — a `sync.RWMutex`-protected `map[SubAgentRole]RoleDef`. Thread-safe for concurrent reads during sub-agent dispatch.

**Loading built-in roles:**

Built-in roles are embedded via `//go:embed roles/*.md` in `internal/prompts/prompts.go` as `BuiltinRolesFS` (an `embed.FS`). At startup, `runner.BuiltinRoleFiles()` in `internal/agent/runner/runner.go` reads the directory and passes each file's content to `reg.LoadBytes(files)`. The file name minus `.md` becomes the role name (e.g. `analyst.md` → `"analyst"`).

**Loading user-defined roles:**

`runner.RoleSearchPaths(cwd)` returns directories to scan: every `.agents/roles/` directory walked up from `cwd`, then `~/.agents/roles/`. `reg.LoadDir(dir)` reads every `.md` file in each directory, parses it with `parseRoleFile`, and adds the role — but only if the role name isn't already registered (built-in always wins).

**Parsing: `parseRoleFile`** splits the markdown at the first `---\n...\n---` YAML frontmatter block, unmarshals the YAML portion into `RoleDef`, and stores the remaining markdown in `Body`. It returns an error on missing or unterminated frontmatter.

**Atomic installation:**

`SetDefaultRoleRegistry` stores the loaded registry in a package-level `atomic.Pointer[RoleRegistry]` in `role.go`. `RoleProfileFor` and `RoleGuidance` read from this pointer; when it's `nil` (e.g. in unit tests), or the role is unknown, they return the zero-value `RoleProfile` / empty string.

**Key methods:**

| Method | Purpose |
|---|---|
| `LoadBytes(files map[string][]byte)` | Parse and register built-in roles (takes precedence) |
| `LoadDir(dir string)` | Discover and register user-defined roles (skipped if name already registered) |
| `ProfileFor(role SubAgentRole) RoleProfile` | Return runtime profile; zero-value for unknown roles |
| `Guidance(role SubAgentRole) string` | Return role-specific system-prompt text (the markdown body) |
| `Names() []string` | All registered role names, for dynamic schema generation |

### Dynamic delegate schema

File: `internal/tools/delegate_schema.go`

`BuildDelegateSchema(roleNames []string, roleDescriptions map[string]string) json.RawMessage` constructs the `delegate` tool's JSON Schema at startup from the active role list and descriptions, using `encoding/json` marshalling to avoid injection. When `RoleNames` is empty, `DelegateTool.Schema()` falls back to a static schema whose `role` field is an unconstrained string.

Wiring chain: `runner.RoleDescriptionsFor(reg.Names())` and `RoleNames` → `runner.NewDelegateTool(DelegateToolOpts{...})` → `DelegateTool.Schema()`. The tool also carries a live `RoleResolver` so roles created during a session are visible without a restart.

### CLI lifecycle display

Files: `internal/agent/agent_dispatch.go` (event publication), `internal/agent/events/events.go` (event types), `cmd/yaah/view_terminal.go` (rendering)

The REPL renders a sub-agent's lifetime with `╭─` / `╰─` box-drawing
corners:

```
╭─ sub-agent: Developer · Implement the retry backoff
╰─ sub-agent: Developer [zai/GLM-5.3-Flash] · completed (6.8s)
```

`SubAgentStartEvent` carries `SubAgentID`, role, model, and prompt;
`SubAgentEndEvent` carries the same identity plus duration, error, and the
result. `terminalView` prints ordinary tools as a single flat
`tool: <name>(<args>) (<duration>)` line on `ToolEndEvent`; it does not
indent sub-agent tool calls or track a context depth.

### TUI lifecycle display

Files: `cmd/yaah/tui.go`, `internal/tui/proxy.go`, `internal/tui/blocks.go`, `internal/tui/components/subagent/`, `internal/tui/components/backgroundjobs/`

Sub-agents render only in the dedicated Subagents pane; they are not added
to the conversation log. `SubAgentStartEvent` calls `AddSubAgentStart`,
which appends a `subagent.Block` to `App.subAgents`. `SubAgentEndEvent`
calls `AddSubAgentEnd` (or `AddSubAgentError`) and finds the block by
`SubAgentID` to transition it in place. `backgroundjobs.Format` renders the
active blocks as a compact row with role, elapsed time, and task; the
100ms activity ticker calls `AdvanceSpinner` on each active block and
repaints the pane. Terminal blocks are pruned from the pane. Background jobs
that complete after the loop retires use the session-scoped fallback hooks
(`SetSessionHooks`) to deliver `SubAgentEndEvent` directly to the current
view. Role colors come from `Theme.RoleHex` in
`internal/tui/colors/theme.go`.

### Timeout enforcement

`DelegateTool.Execute` (`internal/tools/delegate.go`) wraps the runner call
in `context.WithTimeout`. The effective timeout resolves in this order:
per-call `timeout_seconds` (values above 600 are clamped), per-role
`agents.subagent.roles.<role>.timeout`, the role file's `timeout`, then
`agents.subagent.default_timeout`. On deadline or cancellation the tool
returns a structured JSON result
(`{"error":"timed out","role":"developer","timeout":"900s","partial":"..."}`)
instead of a Go error, so the parent can retry, continue, or report. The role
and resolved budget tell the orchestrator which timeout to raise; any partial
output is surfaced alongside the error.

### Parallel dispatch and concurrency cap

Multiple `delegate` calls in one turn are dispatched concurrently by
`toolDispatch` like any other tool. Sub-agent fan-out is bounded by
`Loop.Config.MaxSubAgentConcurrency` (default 3), realised as a separate
`subAgentSem` acquired only for tool calls named `delegate`. This is
independent of `MaxToolConcurrency`.

### Interrupt propagation

The parent context flows through `toolDispatch` → `DelegateTool.Execute` →
`Runner` → `subLoop.Run`. Cancelling the parent (for example Ctrl-C)
propagates through the context; the provider call respects it. A cancelled
sub-agent returns the structured cancellation result rather than failing
the parent turn.

### Structured escalation

Sub-agents raise structured escalations when they hit a blocker. The
escalation is a fenced JSON block in the sub-agent's final output:

```
```escalation
{"severity":"blocker","summary":"...","detail":"...","suggestion":"..."}
```
```

`ParseSubAgentOutput` (`internal/jobs/output.go`) extracts it before the
result is truncated. `toolDispatch` publishes an `EscalationEvent`; the REPL
and TUI render a severity-colored card. The event is advisory: the
orchestrator prompt tells the model to consider halting siblings, but no
automatic sibling cancellation occurs.

### Quality gates

After a sub-agent completes, the orchestrator checks
`Loop.Config.QualityGates[role]` for validator roles. Each validator is
dispatched with the original output as context. The verdict comes from the
validator's contract `verdict` field, with a last-occurrence heuristic as
fallback. A failing verdict annotates the result with
`[quality-gate:FAIL]`. Gates also run when the sub-agent escalated.

### Session directives

Top-level directives come from `agents.default.directives` and the
repeatable `-d/--directive` flag; CLI directives are prepended to config
directives and injected into the orchestrator prompt after the identity
block. Sub-agents do not inherit them; use
`agents.subagent.roles.<role>.directives` for per-role policy.

### Nesting depth

Nesting is bounded structurally, hardcoded to depth 1 for built-in roles:
no built-in sub-agent role registers the `delegate` tool, so a
sub-agent loop cannot dispatch further sub-agents at all
(`buildSubAgentRegistry` omits `delegate` when `remainingDepth`
reaches 0). A custom role that lists `delegate` gets the delegate tool
only while the runner's remaining depth is positive. Depth limiting is
structural; there is no middleware that drops excess `delegate` calls.

### Configuration

```yaml
agents:
  subagent:
    max_concurrency: 3    # simultaneous delegate calls per turn
    default_timeout: 120  # seconds; used when no role default applies
    roles:
      analyst:
        timeout: 120
        max_iterations: 20
      developer:
        timeout: 180
        max_iterations: 25
      reviewer:
        timeout: 120
        max_iterations: 15
      tester:
        timeout: 180
        max_iterations: 20
```

### Agent conflict reconciliation

Files: `internal/tools/conflict_tracker.go`, `internal/tools/recording.go`, `internal/agent/loop.go`, `internal/agent/turn.go`, `internal/agent/agent_dispatch.go`

When an agent dispatches multiple parallel sub-agents in a single turn, those sub-agents may independently modify the same files. If sub-agent B's write clobbers sub-agent A's changes, the agent has no way of knowing without explicit conflict detection.

**Tracker:** `ConflictTracker` is a thread-safe `[]FileRecord` store on `Loop`. Sub-agent write/edit/delete tools are wrapped with `RecordingTool` (via `buildSubAgentRegistry`), which extracts the `filePath` from JSON args and records `(subAgentLabel, filePath, toolName)` to the shared tracker.

**Label propagation:** `DelegateTool.Execute` builds a label from the task's `role` and `description` (e.g. `"developer — Add login handler"`) and injects it into the request context via `WithConflictLabel()`. `RecordingTool.Execute` reads this label from the context, so each record is tagged with which sub-agent produced it.

**Detection:** After the tool batch completes, the `conflict_detect` middleware runs in `PostTool` and calls `ConflictTracker.DetectAndReset()`. Files touched by more than one distinct sub-agent label are flagged. If conflicts are found, a structured `CONFLICT:` user message is injected into the conversation before the next LLM call. The report lists each conflicting file, which sub-agents touched it, and which tools they used.

**Observability:** Two hook event types are emitted: `conflict.check` (every turn when a tracker is present) and `conflict.detect` (when conflicts are found, with `conflict_files` count). When OTel is enabled, `conflict.files` and a `conflict.detected` event are recorded on the current turn span.

### Retained sub-agents and workspace isolation

Files: `internal/tools/retained.go`, `internal/tools/proposal.go`, `internal/tools/supervisor.go`, `internal/agent/runner/checkpoint.go`

A retained dispatch runs the sub-agent in an **isolated dispatch world** — a git worktree, or a per-dispatch container when configured — and seals its output into a proposal; the orchestrator settles it. Nothing reaches the parent tree before settlement, so a rejected attempt is discarded rather than rolled back.

- **The world.** A local session's worktree lane uses `ScopeManager.ForkIsolated` with a `NewWorktreeSandbox`; the container lane uses the wired per-dispatch container setup and destroys it at seal. A container session uses its session container for proposals and a sibling container for report dispatches. Retained is the default when a trace store is configured; `agents.subagent.mode: shared` is the explicit opt-out that runs the sub-agent directly on the host.
- **The settlement.** `select` fast-forwards onto the dispatch basis and fails closed if the basis moved; `apply` is path-disjoint and uses a three-way merge on the worktree lane or whole-path post-images on the container lane. `release` exports the sealed delta to a retention path before tearing a worktree down; a container world is already gone, so only its manifest is consumed. `discard` throws the work away and removes the worktree or manifest directory. A sealed output is consumed exactly once (`ErrOutputConsumed`), and a settlement that landed an effect but failed its causal merge reports the diagnostic instead of inviting a retry that would apply it twice.
- **The judgement.** It is the orchestrator's, not a sub-agent's: the retained lane returns a diff and the orchestrator decides. No reviewer sub-agent is inserted automatically.

**Turn checkpoints.** `ShepherdTurnCheckpointer` (`internal/agent/runner/checkpoint.go`) adapts `goatherd.ScopeManager` to the loop's `TurnCheckpointer` interface. A checkpoint captures the workspace *and* the conversation, and a restore consumes it, so the two stay in step. This is per-attempt robustness inside a dispatch (a role sets `turn_checkpoints`), not a settlement mechanism — the worktree already makes the attempt disposable.

**Confinement.** Isolation is only real if the sub-agent's tools actually operate in the worktree, so the dispatch carries `SubAgentParams.Workdir`. `buildSubAgentRegistry` turns that into a per-scope `PathValidator` — rooted at the worktree, inheriting deny patterns and the approval callback, with a fresh approval cache — and `bash`/`powershell` use it as `cmd.Dir`. Confinement then rides that root: `agents.subagent.containment` jails writes to it, and `agents.subagent.network` governs egress from it.

**Teardown.** A worktree lives until settlement: `select`/`apply`/`discard` end the child scope and remove it, and `release` tears it down explicitly. A container world is destroyed at seal, so its manifest is the proposal and no live world remains to settle. A worktree left behind by a killed run is detected through the git worktree registry, cleared, and retried once on the next `Create`. Recovery reports stale entries instead of deleting them automatically.

**Limits.** A worktree checks out tracked files only, so gitignored build inputs must be recreated by `retained_worktree_bootstrap`. The worktree lane shares the host process, network, and object store; the container lane's boundary is the container. Egress is therefore a separate axis, not a side effect of isolation — see the [isolation vs egress matrix](./configuration.md#the-isolation-vs-egress-matrix) for what each lane governs and what it does not.

---

## Tool execution

### Registry

File: `internal/tools/tools.go`

`NewRegistry()` pre-registers the built-in leaf tools. One shell tool is
platform-selected (`powershell` on Windows, `bash` elsewhere), so 24 of the
25 leaf tools register on a given OS:

| Category | Tools |
|---|---|
| Filesystem (read) | `read`, `ls`, `file_info` |
| Filesystem (mutate — always need approval) | `write`, `edit`, `replace`, `delete`, `patch` |
| Search | `grep`, `glob` |
| Shell (always need approval) | `bash`, `powershell` |
| VCS / data (per-action approval) | `git`, `json_query` |
| Network | `webfetch` (read-only), `http` (per-action approval) |
| Go / dev tooling | `go_outline`, `go_test`, `go_mod`, `diff`, `staticcheck`, `bisect`, `go_refactor` (needs approval) |
| Utility | `calculate`, `question` |

Additional tools are registered by the CLI layer after `NewRegistry()`:
- `memory_search`, `memory_add`, `memory_delete`, `memory_update`, `memory_promote`, `memory_search_sessions`
- `skill`, `plan`, `todowrite`
- `delegate`, `list_subagents`, `subagent_jobs`
- `role` (host mode), `supervisor` (when tracing/supervision is live), `proposal` (retained mode), `apply_out` (isolated workspace)
- Any MCP tools from connected servers

Two environment gates then subtract from that set, because a tool that
cannot run in this session is per-call prompt cost that buys a failed
call later:

- **Go toolchain** — `go_test`, `go_mod`, `go_outline`, `go_refactor`,
  `staticcheck` are dropped when the project root is not inside a Go
  module (`GoToolAvailable`). The same rule already governed sub-agent
  registries; the main session now honours it too. `GoToolchainLeafTools()`
  and `IsGoToolchainTool` read one list, so the two paths cannot gate
  different tools.
- **Memory** — the six `memory_*` tools are dropped in a one-shot session
  (`yaah "prompt"`), which is a single non-interactive turn with no later
  conversation to recall into. The `## Memory Guidelines` prompt block is
  gated on the same value, so the prompt never advertises a tool that is
  not registered.

Both gates resolve **once**, at wiring time. The tools array is part of
the cached prompt prefix, so a set that changed mid-session would
invalidate the provider's cache on every later call and cost more than
the schemas save. Dropped names are recorded in the session's
`workspace_omitted_tools`, keeping "the model didn't" distinguishable
from "the model couldn't".

### Workspace seam (`internal/tools/workspace.go`)

Every filesystem and process operation a tool performs goes through one interface, so a single tool implementation can run on the host during a normal session and inside an isolated substrate during an isolated run:

```go
type Workspace interface {
    ResolvePath(path string) (string, error)   // containment
    WorkDir() string                           // cwd for relative paths and commands
    Local() bool                               // is this the host filesystem?
    Shell() (string, string)                   // ("sh","-c") or ("pwsh","-Command")
    ReadFile(ctx, path) ([]byte, error)
    WriteFile(ctx, path, data, perm) error     // atomic
    Stat(ctx, path) (fs.FileInfo, error)
    Lstat(ctx, path) (fs.FileInfo, error)
    ReadDir(ctx, path) ([]os.DirEntry, error)
    Remove(ctx, path) error
    MkdirAll(ctx, path, perm) error
    Join(elem ...string) string
    Exec(ctx, ExecRequest) (ExecResult, error)
}
```

Two implementations:

| | Backing | Writes | Exec |
|---|---|---|---|
| `localWorkspace` | the host filesystem, through the session's `PathValidator` | temp file plus rename (crash-safe) | `exec.CommandContext`, combined output |
| `sandboxWorkspace` (`workspace_sandbox.go`) | a `goatherdsandbox.Sandbox` | temp file plus rename **inside** the sandbox | in-band through the sandbox |

**Injection.** `Registry` carries both a `PathValidator` and a `Workspace`. Setting only a validator — which every existing caller does — derives a `localWorkspace` from it, so migrating a tool needs no change at its construction site. Both are injected into each tool, because unmigrated tools still need the validator. `SetWorkspace` overrides the derived workspace with an isolated one.

**Why this is the prerequisite for a container backend.** Pointing the tools at a host-visible mount would isolate the *files* while leaving the *processes* on the host, which defeats the purpose of a container. `sandboxWorkspace` therefore routes commands through the sandbox, and `PowerShellTool` refuses a non-local workspace through `requireLocal` rather than silently running the command somewhere the caller did not intend.

Every filesystem tool routes its I/O through a `Workspace` — including the
process-spawning tools (`git`, `diff`, `go_mod`, `go_test`, `staticcheck`,
`bisect`), which run with the workspace as their working directory. A
sub-agent's tools therefore act on its worktree, not the yaah process's cwd.

**Host-only by design (3).** `role` (manages harness role files the host reads), `background_process` (drives the in-memory host process manager), and `go_refactor`, whose dependency view uses `imports.Process` and `importer.Default()` export data from the host toolchain. Its project-file reads and writes go through the injected `Workspace`; what keeps it host-only is dependency metadata, not source access. Isolating it means providing that metadata inside the sandbox, not swapping calls.

**Isolation gate.** `Registry.SetWorkspace` refuses a non-local workspace, reporting unmigrated and host-only tools as separate categories. The refusal is unconditional with respect to the markers, so a forgotten annotation cannot silently weaken it; the markers only produce those diagnostics. `TestRegistry_EveryToolIsClassified` requires every registered tool to be a `FilesystemTool`, a `HostOnlyTool`, or on a short deliberate non-filesystem allowlist, so an unclassified tool fails the build rather than becoming an isolation hole.

The gate turns on which host-only tools a registry holds. The default
`NewRegistry()` refuses an isolated workspace because it contains
`go_refactor`; a sub-agent registry whose role profile excludes the three
host-only tools (`role`, `background_process`, `go_refactor`) is accepted.
The orchestrator runs on the host; a dispatched sub-agent can run isolated.

### Tool execution flow (`executeAndCollect` — middleware path)

```
executeAndCollect(ctx, calls, messages):
  for each tool call:
    spawn toolDispatch.run:
      1. Acquire subAgentSem (delegate only) and toolConcurrency semaphores
      2. Publish ToolStartEvent; start the tool span
      3. Registry.Execute(ctx, name, args)
      4. Parse escalation/watchdog data; truncate with spill
      5. Publish ToolEndEvent; emit hooks
    collect results in call order
    build []ToolResult for middleware
    append tool result messages to *messages and persist
```

Approval is not checked here: the `approval` middleware strips denied calls in
`PostModel` and supplies synthesized results through `Step.SynthesizedResults`,
which `executeToolPhase` merges before dispatch.

### Dangerous tools

Tools implement the `DangerClassifier` interface (`internal/tools/tools.go`) to
declare whether they require user approval:

```go
type DangerClassifier interface {
    IsDangerous(argsJSON string) bool
}
```

Tools that always require approval (the filesystem mutators `WriteTool`,
`EditTool`, `ReplaceTool`, `DeleteTool`, `PatchTool`, and the shells
`BashTool`, `PowerShellTool`) return `true` unconditionally. `GitTool`,
`JSONQueryTool`, and `HTTPTool` classify per-action by inspecting their JSON
arguments — e.g. `git add`/`commit` are dangerous while `git status`/`diff` are
not. `GoRefactorTool` and `PlanTool` also implement the classifier. Tools that
are never dangerous (`ReadTool`, `GrepTool`, `GlobTool`, etc.) simply don't
implement the interface.

`Loop.classifyGate` (`agent_safety.go`) checks whether a tool implements
`DangerClassifier` and calls `IsDangerous` when present. The approval
middleware (`pipeline/approval.go`) uses this single path for all tools, and
also applies `MCPApproval` to MCP-served tools.

All file-path-accepting tools support `~` expansion via `expandHomeDir()`
in `helpers.go`. Shared filesystem helpers (`rgAvailable`, `commonIgnoreDirs`)
also live there; output truncation (`truncateOutput`) lives in `result.go`
with the other result caps.

---

## Context management

### Three-tier architecture

yaah uses a **three-tier** context management system to prevent conversation
history from exceeding the model's context window:

| Tier | Mechanism | Cost | When |
|------|-----------|------|------|
| 0 — Soft-prune | Elide stale tool-output content (no LLM call) | ~µs | Before each LLM call (`PrepareStep`) and after each tool batch (`PostTool`) |
| 1 — Compaction | LLM-powered summarization of old messages | ~tokens | Proactive (50% window) or reactive (overflow) |
| 2 — Trim | Deterministic oldest-message removal (no LLM) | ~µs | Fallback when LLM summarization fails |

Compaction is triggered proactively when effective tokens reach 50% of
`ContextWindow` or raw (cache-inclusive) tokens reach `raw_compaction_threshold`
(default 0.5; the zero value falls back to 0.25), with a 64K minimum target
floor; reactively when the provider signals context overflow; or when the
message count exceeds `CompactMaxMessages` (regardless of token estimates — guards against
unbounded message accumulation when pruning keeps effective tokens below
the token threshold).

**Files:** `internal/agent/pipeline/pruner.go`, `internal/agent/pipeline/softprune.go`,
`internal/agent/agent_prune.go`, `internal/agent/agent_context.go` (`*Loop` methods:
`compactContext`, `trimContext`, `ForceCompact`), `internal/agent/context_manager.go`
(`ContextManager` — compaction/trim policy and state), `internal/agent/context/`
(pure helpers: `MessageTokens`, `PreflightTokens`, `SplitTail`, `PruneMessages`,
`ChunkSplit`, truncation) — a leaf package with no `internal/agent` imports.

### Tier-0: Soft-prune (`Pruner`, `SoftPruneMiddleware`)

Before compaction ever fires, the `SoftPruneMiddleware` reclaims context by
eliding the *content* of stale tool-result messages. The tool message itself
stays in the conversation (preserving the `tool_call_id` linkage required by
the Chat Completions wire format), but its body is replaced with a compact
placeholder:

> `[output pruned — 12345 chars omitted to save context; re-run the tool if you need it again]`

The algorithm (`Pruner.Mark`) walks messages backwards from the most recent,
shielding:
- The last `MinTurns` user turns (default 1)
- The last `ProtectTokens` tokens of tool output (default 2,000)
- Results from `ProtectedTools` (default: `skill`, `delegate`)

Walk termination boundaries (cheap per-turn cost):
- A non-index-0 system message (compaction summary — older is already summarized)
- Already-pruned tool messages are skipped (continue), allowing the walk to
  find newer unpruned messages between the protected turn window and the
  first pruned block. The system-message boundary is the walk's only hard
  termination guarantee; without a prior compaction the walk scans to index 0.

After compaction rebuilds the message list, the pruned set is reset so the fresh
tail is re-evaluated from scratch.

`Pruner.Filter` produces a copy of the message slice with stubbed content at
request-build time (before the provider call). When nothing is pruned (early
session), the fast path returns the input slice unchanged — zero allocation.

Observability: `context.prune` JSONL hook events and `prune` OTel spans (child of
the turn span) record every Mark pass, including "considered, decided not to"
outcomes.

### Token estimation

Primary: the API-reported cache-inclusive prompt size
(`LastPromptTokensInclusive`) for display; compaction uses the provider-shaped
`LastPromptTokens` (see usage shapes above).
Fallback (first call, before any API response): `PreflightTokens()` which
uses a `len(content) / 4` heuristic with a configurable multiplier
(`EstimateFactor`, default 1.3) to compensate for provider tokenizers
systematically undercounting code and JSON payloads.

### Mid-tool-loop compaction

Compaction is safe to run even while the model is mid-tool-loop. The
token-budgeted `SplitTail` split (`internal/agent/context`) is
boundary-aligned: it never cuts a tool-call/result pair, so compacting during
a tool loop preserves tool-call linkage and the model can continue without a
bricked conversation.

### Pre-compaction pruning (`PruneMessages`)

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
keep as many recent turns as fit in 25% of the context window, with:

- A 2K–8K clamp: `min(8000, max(2000, floor(window * 0.25)))` so huge
  windows don't over-preserve and small windows keep a usable floor
- **Boundary alignment**: the split lands on turn boundaries
  (user → assistant → tool results), so a tool-call/response pair is never
  split — walking backward over turns, and forward within an oversized turn
  to find the earliest message that fits
- **User-message anchor**: the most recent user message is always in the
  tail (when its turn fits the budget), preventing the active task from
  being summarized away

### LLM-powered compaction (`compactContext`)

1. Check the anti-thrashing guard: skip if the last 2 compactions each saved less than `max(ContextWindow/20, 2000)` tokens (~5%, with a 2K floor).
2. Check token budget: compute effective tokens — when cached prompt
   tokens are reported (`LastCachedPromptTokens > 0`), subtract them from
   `LastPromptTokens` so heavily-cached conversations don't over-trigger
   compaction. If `effectiveTokens <= ContextWindow * threshold`, return.
3. Compute the preservation split via token budget + boundary alignment.
4. Prune old messages via `PruneMessages()`.
5. Send pruned history to the LLM summarizer with a structured prompt
   (`## Goal`, `## Constraints & Preferences`, `## Progress` with
   `### Done` / `### In Progress` / `### Blocked`, `## Key Decisions`,
   `## Next Steps`, `## Critical Context`, `## Relevant Files`). The request
   leaves `max_tokens` unset, so the provider's configured/default output cap
   applies (`providers.<name>.max_tokens` or the provider default).
6. On re-compaction (second+), passes the previous summary to the LLM and
   asks it to **update** rather than re-summarize from scratch, preserving
   continuity across multiple compactions.
7. Replace old messages with a system message containing the summary.
8. Track compaction effectiveness: if savings fall below
   `max(ContextWindow/20, 2000)` tokens (~5%, with a 2K floor), increment the
   anti-thrashing counter. After 2 consecutive ineffective compactions, skip
   further compaction.
9. If the LLM call fails or returns empty, try chunked compaction when the old tail is large enough; otherwise fall back to `trimContext()`.

### Usage shapes and the context display

Providers report prompt usage in two shapes:

- **OpenAI-compatible:** `prompt_tokens` already includes cache reads.
  `PromptIncludesCache` is set to `true` at decode time.
- **Anthropic-shaped:** `input_tokens` excludes cache reads, which arrive
  separately as `cache_read_input_tokens`. `PromptIncludesCache` stays
  `false`.

`Usage.InclusivePromptTokens()` normalizes both (`prompt + cache` for
Anthropic, `prompt` for OpenAI), including streamed aggregates where
`PromptTokensDetails` is nil. The views read `LastPromptTokensInclusive`, so the context percentage uses the
real context size. Compaction keeps using the provider-shaped
`LastPromptTokens` for its raw gate; this display change does not alter the
compaction thresholds.

### Sub-agent result capping

Sub-agent results are capped by `resolveOutputLimit` (per-call limit, then
role config, then the 50 KiB default) and truncated head-only with an
explicit `...[sub-agent output capped at N]` marker. A separate 25%-of-context
trim bounds the result before it reaches the orchestrator.

### Overflow recovery

When the provider returns a context overflow error, `llm.Client.Call`
triggers an aggressive compaction at 40% of the window (up to 3 attempts)
before retrying. The proactive path is the `compaction` middleware's
`PrepareStep` hook, which uses `PreflightTokens()` (`internal/agent/context`)
with the configurable `EstimateFactor` (default 1.3) before the first API
call.

### Truncation fallback (`trimContext`)

Removes oldest messages one at a time until the token budget is met. Always
preserves the system message at index 0.

---

## Session persistence

File: `internal/agent/persist.go` (`SessionPersister`)

When `Loop.Persister` has a database, every message is persisted to SQLite
as it is appended. This enables session resume across process restarts and
crash recovery.

### Persistence points

| Point | What | Where |
|---|---|---|
| First turn | System prompt + user message | `Loop.initMessages` (`lifecycle_init.go`) |
| Subsequent turns | User message only | `runMiddleware`, before loop entry |
| Assistant response | Content + tool calls (serialized as JSON) | `runMiddleware`, after `LLM.Call` |
| Tool results | Result content, call ID, tool name | `executeAndCollect`, after each tool execution |
| Compaction summaries | New synthetic messages; the persister rebases indices | `ContextManager.OnMessagesReplaced` / `SessionPersister.Rebase` |

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
  → loopBuilder calls persister.SetMsgIdx(s.msgIdx) so new messages
    get the correct DB indices
  → Loop runs normally, appending new messages to both memory and DB
```

### Error handling

`SessionPersister.Persist` logs errors to stderr (`warning: db persist: ...`)
but never returns them. The agent loop continues even if the database is
unavailable. The persister does not advance the message index on a failed
insert.

### Two stores

Session state lives in `state.db`; the append-only Shepherd evidence and
recovery record lives in `trace.db`. They intentionally overlap in message
content and are not replicas. See [storage.md](./storage.md) for the ownership,
fidelity, and retention contract.

---

---

## Semantic memory search

Files: `internal/memory/vector.go`, `internal/memory/memory.go`,
`internal/memory/message_repo.go`, `internal/tools/memory.go`

yaah optionally stores vector embeddings alongside memory entries and session
messages, enabling semantic search via cosine similarity. The system works in
five stages:

### 1. Embedding model (`Embedder` interface)

```go
type Embedder interface {
    Embed(ctx context.Context, text string) (Embedding, error)
}
```

`HTTPEmbedder` implements this by calling an OpenAI-compatible
`/v1/embeddings` endpoint — this works with LM Studio, Ollama, llama.cpp,
and any cloud provider. The model is configured via the `embedding` config
stanza, which references a provider's `base_url` (e.g. `lmstudio` →
`http://127.0.0.1:1234/v1`).

### 2. Storage (`embedding` BLOB column)

Both the `memory` and `messages` tables carry an optional `embedding BLOB`
column. Embeddings are serialized as little-endian
float32 arrays. At startup, `ReconcileEmbeddings` backfills entries that
were created before the embedding provider was configured.

### 3. Embed-on-save

- **memory**: `MemoryAddTool.Execute` calls `AddMemoryDedup`, then
  `EmbedMemoryAsync`, and waits for the embedding to be stored so new
  entries are immediately searchable. `embMu` guards the `embedder` field,
  not SQL or network calls.

- **messages**: `AddMessage` fires `embedMessageAsync` in a background
  goroutine (user and assistant messages only; tool results are skipped).
  The agent loop never blocks on embedding — the DB stays open for the
  life of the serve process, so goroutines complete naturally.

### 4. Search (`SearchMemoryVector` / `SearchMessagesVector`)

1. Embed the query via `d.Embedder().Embed(ctx, query)`
2. Scan all rows with non-null `embedding`
3. Compute `CosineSimilarity` between the query embedding and each stored
   embedding (brute-force scan)
4. Sort descending by score, return top-K

### 5. Tool dispatch

Both `memory_search` and `memory_search_sessions` try vector search first
(`memory_search` skips it when a tag filter is set; session search skips it
for an empty query).
If the embedder is nil, the embedding call fails, or no vector results are
found, they fall back to FTS5 keyword search. The CLI memory commands
(`yaah memory add` / `yaah memory search`) load config and set the embedder
independently (they open their own short-lived DB connections).

### Scope tiers

Every atom carries a `scope`: `global` outlives a session, `session` is
distilled from one conversation. `memory_search` filters to `global` by
default, because session atoms are a single conversation's working context and
must not surface in an unwidened recall. Naming `scope:"session"` reaches atoms
distilled from earlier conversations — they persist until pruned (30 days
unused, or an explicitly deleted session) — and `scope:"all"` spans both.
`all` is a filter, never a stored tier: `memory.NormalizeScope` rejects it on
write, so no row can be created invisible to a `scope = ?` predicate. A call's
`scope` argument outranks the tool's instance default, which exists so an
unwidened search stays global rather than so a search can never be widened.

### Cosine similarity

```go
func CosineSimilarity(a, b []float32) float32 {
    if len(a) != len(b) { return 0 }
    var dot, normA, normB float32
    for i := range a {
        dot += a[i] * b[i]
        normA += a[i] * a[i]
        normB += b[i] * b[i]
    }
    if normA == 0 || normB == 0 {
        return 0
    }
    return dot / (float32(math.Sqrt(float64(normA))) * float32(math.Sqrt(float64(normB))))
}
```

With a 768-dimension embedding (nomic-embed-text) and hundreds of entries,
a full scan completes in under a millisecond — no ANN index needed.

## Hook events

File: `internal/agent/events/hookevent.go`

yaah emits structured JSONL events to `<HookDir>/<session-id>.jsonl` for external agent integrations (e.g. entire-agent-yaah). Events are fire-and-forget — failures are silent and never break the loop.

### Event types

| Event | Emitted when |
|---|---|
| `session.start` | First `Run()` call for a new session |
| `session.end` | `Run()` returns (with `exit_reason` field) |
| `turn.start` | Start of each iteration (with `turn` number) |
| `tool.start` | Before each tool execution (with `tool_name`, `tool_args`) |
| `tool.end` | After each tool execution (with `tool_result`, `duration_ms`, `tool_error`) |
| `context.prune` | After each tool batch — soft-prune outcome (with `prune_reason`, `prune_candidates`, `prune_marked`, `prune_reclaimed`, `prune_protected`, `prune_committed`, `prune_total_marked`) |
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
    PruneReason   string        `json:"prune_reason,omitempty"`
    PruneCandidates int         `json:"prune_candidates,omitempty"`
    PruneMarked     int         `json:"prune_marked,omitempty"`
    PruneReclaimed  int         `json:"prune_reclaimed,omitempty"`
    PruneProtected  int         `json:"prune_protected,omitempty"`
    PruneCommitted  bool        `json:"prune_committed,omitempty"`
    PruneTotalMarked int        `json:"prune_total_marked,omitempty"`
}
```

---

## Streaming

File: `internal/agent/llm/stream.go` (`runStream`, `assembleStreamed`), `internal/agent/events/events.go`

When the provider implements `StreamProvider` **and** `OnToken` is non-nil (a broker/View is active), `llm.Client.Call` selects the streaming path and `runStream()` assembles the response; `lifecycle_init.go` wires the client's token/thinking callbacks to broker events. The path:

1. Reads from a `<-chan StreamChunk`.
2. Accumulates `delta.Content` into a string builder, publishing each chunk as a `TokenDeltaEvent` to the internal broker.
3. Accumulates `delta.ReasoningContent` separately, publishing as `ThinkingEvent`.
4. Assembles tool calls from streamed deltas (by index), ordering them before returning.
5. Rejects truncated streams (`finish_reason == "length"` with pending tool calls).

**Event flow:** Instead of direct callbacks, streaming deltas and tool lifecycle events are published to the `pubsub.Broker[Event]`. Consumers (TUI, REPL) receive them via their `View.HandleEvent` implementation. The broker is created internally when `Loop.View` is set — callers never touch it.

Events published during streaming and tool execution:

| Event type | When | Fields |
|---|---|---|
| `TokenDeltaEvent` | Each streamed content delta | `Text string` |
| `ThinkingEvent` | Each reasoning/thinking delta | `Text string` |
| `FlushEvent` | Model finishes a streaming segment | `Content string` |
| `ToolStartEvent` | Before tool execution | `ID int64`; `Name, Args string` |
| `ToolEndEvent` | After tool execution | `ID int64`; `Name, Args, Result, Error string`; `Duration time.Duration` |
| `SubAgentStartEvent` | Sub-agent begins | `SubAgentID, Role, Model, Prompt string` |
| `SubAgentEndEvent` | Sub-agent completes | `SubAgentID, Role, Model, Prompt, Error, Result string`; `Duration time.Duration` |
| `DoneEvent` | Agent loop finishes | `Response, Error, FinishReason, ResponseModel string`; `ContextTokens, ContextWindow, LastPromptTokens, LastPromptTokensInclusive int`; `Usage` |

## Provider interface

File: `internal/agent/llm/types.go`

```go
type Provider interface {
    Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error)
}

type StreamProvider interface {
    Provider
    SendStream(ctx context.Context, req types.ChatRequest) (<-chan providers.StreamChunk, <-chan error)
}
```

The `internal/providers/` package implements OpenAI Chat Completions and Anthropic Messages API clients. Providers are resolved by the CLI layer from `~/.yaah/config.yaml` and passed to `Loop.Provider`. `config.Provider.API` (`"openai"`, `"anthropic"`, or `"copilot"`) determines which client is created — both implement the same `Provider` + `StreamProvider` interfaces.

---

## Engine-View architecture

Files: `internal/agent/events/events.go`, `internal/agent/view.go`, `internal/pubsub/broker.go`

The agent loop communicates with consumers (TUI, REPL, sub-agent runner) through a single typed event interface. View delivery is broker-based; a few control hooks (`ApproveFn`, `ContinueAfterMaxIter`, `OnUsage`, `OnSubUsage`) remain on the loop.

### Event interface

All events implement the sealed `Event` interface via an unexported `eventMarker()` method. This prevents external packages from adding new event types — the engine owns the event contract:

```go
type Event interface {
    eventMarker()
}
```

Concrete event types (all pointer receivers for clean nil checks in type switches):

| Event | Description |
|---|---|
| `TokenDeltaEvent` | Streaming token delta from the model |
| `ThinkingEvent` | Reasoning/thinking text (DeepSeek R1, Claude) |
| `FlushEvent` | Streaming segment complete; content should be committed to the message list |
| `ToolStartEvent` | Tool execution begins (name + args) |
| `ToolEndEvent` | Tool execution completes (name, args, result, duration, error) |
| `SubAgentStartEvent` | Sub-agent dispatch begins (role, model, prompt) |
| `SubAgentEndEvent` | Sub-agent dispatch completes (role, duration, error) |
| `EscalationEvent` | Sub-agent raises a structured escalation (severity, summary, detail, suggestion) |
| `DoneEvent` | Agent loop finishes (response, error, context stats) |
| `CompactionStartedEvent` | Context compaction begins (before/target tokens, reason) |
| `CompactionDoneEvent` | Context compaction finishes (before/after tokens, savings %, method, elapsed) |

### View interface

Consumers implement `agent.View`:

```go
type View interface {
    HandleEvent(Event)
}
```

`HandleEvent` is called sequentially from a dedicated forwarder goroutine — implementations must be safe for use from a single goroutine. The agent loop does not call `HandleEvent` concurrently.

### BrokerView adapter

`BrokerView` adapts a `pubsub.Broker[Event]` subscription into a `View` by running a forwarder goroutine that reads from the subscription channel and calls `HandleEvent` on the target view.

```go
broker := pubsub.NewBroker[Event]()
bv := agent.NewBrokerView(broker, myView)
defer bv.Close()
// ... publish to broker ...
```

The agent loop creates the broker and `BrokerView` internally in `applyDefaults` when `Loop.View` is set. Lifecycle: create in `applyDefaults` → publish events during `Run` → close broker on `Run` return.

### Consumers

| Consumer | View impl | File |
|----------|-----------|------|
| TUI | `App.HandleEvent` (type switch; `App` is the `agent.View`) | `internal/tui/proxy.go` |
| REPL | `terminalView` | `cmd/yaah/view_terminal.go` |
| Sub-agents | `agent.NoopView` | `internal/agent/runner/runner.go` |
| MCP serve | `agent.NoopView` | `cmd/yaah/serve.go` |
| ACP serve | `acp.View` + `acp.ViewWithWrite` | `internal/acp/view.go` |

Control-plane messages (todos, questions, approvals, model lists) use `control.Msg` (`internal/control`) — a separate channel from the broker events.

---

## TUI architecture

Files: `cmd/yaah/tui.go`, `internal/tui/`

The TUI is a [tview](https://github.com/rivo/tview)/[tcell](https://github.com/gdamore/tcell) application running the agent loop in a background goroutine. Communication from the agent goroutine to the TUI goes through the typed event broker — the agent loop publishes `Event` values to the internal `pubsub.Broker[Event]`, and a forwarder delivers them to `App.HandleEvent`, the `agent.View` implementation in `internal/tui/proxy.go`, which funnels mutations through an ordered event queue onto the tview main goroutine. Streaming token deltas collect in a pending buffer and are flushed on `FlushEvent` and at done/tool/compaction boundaries; refresh work is coalesced rather than timer-debounced.

**Layout:** Pages → Flex hierarchy with a conversation view (collapsible tool/reasoning blocks rendered by components under `internal/tui/components/`), an info pane (session/context/MCP/config sections), a dedicated Subagents pane for active dispatches, a todo sidebar, and a multi-line input. All colors come from the shared theme in `internal/tui/colors/theme.go`.

**Command palette:** typing `:` in the composer shows a typeahead strip of matching commands (Tab completes, Up/Down cycle, Esc dismisses); `ctrl+p` opens the same set as a command palette (`:help`, `:clear`, `:compact`, `:banner`, `:model`, `:search`, `:steer`, `:followup`, `:verbose`, `:stop`, `:quit`). `:model` queries providers' model lists and filters live; selection swaps provider/model mid-session.

Component inventory and rendering rules: [TUI component system](./tui-components.md).

---

## OpenTelemetry observability

Files: `internal/observability/`, `internal/agent/loop.go`, `internal/config/load.go`

yaah emits traces via OTLP HTTP to any OpenTelemetry-compatible backend. Tracing is off by default; enable with `observability.otel.enabled: true` in `config.yaml`.

### Initialisation

`observability.Setup(ctx, cfg)` in `internal/observability/otel.go` creates a `TracerProvider` (and optionally a `MeterProvider`) configured for the OTLP endpoint. It returns a `shutdown` function that `cmd/yaah/wiring.go` defers. The loop gates spans on `Loop.Config.OtelEnabled`.

`Config.ExtraProcessors` allows injecting additional `sdktrace.SpanProcessor` instances alongside the OTLP batcher. Serve mode (`cmd/yaah/serve.go`) uses this to attach an in-memory `BufferingSpanProcessor` so traces can be queried via the `traces` MCP tool without an external backend. When `Endpoint` is empty, the OTLP exporter is skipped entirely and only the extra processors receive spans.

### In-memory span buffer

`BufferingSpanProcessor` in `internal/observability/buffer.go` collects completed spans in a ring buffer (capped at 10,000 spans). It supports flat queries (`Traces()`), hierarchical tree rendering (`TraceTree(traceID)`), and reset between runs. The `yaah mcp serve` MCP server exposes this via the `traces` tool.

### Span hierarchy

| Span type | Location | Contents |
|---|---|---|
| Tool name | `toolDispatch.run` in `agent_dispatch.go` | Operation name is the tool name. `tool.args` attribute (200-char truncated JSON). `result` event with truncated output. Errors are recorded via `RecordError`. |
| `subagent: <role> — <description>` | `toolDispatch.run` in `agent_dispatch.go` | Role + task description in the operation name. `dispatched` event with role and task text. `completed` / `failed` event on finish. |
| `llm.stream` / `llm.chat` | `observability/provider.go` and `observability/trace.go` | Streaming uses `llm.stream`; non-streaming uses `llm.chat`. `tokens` event with prompt/completion/total tokens, message count, and system length. `llm.duration_ms` on `llm.chat`. |
| `prune` | `SoftPruneMiddleware` (`PrepareStep` / `PostTool`) → `observability/trace.go` | `prune.reason`, `prune.candidates`, `prune.marked`, `prune.reclaimed_tokens`, `prune.protected_skipped`, `prune.committed`, `prune.total_marked`. Emitted after each pass, including non-committed passes. |

`conflict.check` is a hook event, not a span; the turn span carries
`conflict.files` and a `conflict.detected` event when conflicts are found.

### Propagation to sub-agents

`Loop.Config.OtelEnabled` is propagated through `taskRunnerOpts.OtelEnabled` → `makeTaskRunner` → `subLoop.Config.OtelEnabled`. The parent span context flows via `context.Context` through `Registry.Execute(runCtx, ...)`, so sub-agent tool spans appear as children of the sub-agent span in the trace waterfall.

### Configuration

```yaml
observability:
  otel:
    enabled: false
    endpoint: "localhost:4318"
    service_name: "yaah"
    traces: true
    metrics: false
```

The OTel SDK also honours standard environment variables for sampling, TLS, and resource attributes (`OTEL_RESOURCE_ATTRIBUTES`, `OTEL_TRACES_SAMPLER`, etc.).

---

## Message types

File: `internal/types/types.go`

```go
type Message struct {
    Role             string        `json:"role"`
    Content          string        `json:"content"`
    Refusal          string        `json:"refusal,omitempty"`
    ReasoningContent string        `json:"reasoning_content,omitempty"`
    ToolCalls        []ToolCall    `json:"tool_calls,omitempty"`
    ToolCallID       string        `json:"tool_call_id,omitempty"`
    Name             string        `json:"name,omitempty"`
    CacheControl     *CacheControl `json:"cache_control,omitempty"`
}
```

Helper constructors: `SystemMsg(content)`, `UserMsg(content)`, `ToolResultMsg(callID, name, content)`.

Tool definitions sent to the LLM use `ToolDef`/`ToolFn` structs built by `buildToolDefs()` from the registry.
