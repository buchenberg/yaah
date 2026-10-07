# Agentic Framework Comparison — A Code-Level Deep Dive

*Analysis date: 2026-10-07 (second pass; the original 2026-08-26 analysis was fully re-verified against current checkouts). All findings below are drawn from reading the actual source in each repository, not from READMEs or marketing docs. Line counts computed with `find`+`wc` over non-test source, excluding `node_modules`, build dirs, vendored and generated code where noted. Non-framework checkouts (trace kernels, libraries, scratch projects) are excluded from the comparison.*

---

## 1. What's in the directory

Nine comparable agent frameworks:

| Repo | Language | Primary LOC (non-test) | Category |
|---|---|---:|---|
| **yaah** | Go | ~45k Go (+33k tests) | Terminal coding-agent harness (vendor-free, single binary) |
| **naah** | C# / .NET 10 | ~12k C# (+6k tests, +2k SPA) | Port of yaah to .NET (phases A+C complete) |
| **crush** | Go (Charm) | ~106k Go | Terminal coding agent (fantasy LLM lib, Bubble Tea v2 TUI) |
| **goose** | Rust | ~539k Rust (+201k UI TS) | AI agent platform: CLI + Electron desktop + in-process platform extensions |
| **opencode** | TypeScript (Effect) | ~500k TS across 36 packages | Coding agent platform mid-rewrite V1→V2 (durable, event-sourced) |
| **kilocode** | TypeScript | ~692k TS (incl. ~44k generated) | Fork of opencode + VS Code/JetBrains extensions + Agent Manager |
| **pi** | TypeScript | ~209k TS across 14 packages | Minimal agent monorepo (agent/ai/coding-agent/durable/chord/tui) |
| **deepagents** | Python | ~30k core SDK (+215k deepagents-code harness) | LangChain/LangGraph middleware SDK that now also ships its own harness |
| **hermes-agent** | Python | ~899k Python | Kitchen-sink personal agent (CLI, gateway, plugins, cron, desktop) |

Excluded as non-frameworks: the shepherd checkouts (a programmable meta-agent and trace-kernel ports — execution substrates with formal semantics, not agent harnesses; yaah integrates the Go kernel as a library, covered under yaah's own row), `OpenHands`, `ReCount`, `deepseek-harness`, `gastown` (scratch/platform projects not in scope), `tview`/`tviewmd` (terminal UI libraries), `jaeger-mcp-server(-rs)` (observability tooling).

**Family relationships discovered in code:**

- **kilocode is a hard fork of opencode.** Kilo changes to shared upstream files carry inline `kilocode_change` markers, enforced by CI (`script/check-opencode-annotations.ts`); `src/kilocode/**` is the exempt zone for Kilo-owned code. Comparing the two is really "what does a product-focused fork add to a platform?"
- **naah is a port of yaah** (the Go tree is explicitly named the reference implementation). Its 164-line `AgentLoop.cs` is a faithful translation of yaah's loop-and-middleware shape into .NET idioms.
- **yaah's soft-pruner is a documented Go port of kilocode's compaction prune pass** (comment in `yaah/internal/agent/pipeline/pruner.go`: "a Go port of kilocode's compaction.ts prune pass"; kilocode's prune constants still live in the shared `packages/opencode/src/session/compaction.ts`). Ideas already cross-pollinate across this directory.
- **deepagents-code is the SDK eating its own dog food**: a ~215k-LOC terminal coding agent built on the deepagents middleware SDK, shipped in the same monorepo.

---

## 2. The Agent Loop

The single most differentiating component. Two architectural theses emerge: **own the loop** (yaah, pi, opencode V2, goose, hermes) vs **delegate the loop** (crush delegates to `charm.land/fantasy`; deepagents delegates to LangGraph's `create_agent`).

### 2.1 yaah — explicit loop + middleware pipeline + checkpoint/restore

`internal/agent/loop.go` — `Loop.runMiddleware()`:

```
for {                                     // outer: checkpoint-restore retries
  for iter := 0; iter < MaxLoopCycles; iter++ {   // inner: model turns
    checkpointTurn(ctx, messages)          // git snapshot before each turn
    step, req := buildTurnRequest(...)     // pipe.RunPrepareStep (all middleware)
    guardContextBeforeCall(...)            // token budget guard
    result := l.LLM.Call(ctx, req)         // streaming + fallback + retry
    pipe.RunPostModel(ctx, &msg, step)     // approval, permissions, limits
    executeToolPhase(...)                  // concurrent dispatch, PostTool hooks
    persist incrementally (MsgIdx diff)
  }
  // max iterations: rewind via checkpoint + retry with guidance, else MaxIterationsError
}
```

Distinctive properties:

- **Turn checkpoints with rewind-and-retry.** Before every model turn the loop snapshots workspace + conversation. On a hard tool-phase failure or iteration exhaustion it *rewinds to the last checkpoint and retries with failure guidance* (bounded by `MaxTurnRestores`, default 3 — `internal/agent/turn_checkpoint_loop.go:15`) instead of failing the run. No other framework in the directory does conversation+workspace transactional rollback at the turn level.
- **Overflow-recovery adoption**: if `LLM.Call`'s internal compaction replaced the conversation, the loop detects the slice replacement and adopts the compacted baseline. This is defensive correctness you normally only find in much larger codebases.
- **Curated sub-agent pipeline**: `buildPipeline()` returns `pipeline.NewSubAgentPipeline()` for sub-agents — they skip persistence, compaction, spawning, and quality gates by construction. The orchestrator registers **11 middleware by name (9 on by default)**: steer, followup, compaction, soft_prune, approval, inline_limit, tool_concurrency, loop_detection, conflict_detect, plus opt-in permission and prompt_caching. Sub-agent loops get their own trio (tool_concurrency, shepherd_trace, and permission when the parent passes rules); shepherd trace initialization is session-wide infrastructure shared by the supervisor and supervised-task tools, not orchestrator middleware.
- **Loop-shape**: the pipeline `Middleware` interface is three hooks (`PrepareStep`, `PostModel`, `PostTool`) — the smallest complete interception surface of any framework here, and everything interesting is a middleware, not loop logic.
- **Lean sub-agent prompts**: sub-agents get a dedicated 927-byte identity plus user/project context — not the orchestrator's identity — so ~1.2k tokens of unactionable orchestration guidance is absent from every dispatch.

### 2.2 crush — loop lives in a library; the harness is a concurrency shell

The actual model-turn iteration is inside `charm.land/fantasy`'s `Agent.Stream()` (v0.45.1); crush calls it with `PrepareStep` callbacks (`internal/agent/agent.go:856`, `:1472`, `:1867`) that:

- drain queued follow-up prompts into the step (with per-accept-sequence cancel coverage — a queued prompt covered by an earlier cancel is dropped *and still gets its terminal `RunComplete` event* via `publishCanceledQueueDrops`, `agent.go:465-495`),
- place Anthropic `cache_control` on the system message and last 2 messages (`agent.go:893-907`) plus the last tool definition (`agent.go:725`),
- work around provider media limitations,
- create the assistant message row before streaming starts.

Loop termination is expressed as **`StopWhen` conditions**, notably: context-window threshold → set `shouldSummarize` and stop (auto-summarization runs after the stream; separate large/small-window thresholds, skipped entirely if the window is unknown — protecting local models; `agent.go:1095-1117`), and repeated-tool-call detection (windowed, `agent.go:1118`). The result is that crush's "agent loop logic" is really *policy around* a library loop. The engineering weight is in dispatch concurrency: accepted (fire-and-forget) runs, cancel-on-entry, busy-queueing, per-session mutexes — still the most careful cancellation protocol of the Go frameworks. New since August: a read-only **plan mode** agent, **Claude Channels delivery** (session injection, exactly-once routing, reply-back — `channel.go`, `channelreply.go`), hyper-provider credits display, a theme system, and configurable request timeouts. The `internal/agent` package is ~17k lines including its tools subpackage (`agent.go` itself is ~5.9k of top-level files); `coordinator.go` has grown to 1,871 lines of provider wiring.

### 2.3 goose — the monolith loop, now with a state-machine escape hatch

`crates/goose/src/agents/agent.rs` is now a **6,569-line** file containing the reply loop: `reply()` (agent.rs:2116) streams from the provider, routes tool calls through MCP extension streams (`tool_stream` merges tool results + action-required messages via `tokio::select!`), handles approval routing, retries (`retry_manager`), stop-hook block caps (a stop hook that keeps blocking the turn is overridden after `GOOSE_STOP_HOOK_BLOCK_CAP` consecutive blocks — a failure mode nobody else explicitly guards), auto-compaction inline before the call, and the `final_output_tool` contract for subagents. The major architectural move since August: `reply()` can delegate to **`reply_with_state_machine`** (agent.rs:1791), backed by the new `goose-agent` crate — an effect-based `Machine`/`Operation`/`Inference`/`Step` state-machine core that is wasm-compatible. The monolith remains the load-bearing wall, but the escape hatch exists.

### 2.4 opencode V2 — the durable event-sourced runner (frozen mid-migration)

The V2 "SessionRunner" (`packages/core/src/session/runner/llm.ts`) carries the same explicit checked/unchecked migration checklist as in August — 12 checked, 11 unchecked — and **not one box has advanced since 2026-08-26**. Loop shape unchanged: drain durable inbox (steer → queue) → per turn: promote steers/queued inputs, resolve model, project history, materialize tools, `compactIfNeeded` (TurnTransition as a typed `Effect.die` defect), exactly one provider turn under a semaphore, fiber-per-tool settlement, `Step.Ended` with snapshot diff. Durable prompt admission is still the standout: `SessionV2.prompt()` writes a durable `session_input` row before waking the runner, so crashes lose at most an admission, not a turn.

The V1/V2 coexistence verdict also stands, but with a corrected file: the live V1 monolith is `packages/opencode/src/session/prompt.ts` (1,631 LOC, still serving the HTTP API), not the 1-line re-export the previous analysis cited in `packages/core`. The 263 commits since August touched `packages/core/src` only ~9 times — the V2 core is effectively frozen while activity moved to product surface (the "opencode Go" paid tier, stats site, model-catalog churn) and continued V1 teardown at the edges (QuestionV1 contract, `SessionV1.WithParts`, v1 permission errors removed). Two generations of loop still coexist.

### 2.5 kilocode — same engine, different policies (and a scheduler)

Being an opencode fork, the loop is shared; Kilo's additions visible at loop level: a `KiloSessionPromptQueue` replacing upstream queuing, compaction payload-recovery, chunked compaction, and — new since August — a **session goal subsystem** (goal tracking with a completion gate that only permits wait-tools — `schedule_wakeup`, `cron_create`, `background_process` — once a goal is set; `kilocode/session/goal/policy.ts`), **scheduled wakeups and cron** (`kilocode/wakeup/`, `cron_create/list/delete` tools), and a run of session-loop hardening files (`steering.ts`, `retention.ts`, `mode-reminders.ts`, `continuation.ts`, `drain.ts`). Delegation policy has softened from "hard one-level" to **configurable depth**: `task.ts:134` enforces `cfg.subagent_depth ?? 1` — nested subagents are opt-in, one-level by default. The permission-ceiling inheritance from parent (merged over the subagent's own policy) deliberately survives.

### 2.6 pi — the cleanest loop in the directory (and it is growing a durability layer)

`packages/agent/src/agent-loop.ts`, now **940 lines** (was 792) — still the whole loop, still the cleanest. The two signature safety details remain: `stopReason === "length"` fails every tool call in the message via `failToolCallsFromTruncatedMessage` (a token-limit-truncated message can yield tool calls that parse and validate but are silently incomplete — pi refuses to execute them), and the `AgentMessage` vs LLM `Message` separation with `convertToLlm` running exactly once per turn at the call boundary. Tool schemas moved from zod to **TypeBox**; the tool set is 8 types (read, bash, powershell, edit, write, grep, find, ls) with a default of just 4 (read, bash, edit, write). The big story since August is infrastructure: **pi-durable** (23.9k LOC — durable/restartable sessions with progress commit intervals and windowed shell output), **pi-chord** (11.7k LOC concurrency/context substrate with a 2,205-line delta tracker), **pi-codemode** (a sandboxed JavaScript runtime served as an MCP tool), and **pi-env** (SSH remote execution environments with cross-platform daemons). The package count went from 9 to 14. One wart: `interactive-mode.ts` in the coding agent is now 7,072 lines — the pi harness is growing its own god-file.

### 2.7 deepagents — the loop is a graph you assemble (and now a harness too)

`libs/deepagents/deepagents/graph.py` (~1,011 lines) still builds a LangGraph `CompiledStateGraph` from `langchain.agents.create_agent` plus a middleware stack — but the default stack **no longer includes planning todos** (the todo tool ships only in the OpenAI Codex harness profile). The `DeltaChannel` state optimization survives (checkpoint growth O(N²)→O(N), snapshot every 50 messages), as do filesystem offload with head+tail previews and the 85%-trigger summarization. New since August: a **profiles subsystem** (provider+harness profiles keyed by model, driving middleware exclusion and tool-description overrides), a `RubricMiddleware` (a grader sub-agent self-eval loop, 1,438 lines), content-addressed binary blob offload, and two new backends (`StoreBackend` on LangGraph Store, `LocalShellBackend`). Async sub-agent tools were renamed (`start_async_task` etc.) and now target subagents deployed via LangSmith Deployments. The headline: **deepagents-code** (~215k LOC, v0.1.83) — a full terminal coding agent with a Textual TUI, skills engine with trust model, 20+-module hooks engine, plugins, and MCP providers, built entirely on the SDK.

### 2.8 hermes-agent — the maximalist loop, decomposed

The August god-file verdict needs revision. `agent/conversation_loop.py` is now **1,835 lines** (was ~4.6k), decomposed into 33 `turn_*`/iteration modules (`turn_preflight.py`, `turn_api_call.py`, `turn_finalizer.py`, …) — the decomposition the previous analysis noted as "ongoing" has substantially landed. The dual-budget loop guard survives (`api_call_count < max_iterations and iteration_budget.remaining > 0) or _budget_grace_call`), though defaults changed: config default is now 250 iterations (constructor default unlimited, shared with subagents; subagent cap 50). Preflight compression (≤3 passes), protocol-tail repair, and tool-schema-aware token estimation all remain. Steer handling changed mechanism: steering text is now appended as a standalone user message *after* the last tool result rather than merged into it (role-alternation intent preserved). Meanwhile `run_agent.py`'s `AIAgent` is now a composition of **13 mixins with an 84-parameter constructor** — the loop got cleaner while the agent object got wider.

### 2.9 naah — thin orchestrator, pipeline-per-turn, now with a desktop

`Naah.Core/Agent/AgentLoop.cs` is still 164 lines and works exactly as documented: builds a `TurnContext` (history, `IChatClient`, registry, per-turn `Channel<TokenDeltaEvent>`/`Channel<ToolEvent>` for backpressure-capable streaming) and calls a pre-built `AgentMiddlewareDelegate` until `ShouldContinue` is false or `MaxTurns`. Phase status has advanced: phases A *and* C are complete — compaction works (`CompactionMiddleware` plus chunking/pruning/summarization compactors), OTel is wired (AgentActivitySource, AgentMetrics, in-memory span exporter), and a **Photino.NET cross-platform desktop** (phases 1–5 of 6) embeds Naah.Server in-process and serves the React SPA from embedded resources. One caveat that matters for the port's story: sub-agents are still non-functional — `SubAgentRunner.cs` (280 LOC) is DI-registered but called by nothing, and the `task` tool returns a stub string. The default middleware composition also omits `FallbackMiddleware` (registered but never composed). Naah.Core has grown to 20 packages and ~11.8k non-test C#.

---

## 3. Token Efficiency

Mechanisms observed, strongest → weakest per framework:

| Framework | Proactive (no LLM call) | Reactive compaction | Prompt caching | Tool-output bounding |
|---|---|---|---|---|
| **yaah** | **Soft-prune**: stubs stale tool results in the *ephemeral request only* (originals never mutated) | LLM compaction at configurable threshold, pre-call guard, in-`Call` overflow recovery | Anthropic breakpoints (cap 4): system first, then tool msgs at turn boundaries, newest first | Tool-result line/byte caps + disk spill with path hint |
| **kilocode** | **Prune**: `PRUNE_MINIMUM=20k`, `PRUNE_PROTECT=40k` tokens, protected tools (`skill`), preserve-recent tail; safe re-prune at cache-invalidating boundaries (`PruneReason: normal \| post-compaction \| payload-limit`) — constants live in the shared upstream `session/compaction.ts` | Compaction with chunking + payload recovery | Upstream mechanisms | `TOOL_OUTPUT_MAX_CHARS=2000` w/ truncation marker. **swe-pruner was removed** (2026-08-18, "remove experimental task-aware output pruning") — the per-call semantic-filtering experiment did not survive |
| **opencode V2** | Tool-output store with managed output paths (externalize bulky results); context epochs bound re-projection | `compactIfNeeded` pre-call + single-shot `compactAfterOverflow` on context-overflow failure | `promptCacheKey` (OpenAI) from session id; Anthropic `cache_control` in the `llm` package schema | Producer capture limits at tools; bounding at registry settlement |
| **crush** | — | Auto-summarize `StopWhen` (context-window aware; separate thresholds for large vs small windows; skipped entirely if window unknown) | `cache_control` on system + last 2 messages + last tool definition | — |
| **deepagents** | **Filesystem offload**: oversized tool results written to the backend FS, replaced by head+tail preview (5+5 lines) + read instructions; content-addressed binary blob offload | `SummarizationMiddleware` (default trigger: 85% of context), fallback tail-clip on `ContextOverflowError` | Upstream Anthropic/Bedrock/Fireworks prompt-caching middleware | Per-tool size thresholds |
| **pi** | Compaction as **pure functions** (I/O in session manager); file-operation carryover across compactions | LLM summarization w/ dedicated system prompt | Provider-level (pi-ai package) | Read `DEFAULT_MAX_BYTES` cap + bash `OutputAccumulator` line/byte caps |
| **goose** | `compute_tool_call_cutoff` (tool-result cutoff policy) | Auto-compact at threshold % (configurable) inline in the loop; plus the new `goose-context-management` crate — a standalone summarize/compact library exposed cross-language via goose-sdk uniffi | — | Tool-call cutoff |
| **hermes** | **Preflight compression**: rough token estimate *including tool schemas*, multi-pass (≤3) | Context compressor w/ protect-first/last-N windows; context-engine plugins; manual compression feedback loop | — (session DB persists text-only summaries of multimodal results) | Multimodal→text summary at persistence |
| **naah** | Compaction middleware (working port, incl. chunking/pruning/summarization compactors) | present | — | — |

**Analysis.** The three-tier ladder still holds (prune cheaply without an LLM → summarize with a model call → overflow-recover as last resort), and yaah + kilocode still implement it most completely. The August "most novel single idea" (kilocode's swe-pruner) has been **removed by its own authors** — the lesson being that per-call semantic filtering added latency and complexity for unclear wins. The durable novelty prizes now go to hermes (tool-schema-aware estimation — a real accounting blind spot everyone else still has) and deepagents (content-addressed blob offload). pi's compaction purity remains the best-factored implementation, and its new durable layer adds restartability without changing that. opencode's context *epochs* + output *store* remains the most ambitious durable-context design, but its own checklist shows it is still being built out.

---

## 4. Sub-agent Capabilities

| Framework | Dispatch model | Isolation & limits | Notable |
|---|---|---|---|
| **yaah** | **Role registry**: `SubAgentRole` → `RoleProfile` (tools, `MaxLoopCycles`, `MaxToolTurns`, JSON mode, timeout, nesting depth). Every dispatch resolves an explicit role (built-in + filesystem role files) | Curated sub-agent pipeline (no persistence/compaction/spawning); `MaxSubAgentConcurrency`; per-role timeouts | Background jobs manager (session-scoped usage attribution); `supervised_session` + `supervisor` tools with workspace+conversation checkpoints and rollback; optional per-variant git worktree isolation; per-sub-agent Shepherd causal trace (parent inspects child's trace on failure); broker `SubAgentStart/End` events |
| **crush** | Coordinator with named agents ("coder", "task"); `runSubAgent` creates a real SQLite *task session* | Session-per-subagent (persistent, inspectable); cost propagated to parent | Sub-agent results are first-class sessions (resumable, browsable) — the nicest persistence story |
| **goose** | `subagent_handler`: recipe-driven subagent tasks | `max_turns` per task; cancellation tokens; `return_last_only` mode | **`final_output_tool` contract** — the subagent must call `final_output` to terminate; the loop warns and continues if it hasn't. Streams notifications back to the parent |
| **opencode** | `task` tool: `subagent_type` + prompt; agent configs marked `mode: "subagent"` | `deriveSubagentSessionPermission`; optional `task_id` **resume** of a prior subagent session; step limits per agent | Background subagents behind an experimental flag, with strong prompt-side guardrails |
| **kilocode** | Same `task` tool, plus `task-background-process` | **Configurable delegation depth** (`subagent_depth`, default 1); permission ceilings *inherited from parent* and merged over the subagent's own policy (upstream removed inheritance; Kilo deliberately preserves it) | Agent Manager (VS Code): multi-session orchestration with per-session git worktrees, now with worktree *pooling*, multi-project management, and PR review/merge actions; new session goal subsystem gates long-running agents on wait-tools (`cron_create`, `schedule_wakeup`, `background_process`) |
| **pi** | **None built in** — subagents ship as an *extension example*; a new experimental durable `Subagent` extension exists in the durable runtime only | Depends on extension | Architectural statement: the harness core stays loop-pure; teams add delegation policy |
| **deepagents** | Sync `SubAgent` specs via `task` tool, plus `AsyncSubAgentMiddleware` exposing `start_async_task`/`check_async_task`/`update_async_task`/`cancel_async_task`/`list_async_tasks`; async subagents can target LangSmith Deployments | Async tasks run as LangGraph async graph executions; state carries live task list | Async subagents are state-machine citizens, not bolt-ons |
| **hermes** | `delegate_task` (split across 10 files, ~5.7k total): single + batch parallel dispatch | `max_spawn_depth` (default 1), `max_concurrent_children` (default 10), child timeout, MCP-toolset inheritance, sub-agent approval callbacks (auto-deny/auto-approve), interrupt propagation | Kanban dispatch runs **inside the gateway** by default (3k-line dispatcher); mixture-of-agents moved from tool to slash-command/virtual-model mode; kanban-first review flow (`request_review`/`request_changes`, review forks defer compaction) |
| **naah** | **Still a stub**: `SubAgentRunner` is DI-registered but unreached; the `task` tool returns "would handle" placeholder text | — | Phase B/D/E remain; sub-agents are the port's biggest gap |

**Analysis.** Three schools persist: (1) **role/registry-based** (yaah, crush's named agents, opencode/kilo's agent configs) — declarative, auditable, tool-restricted; (2) **ad-hoc programmatic** (hermes `delegate_task`, deepagents specs) — maximum flexibility, policy lives in code; (3) **extension-based** (pi) — delegation as policy the team adds. The most production-grade limits are still hermes' (depth × concurrency × timeout × approval plumbing all configurable); the cleanest conceptual model is yaah's roles-as-data; the best async story is deepagents' five-tool task state machine; the best observability story is the crush/goose "sub-agent = real session" persistence, with yaah's per-sub-agent causal traces close behind. The interesting movement since August is *scheduling*: both kilocode (goal-gated cron/wakeups) and hermes (in-gateway kanban) are converging on long-running, queued, multi-agent work dispatch.

---

## 5. Tool Use

| Framework | Built-in count | Registry pattern | Signature |
|---|---:|---|---|
| **hermes** | **86 registered** at runtime (43 tool modules, 267 files after decomposition) | Import-time `registry.register()`; auto-discovery; **toolset system** reorganized around postures: web, terminal, file, browser, coding, debugging, safe, delegation, kanban, discord, spotify, feishu, … | Largest surface; toolsets are user-facing product |
| **yaah** | ~46 tools (53 tool files) | `tools.Registry`; `path_validator`, `conflict_tracker` cross-cutting; go-specific suite (`go_outline`, `go_refactor`, `go_test`, `go_mod`, `bisect`, `staticcheck`) | Deepest language-specific tooling of any framework here |
| **crush** | ~30 per session (29 `New*Tool` constructors; 16 always-on + question + 8 LSP + 2 MCP-resource + agent/agentic_fetch wrappers; web tools are sub-agent-only) | Tool interface + **`.md` description file pairs** (self-documenting tools); `hooked_tool.go` decorator injects PreToolUse hooks; **LSP suite as first-class tools** + sourcegraph | LSP integration is the standout |
| **goose** | Architecture shifted: `developer` is now an **in-process platform extension** implementing `McpClientTrait`; memory/computer moved to a `goose-mcp` crate; platform extensions include todo, summarize, analyze, apps, chatrecall, code_execution, scheduler | Extension manager (6,216 lines) spawns external MCP servers (stdio + streamable HTTP); `tool_confirmation_router`; malware-check gate | In-process platform extensions + out-of-process MCP — the boundary moved, the openness stayed |
| **opencode V2** | ~14 core built-ins | **Opaque `Tool.make` values**; Location-scoped `ToolRegistry`; permissions attached at invocation-context construction; output bounding only at settlement | Most formally specified tool architecture |
| **kilocode** | upstream + `repo_clone`, `kilo_local_recall` (session-transcript search), `semantic_search` (LanceDB/qdrant codebase index via `kilo-indexing`), `kilo_memory_recall`, `lsp`, `suggest`, `apply_patch` | Upstream + marker rules | Three distinct recall surfaces (transcripts, code index, project memory) — easy to conflate, worth distinguishing |
| **pi** | 8 tool types, **default 4** (read, bash, edit, write) | **TypeBox** schemas (zod is gone); `wrapToolDefinition` + `defineTool` for extensions; `file-mutation-queue` serializes writes | Smallest viable set; quality over breadth |
| **deepagents** | FS tools derived from **`BackendProtocol`** (state, filesystem, sandbox, composite, context_hub, langsmith, store, local_shell) | Backend abstraction means the same `read_file` tool works against in-memory state, a sandbox, or a hub | Cleanest hexagonal tool boundary; deepagents-code layers a full harness (skills, hooks, plugins) on top |

**Approval/permissions** (cross-cutting): yaah pipeline approval+permission middlewares; crush `permission` package + hooks (hooks run *before* permission checks; only PreToolUse is implemented); opencode `PermissionV2` with typed sources; kilocode merged parent ceilings; goose confirmation router + modes; hermes per-subagent approval callbacks; naah `ApprovalMiddleware` + `IApprovalHandler` (though `FallbackMiddleware` is registered but never composed); deepagents `HumanInTheLoopMiddleware` with interrupt configs. All treat approval as a first-class pipeline stage — table stakes in 2026.

---

## 6. Overall Architecture

**Process & packaging**

- Single static binary: **yaah** (cross-compile matrix in CI), **crush** (CGO_ENABLED=0), goose (single Rust binary via goose-cli).
- Runtime-dependent: **goose** (Rust + tokio; Electron desktop — the Ink text UI was deprecated and removed, replaced by the native Rust CLI), **naah** (.NET 10; Spectre.Console REPL + SignalR web + Photino.NET desktop).
- Node-runtime monorepos: **opencode** (bun, turbo, SST, 36 packages incl. sdk/console/desktop/function/slack), **kilocode** (bun, turbo; VS Code *and* JetBrains extensions, kilo-console/kilo-web-ui), **pi** (npm, lockstep versioning, 14 packages at 1.0.4).
- Python: **hermes** (pip/uv installable app + Docker images + an Electron/React desktop app with heavy momentum + a TUI stack), **deepagents** (uv monorepo: SDK + deepagents-code harness + acp + evals).

**State & persistence**

| Store | Frameworks |
|---|---|
| SQLite (embedded) | yaah (modernc, FTS5 sessions+memory), crush (sqlc codegen), opencode V2 (Drizzle + migrations), hermes (SessionDB FTS5), goose (session manager); kilocode ships a drizzle SQLite layer but sessions remain filesystem JSON (`~/.local/share/kilo/storage/`, path-array keys) |
| Durable session logs / checkpoints | pi-durable (restartable sessions with progress commit intervals), opencode V2 (event-sourced rows), deepagents (LangGraph checkpoints + DeltaChannel) |

**Eventing / observability**

- **OTel-first**: yaah (tracing spans per prompt/turn/tool + Shepherd trace facts + in-memory span buffer), opencode (OTLP export in core), goose (tracing crate), crush (PostHog events), hermes (observability plugin + telemetry surge), deepagents (LangSmith integration).
- **Event-sourced**: opencode V2 (EventV2 sequence numbers, replayable projections, session input inbox).
- **In-process pub/sub**: yaah typed broker (`PublishMustDeliver` semantics for terminal events), crush pubsub broker (lossy + must-deliver modes), kilocode/opencode v1 `Bus`, pi `EventStream` (push/end result channel — still the simplest).

**Extension models**

1. **MCP clients**: everyone (yaah stdio+HTTP+serve-as-server, crush, goose — goose is MCP-native with in-process platform extensions, opencode/kilo, pi now ships pi-mcp + codemode-as-MCP-tool, hermes w/ toolset inheritance into subagents).
2. **Middleware/pipeline**: yaah, naah, hermes (context-engine/memory/model-provider plugin types), opencode plugins, deepagents AgentMiddleware stack + profiles.
3. **Skills** (SKILL.md discovery): yaah, crush, kilocode/opencode, hermes, deepagents (`SkillsMiddleware`; deepagents-code adds a trust model) — an emerging cross-tool standard.
4. **Hooks** (user shell commands on lifecycle events): crush (Claude-Code-compatible protocol), goose (stop-hook block caps; hooks/plugin system ~2.3k lines), yaah (HookEvent bus), deepagents-code (20+ hook modules).
5. **Out-of-process everything**: goose (extensions are servers or in-process platform extensions).

---

## 7. SOLID Design Assessment

Grading engineering *as found in code*, weighted by consequence:

### SRP — Single Responsibility

- **Best: pi (the loop) and opencode V2.** pi's agent-loop file is still the cleanest single artifact in the directory (940 lines, compaction pure, message models separated) — but the coding-agent harness around it is growing file-heavy (`interactive-mode.ts` 7,072 lines). opencode V2 remains orchestration over small collaborators with written invariants.
- **Excellent: yaah.** Loop/pipeline/middleware/tools/jobs are separate packages; the loop file contains only orchestration; every middleware is one file + tests. 11 registered orchestrator middleware, 9 default, all named and individually testable.
- **Excellent (improved): hermes.** The August god-file verdict is outdated: the 4.6k-line conversation loop is now 1,835 lines decomposed into 33 turn-scoped modules. The counter-trend: `AIAgent` accreted to 13 mixins and an 84-parameter constructor.
- **Good: crush** at the package level, weaker at the file level — `agent.go` still mixes dispatch concurrency, streaming callbacks, caching placement, and summarization policy; `coordinator.go` grew to 1,871 lines.
- **Good: naah** — each middleware one concern (with the quirk that `FallbackMiddleware` exists but is never composed).
- **Mixed: deepagents** — the SDK layering improved (profiles, backends), and it inherits LangChain's boundary blur.
- **Weakest: goose.** `agent.rs` grew from 4.4k to **6,569 lines**. The `goose-agent` state-machine crate is a genuine extraction path, but today the monolith is bigger than in August.

### OCP — Open/Closed

- **Middleware pipelines remain the OCP win**: yaah (11 middlewares, config-driven enable/disable by name), naah, hermes context-engine plugins, deepagents AgentMiddleware + profiles (per-model middleware exclusion is a genuinely new OCP mechanism).
- **goose's MCP-everything** is OCP via process boundaries — adding capabilities never touches core, at the cost of a 6,216-line extension manager.
- **crush's hooks + skills** extend behavior without code changes; loop policy changes still require editing `agent.go`.
- **kilocode's fork model is an anti-OCP pressure**: upstream evolution conflicts with Kilo divergence; the team compensates with CI-enforced marker discipline plus promise-facade and architecture ratchets — process substituting for architecture, still working.

### LSP — Liskov Substitution

- **yaah `Middleware`**: small interface where implementations synthesize tool-result messages for *removed* tool calls so provider invariants hold — middleware can't break the `tool_call_id` pairing rule.
- **crush `fantasy.Agent`**: the loop contract is externalized; substitutable but the contract lives outside the repo — a reviewability cost.
- **deepagents `BackendProtocol`**: genuine behavioral substitution (state vs sandbox vs store vs shell filesystems behind identical tools), now with eight backends.
- **goose's `goose-agent` machine traits**: an explicit substitutability play — the same loop contract implemented twice (inline monolith and state machine), selected by flag.

### ISP — Interface Segregation

- **Go idiom enforced**: crush's AGENTS.md mandates consumer-defined small interfaces; yaah's `Middleware` (3 methods), `Compactor`, `TurnCheckpointer` are narrow by construction.
- **opencode's service-per-concern** is fine-grained DI, though the count of services a feature touches is high.
- **hermes `AIAgent`**: the 13-mixin, 84-parameter constructor is the biggest ISP violation in the directory — consumers see everything.
- **pi's config-bag**: effectively ISP via optional functions (`getSteeringMessages?`, `prepareNextTurn?`, `shouldStopAfterTurn?`).

### DIP — Dependency Inversion

- **Strongest: opencode** — Effect `Layer`s are a DI graph with compile-time composition and scoped lifetimes. The most industrial DI discipline in the directory.
- **naah** uses `Microsoft.Extensions.DependencyInjection` — idiomatic, boring, effective.
- **yaah** inverts through a hand-written composition root (`cmd/yaah/wiring*.go`) — explicit, greppable, no framework.
- **goose/hermes** lean on global singletons (`SessionManager::instance()`, `PermissionManager::instance()`; hermes' 13 mixins) — the least inverted designs here.
- **pi** threads dependencies explicitly through constructors.

### Verdict table (October 2026)

| | SRP | OCP | LSP | ISP | DIP | Overall feel |
|---|---|---|---|---|---|---|
| yaah | ●●● | ●●● | ●●● | ●●● | ●● | Best middleware factoring; checkpoint/restore still unique |
| pi | ●●● | ●● | ●●● | ●●● | ●● | Cleanest loop; harness files growing heavy |
| opencode V2 | ●●● | ●●● | ●● | ●● | ●●● | Industrial; V2 frozen mid-checklist while V1 still serves |
| hermes | ●● | ●● | ●● | ● | ● | Loop decomposed (33 modules); agent object accreted (84 params) |
| crush | ●● | ●● | ●● | ●●● | ●● | Concurrency-mature, file-heavy |
| naah | ●●● | ●●● | ●● | ●● | ●●● | Faithful port; sub-agents still a stub |
| deepagents | ●● | ●●● | ●●● | ●● | ●● | Elegant SDK now dogfooded by its own 215k harness |
| kilocode | ●● | ● | ●● | ●● | ●● | Product velocity via fork discipline + scheduler ambitions |
| goose | ● | ●●● | ●● | ●● | ● | Extension-native; monolith grew, state-machine hatch open |

---

## 8. Head-to-Head Summary

| Dimension | Winner (code-justified) | Runner-up |
|---|---|---|
| **Agent loop design** | **yaah** (checkpoint/restore, overflow-adoption, curated sub-pipeline) | pi (cleanest minimal), opencode V2 (durable semantics) |
| **Loop simplicity/readability** | **pi** | naah |
| **Token efficiency breadth** | **kilocode** (prune+chunk+recovery; note swe-pruner was removed) | yaah (3-tier ladder), deepagents (FS offload) |
| **Token efficiency novelty** | **hermes** (tool-schema-aware estimation) | deepagents (content-addressed blob offload) |
| **Sub-agent machinery** | **hermes** (depth/concurrency/timeouts/approvals + in-gateway kanban) | kilocode (goal-gated scheduling), yaah (roles-as-data) |
| **Sub-agent auditability** | **crush** (sub-agent = persistent, resumable session) | yaah (per-sub-agent causal traces via the Shepherd kernel) |
| **Tool breadth** | **hermes** (86 tools, toolsets) | crush (LSP suite) |
| **Tool architecture rigor** | **opencode V2** (opaque tools, settlement boundary) | deepagents (BackendProtocol, 8 backends) |
| **Extensibility** | **goose** (in-process platform extensions + MCP everything) | yaah/opencode middleware+plugins |
| **Persistence/durability** | **opencode V2** (event-sourced, durable admission) | pi-durable (restartable sessions), crush (SQLite sessions) |
| **Observability** | **yaah** (OTel spans + Shepherd facts) | opencode (EventV2 replay) |
| **SOLID overall** | **yaah / pi / opencode V2** (different weights) | naah |
| **Testability culture** | **hermes** (~44k test functions) | crush (golden-file TUI testing), pi (faux-provider harness) |
| **Local inference** | **goose** (`goose-local-inference`: candle/llamacpp/MLX, GGUF, model downloads) | — |

---

## 9. Per-Framework One-Paragraph Verdicts

**yaah** — Still the best factored loop-and-pipeline in the directory, with the most defensible failure semantics (turn checkpoints with bounded restore, overflow-adoption, synthesized-denial tool results that preserve provider invariants) and now a lean sub-agent prompt that strips ~1.2k unactionable tokens from every dispatch. Its middleware system is what crush's `agent.go` would be if it were decomposed, and its role-based sub-agent registry is cleaner than config-file agent definitions. The Shepherd-kernel integration (trace facts, supervised rollback) is a differentiator no other harness here has. Weakest area remains breadth-for-size: the Go tool suite is deep but the surface (web UI, TUI, ACP, MCP server, supervised sessions) is very broad for a single maintainer.

**naah** — A faithful architectural translation of yaah into .NET idioms that has quietly outgrown its "Phase A" label: working compaction, wired OTel, a 20-package core, and a new Photino cross-platform desktop shipping the React SPA. The honest caveat is sub-agents: the runner exists but the `task` tool is still a stub, so the port's most interesting feature is unreachable. The plan documentation (explicit phases, a "no TUI port" decision the Photino wrapper respects) remains a model for port projects.

**crush** — Still the most concurrency-mature Go harness (accepted runs, cancel sequencing, queue draining, RunComplete guarantees for every admitted call) with the best LSP tooling and pragmatic library delegation (`fantasy`) — at the price of loop policy living in a file-heavy agent package and the actual iteration logic being outside the repo. New plan mode and Claude Channels delivery show the harness absorbing product features without rearchitecting the loop.

**goose** — The extension-native framework, now with the directory's largest codebase (~539k Rust). The architecture moved under it: `developer` became an in-process platform extension, memory/computer moved to a `goose-mcp` crate, the Ink TUI was deleted in favor of a native Rust CLI, and a genuine local-inference stack (candle/llamacpp/MLX + resumable model downloads) plus an iroh-based p2p transport appeared. The 6.5k-line `agent.rs` monolith grew, but the `goose-agent` state-machine crate is a real extraction path. Choose it for ecosystem; the code aesthetics are still a construction site.

**opencode** — The most ambitious architecture in the directory, and since August it has been *frozen*: the V2 migration checklist has not advanced one box, the live V1 monolith still serves the HTTP API, and 263 commits went almost entirely to product surface (paid tier, stats site, model catalog). The durable event-sourced design remains the reference blueprint — but "if the migration lands" is now a two-month-old conditional. Today it is two generations of loop coexisting, with excellent blueprints and a paused construction site.

**kilocode** — Proof that a disciplined fork can out-product its upstream: configurable-depth delegation with inherited permission ceilings, compaction chunking + payload recovery, three distinct recall surfaces, and — new since August — a scheduler story (goal-gated sessions, cron, wakeups) plus Agent Manager worktree pooling and PR actions, JetBrains and console clients. The swe-pruner experiment was removed by its own authors, which is itself informative. CI ratchets (annotation checks, promise-facade and architecture gates) still hold the fork line against upstream drift.

**pi** — The minimalist thesis still executed: a 940-line loop with the two best loop-level safety details in the directory, TypeBox schemas, pure-function compaction, and the cleanest message-model boundary. The August snapshot missed its infrastructure trajectory: pi-durable (restartable sessions), pi-chord, pi-codemode, and pi-env SSH make pi the quiet leader in durable-session engineering at the library level. Watch `interactive-mode.ts` (7k lines) — the minimalist harness is growing a maximalist file.

**deepagents** — No longer just a middleware SDK: deepagents-code (~215k LOC) is a full harness with TUI, skills trust model, hooks, and plugins built on it, plus a profiles system, rubric self-eval middleware, and two new backends. The `BackendProtocol` remains the cleanest hexagonal tool boundary here. Value is still inversely proportional to how much you already dislike LangChain's boundaries — but the SDK now demonstrates its own patterns at harness scale.

**hermes-agent** — The maximalist, decomposing: the 4.6k-line loop is now 1,835 lines over 33 turn modules (the god-file verdict from August no longer applies to the loop), 86 tools over reorganized toolsets, ~44k test functions, and new subsystems since August — a package manager, gateway-hosted multi-agent rooms, a platform layer, and an Electron desktop with real momentum. The `AIAgent` 13-mixin/84-parameter constructor is the new god-object. Many loop defenses here (protocol-tail repair, tool-schema token estimation, multi-pass preflight) still exist nowhere else in the directory because nobody else has hit those bugs yet.

---

## 10. Cross-Pollination Map (what each should steal)

- **Everyone ← pi**: fail tool calls when `stopReason === "length"`; keep agent-domain message models separate from wire formats; steal pi-durable's restartable-session design before building your own.
- **Everyone ← hermes**: include tool schemas in token estimates; repair protocol-invalid tails before they become infinite empty-response loops.
- **Everyone ← kilocode**: prune at cache-invalidating boundaries only; goal-gated scheduling (an agent that can only call wait-tools once its goal is set) is a clean pattern for long-running sessions.
- **Everyone ← opencode**: durable prompt admission (inbox rows) so crashes can't lose an admitted user turn.
- **Everyone ← crush**: every admitted run gets exactly one terminal event, even when canceled before starting or dropped from a queue.
- **Everyone ← goose**: stop-hook block caps; the platform-extension pattern (in-process extensions behind an MCP-shaped trait) is the best of both worlds between native tools and MCP servers.
- **Everyone ← yaah**: turn-level checkpoint/restore with bounded retries; synthesized tool results for denied/dropped calls; lean per-role sub-agent prompts.
- **goose/hermes-adjacent ← the field**: decompose the god-file — the middleware pattern demonstrated by yaah/naah is the proven path, and hermes' 33-module turn decomposition is the proof it works at maximalist scale.

---

*Method note: findings reference specific files so every claim above is re-verifiable at the cited location — this pass re-verified all nine frameworks against checkouts dated 2026-09-30 through 2026-10-07 (goose and deepagents as fresh shallow clones at 2026-10-07 HEAD). LOC counts are `find`+`wc` over non-test source excluding `node_modules`, build/dist, and (where noted) generated code; they are not directly comparable to pygount's language-detection totals from the August pass. The shepherd checkouts (meta-agent + trace-kernel ports) were excluded from this edition as non-frameworks; yaah's use of the Go kernel is covered under yaah's own rows.*
