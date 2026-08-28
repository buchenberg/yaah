# Agentic Framework Comparison — A Code-Level Deep Dive

*Analysis date: 2026-08-26. All findings below are drawn from reading the actual source in each repository, not from READMEs or marketing docs. Line counts computed with pygount (excluding `.git`, `node_modules`, build dirs, and test fixtures where noted).*

---

## 1. What's in the directory

Fifteen checkouts, twelve of which are genuinely comparable agent frameworks:

| Repo | Language | Primary LOC | Category |
|---|---|---:|---|
| **yaah** | Go | ~39k Go (67k w/ JSON+YAML) | Terminal coding-agent harness (vendor-free, single binary) |
| **naah** | C# / .NET 10 | ~10k C# | Port of yaah to .NET (Phase A complete) |
| **crush** | Go (Charm) | ~67k Go | Terminal coding agent (fantasy LLM lib, Bubble Tea v2) |
| **goose** | Rust | ~139k Rust | AI agent framework: CLI + Electron desktop + MCP extensions |
| **opencode** | TypeScript (Effect) | ~274k TS core (1.4M monorepo) | Coding agent platform mid-rewrite V1→V2 (durable, event-sourced) |
| **kilocode** | TypeScript | ~416k TS (1.7M monorepo) | Fork of opencode + VS Code extension + Agent Manager |
| **pi** | TypeScript | ~129k TS | Minimal agent monorepo (agent/ai/coding-agent/orchestrator/tui) |
| **deepagents** | Python | ~1.7k core SDK | LangChain/LangGraph middleware SDK for "deep agents" |
| **hermes-agent** | Python | ~550k Python | Kitchen-sink personal agent (CLI, gateway, plugins, cron) |
| **shepherd** | Python | ~287k Python | Programmable meta-agent + trace kernel (v3 reference w/ formal semantics) |
| **shepherd-kernel-go** | Go | ~2.2k Go | Port of Shepherd's trace-kernel ABI (`shepherd.kernel.abi.v0`) |
| **shepherd-kernel-dotnet** | C# | ~1.4k C# | Same ABI port to .NET |

Not frameworks, excluded from comparison: `tviewmd` (terminal markdown viewer library), `entire-test` (demo scratch), `external-agents` (standalone agent binaries for the Entire CLI).

**Family relationships discovered in code:**

- **kilocode is a hard fork of opencode.** Its own AGENTS.md documents a "fork isolation rule": Kilo changes to shared upstream files must be mirrored under `src/kilocode/<same/path>.ts` behind `kilocode_change` markers, enforced by CI. Comparing the two is really "what does a product-focused fork add to a platform?"
- **naah is a port of yaah** (the Go tree is explicitly named the reference implementation, ~52k LOC target).
- **yaah's soft-pruner is a documented Go port of kilocode's compaction prune pass** (comment in `yaah/internal/agent/pipeline/pruner.go`: "a Go port of kilocode's compaction.ts prune pass"). Ideas already cross-pollinate across this directory.
- **The Shepherd kernel trio is a determinism experiment**: Python reference, Go port, and .NET port all produce *byte-identical digests* from shared golden test vectors against a frozen ABI.

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

- **Turn checkpoints with rewind-and-retry.** Before every model turn the loop snapshots workspace + conversation. On a hard tool-phase failure or iteration exhaustion it *rewinds to the last checkpoint and retries with failure guidance* (bounded by `MaxTurnRestores`, default 3) instead of failing the run. No other framework in the directory does conversation+workspace transactional rollback at the turn level.
- **Overflow-recovery adoption** (`loop.go:250-259`): if `LLM.Call`'s internal compaction replaced the conversation, the loop detects the slice replacement and adopts the compacted baseline — with a comment citing review findings B6/B7. This is defensive correctness you normally only find in much larger codebases.
- **Curated sub-agent pipeline**: `buildPipeline()` returns `pipeline.NewSubAgentPipeline()` for sub-agents — they skip persistence, compaction, spawning, and quality gates by construction.
- **Loop-shape**: the pipeline `Middleware` interface is three hooks (`PrepareStep`, `PostModel`, `PostTool`) — the smallest complete interception surface of any framework here, and everything interesting (16 middlewares: steer, followups, compaction, approval, permission, conflictdetect, inlinelimit, loopdetect, promptcaching, pruner, softprune, staleness, trace, toolconcurrency, scope_init, shepherd_trace) is a middleware, not loop logic.

### 2.2 crush — loop lives in a library; the harness is a concurrency shell

`internal/agent/agent.go` (8.6k lines in package). The actual model-turn iteration is inside `charm.land/fantasy`'s `Agent.Stream()`; crush supplies a `PrepareStep` callback that runs per step and:

- drains queued follow-up prompts into the step (with per-accept-sequence cancel coverage — a queued prompt covered by an earlier cancel is dropped *and still gets its terminal `RunComplete` event* so non-interactive callers don't hang),
- places Anthropic `cache_control` on the system message and last 2 messages,
- works around provider media limitations,
- creates the assistant message row before streaming starts.

Loop termination is expressed as **`StopWhen` conditions**, notably: context-window threshold → set `shouldSummarize` and stop (auto-summarization runs after the stream), and repeated-tool-call detection (`hasRepeatedToolCalls`, windowed). The result is that crush's "agent loop logic" is really *policy around* a library loop. The engineering weight is in dispatch concurrency: accepted (fire-and-forget) runs, cancel-on-entry, busy-queueing, per-session mutexes — the most careful cancellation protocol of the Go frameworks.

### 2.3 goose — the monolith loop

`crates/goose/src/agents/agent.rs` is a **4,427-line** file containing the reply loop: `reply()` streams from the provider, routes tool calls through MCP extension streams (`tool_stream` merges tool results + action-required messages via `tokio::select!`), handles approval routing, retries (`retry_manager`), stop-hook block caps (a stop hook that keeps blocking the turn is overridden after N consecutive blocks to avoid infinite loops — a failure mode nobody else explicitly guards), auto-compaction inline before the call, and a "final output tool" contract for subagents. Everything is async Stream composition. It works, it is battle-tested, but it is the least decomposed loop of the twelve — SRP suffers by design (see §7).

### 2.4 opencode V2 — the durable event-sourced runner

`packages/core/src/session/runner/llm.ts`. The V2 "SessionRunner" is mid-migration (its own header carries an explicit checked/unchecked checklist). Loop shape:

```
run()  → drain durable inbox (steer → queue) → while shouldRun:
  runTurn() → while needsContinuation (steps):
    load session, select agent, init context epoch
    promote steers/queued inputs (steer resets step counter)
    resolve model, project history from baselineSeq
    materialize tools (skipped on last step → toolChoice:"none" + MAX_STEPS_PROMPT)
    compactIfNeeded → if compacted, rebuild (TurnTransition as typed defect)
    llm.stream(request) — exactly one provider turn, events published under a semaphore
      tool-call events settle through registry, fiber per tool (FiberSet)
    await settlements; publish Step.Ended with snapshot diff (files changed this step)
    needsContinuation = any tool settled
```

Distinctive properties:

- **Durable prompt admission separated from execution**: `SessionV2.prompt()` writes a durable `session_input` row before waking the runner; the runner promotes inbox rows to visible messages at safe boundaries. Crashes lose at most an admission, not a turn (post-crash provider continuation recovery is explicitly still a design TODO).
- **Control flow via `Effect.die(TurnTransitionError)`** caught by `catchDefect` — compaction continuation and overflow recovery are typed defects, not booleans threaded through the loop. Overflow recovery is deliberately single-shot: "Post-compaction provider attempt cannot recover another overflow."
- **Steer/queue semantics are first-class loop citizens** (`promotion: "steer" | "queue"`), matching crush's queue-drain and pi's steering, but durable rather than in-memory.
- Cost: this is Effect-TS everywhere — `Layer`, `Service`, `FiberSet`, `Semaphore`, `Effect.fn` tracing. The V1 monolith (`session/prompt.ts` legacy path) still exists in parallel; the codebase literally contains two generations of loop.

### 2.5 kilocode — same engine, different policies

Being an opencode fork, the loop is shared; Kilo's additions visible at loop level: a `KiloSessionPromptQueue` replacing upstream queuing, compaction payload-recovery, chunked compaction, and the swe-pruner provider transform (see §3). Delegation policy is hard-coded: "Kilo keeps delegation one level deep to avoid recursive subagent chains" (`src/kilocode/tool/task.ts`), vs upstream's arbitrary agent configs.

### 2.6 pi — the cleanest loop in the directory

`packages/agent/src/agent-loop.ts`, **792 lines total** including comments — the whole loop. Shape:

```
runLoop():
  outer while(true):                       // follow-ups after natural stop
    inner while (hasMoreToolCalls || pendingSteering):
      inject pending steering messages
      message = streamAssistantResponse()  // transformContext → convertToLlm at boundary
      if stopReason error/aborted → end
      if stopReason === "length" → fail ALL tool calls (truncated-argument guard)
      executeToolCalls() (parallel, per-tool terminate flag)
      prepareNextTurn hook → may swap context/model/thinkingLevel per turn
      shouldStopAfterTurn hook → may end
      drain steering
    followUps? → continue outer
```

Two details worth calling out because nobody else has them:

1. **`stopReason === "length"` fails every tool call in the message** (`failToolCallsFromTruncatedMessage`): since streamed tool-call args are finalized by a salvage parser, a token-limit-truncated message can yield tool calls that *parse and validate but are silently incomplete*. pi refuses to execute them and tells the model to re-issue. This is a real production failure mode the others don't explicitly handle at loop level.
2. **`AgentMessage` vs LLM `Message` separation**: the context holds rich agent-domain messages; `convertToLlm` runs exactly once per turn at the call boundary, and `transformContext` allows arbitrary pre-transform. The harness thesis is "own the message model, rent the wire format."

### 2.7 deepagents — the loop is a graph you assemble

`libs/deepagents/deepagents/graph.py` (~1,050 lines) builds a LangGraph `CompiledStateGraph` from `langchain.agents.create_agent` plus a middleware stack (planning todos, filesystem, subagents, summarization, skills, memory, permissions, HITL interrupts). The "loop" is LangGraph's tool-node/agent-node cycle; deepagents' value is the middleware composition and one clever state-level optimization: `DeepAgentState` uses a **`DeltaChannel` on messages to reduce checkpoint growth from O(N²) to O(N)** (snapshot every 50 messages) — a real token/cost-adjacent efficiency feature at the persistence layer, not the prompt layer.

### 2.8 hermes-agent — the maximalist synchronous loop

`agent/conversation_loop.py` (4.6k lines) + `run_agent.py` `AIAgent` (4.6k lines, ~60-parameter constructor — though many methods are now forwarders into the `agent/` package, an ongoing decomposition). Loop guard:

```python
while (api_call_count < agent.max_iterations          # default 90
       and agent.iteration_budget.remaining > 0) or agent._budget_grace_call:
```

— a dual budget (iteration count *and* token/iteration budget with one grace call). Distinctive loop-level machinery: pre-API-call steer drain that injects steering text into the last *tool* message (preserving role alternation instead of appending a second user message); `_drop_trailing_empty_response_scaffolding` / `repair_message_sequence` to fix protocol-invalid tails (`...tool, user, user`) that make providers silently return empty content forever; multi-pass preflight compression (up to 3 passes) before the call. This is hard-won defensive engineering against real provider misbehavior — the loop is verbose precisely because it handles failure modes the minimal frameworks haven't met yet.

### 2.9 naah — thin orchestrator, pipeline-per-turn

`Naah.Core/Agent/AgentLoop.cs` (164 lines): builds a `TurnContext` (history, `IChatClient`, registry, per-turn `Channel<TokenDeltaEvent>`/`Channel<ToolEvent>` for backpressure-capable streaming) and calls a pre-built `AgentMiddlewareDelegate` until `ShouldContinue` is false or `MaxTurns`. All LLM/tool work lives in middleware (Approval, Compaction, Fallback, Logging, Permission, RateLimit, Telemetry). It is yaah's architecture translated to .NET idioms (DI, `Microsoft.Extensions.AI`), currently at "Phase A" — sub-agents, compaction maturity, and OTel parity are explicitly future phases.

### 2.10 shepherd — no traditional loop at all

An agent run is a *retained output*: `workspace.run(task_id, ...)` executes a task (whose Python function signature *is* the permission surface — parameter type grants define what the agent may touch), captures an effects stream as a content-addressed causal DAG, and applies nothing to the filesystem until the user runs `shepherd run select`. The "kernel-v3-reference" package implements the underlying calculus: algebraic effects (`Perform`/`Handle`/`Resume`/`Abort`), a generator-based direct evaluator where *a paused generator is the captured continuation*, a defunctionalized IR, an abstract machine, and trace validators at three schema boundaries — with Lean proofs in flight. This is the only framework here where the agent loop is a research artifact with formal semantics.

---

## 3. Token Efficiency

Mechanisms observed, strongest → weakest per framework:

| Framework | Proactive (no LLM call) | Reactive compaction | Prompt caching | Tool-output bounding |
|---|---|---|---|---|
| **yaah** | **Soft-prune**: stubs stale tool results in the *ephemeral request only* (originals never mutated); thresholds tuned with documented field history (40k/20k → 12k/4k → 3k/500 after real sessions never fired the old ones) | LLM compaction at configurable threshold, pre-call guard, in-`Call` overflow recovery | Anthropic breakpoints (cap 4): system first, then tool msgs at turn boundaries, newest first | Tool-result line/byte caps + disk spill with path hint |
| **kilocode** | **Prune**: `PRUNE_MINIMUM=20k`, `PRUNE_PROTECT=40k` tokens, protected tools (`skill`), tail 2 turns, preserve-recent 2k–8k tokens; safe re-prune at cache-invalidating boundaries (`PruneReason: normal \| post-compaction \| payload-limit`) | Compaction with chunking + payload recovery | Upstream mechanisms | `TOOL_OUTPUT_MAX_CHARS=2000` w/ truncation marker; **swe-pruner**: model passes `context_focus_question` per tool call → small model skims output keeping relevant lines (implements arXiv 2601.16746) |
| **opencode V2** | Tool-output store with managed output paths (externalize bulky results); context epochs bound re-projection | `compactIfNeeded` pre-call + single-shot `compactAfterOverflow` on context-overflow failure | `promptCacheKey` (OpenAI) from session id; Anthropic `cache_control` in the `llm` package schema | Producer capture limits (e.g. Bash `maxOutputBytes` w/ accurate loss reporting) at tools; bounding at registry settlement |
| **crush** | — | Auto-summarize `StopWhen` (context-window aware; separate thresholds for large vs small windows; skipped entirely if window unknown, protecting local models) | `cache_control` on system + last 2 messages + last tool definition | — |
| **deepagents** | **Filesystem offload**: oversized tool results written to the backend FS, replaced by head+tail preview + read instructions (shared `_message_eviction` used by both proactive per-call offload and reactive overflow clipping) | `SummarizationMiddleware` (default trigger: 85% of context), fallback tail-clip on `ContextOverflowError` | `AnthropicPromptCachingMiddleware` (from langchain_anthropic) | Per-tool size thresholds |
| **pi** | Compaction as **pure functions** (I/O in session manager); file-operation carryover across compactions (read/modified file lists survive into the summary context) | LLM summarization w/ dedicated system prompt | Provider-level (pi-ai package) | `truncate` tool + output-accumulator |
| **goose** | `compute_tool_call_cutoff` (tool-result cutoff policy) | Auto-compact at threshold % (configurable) inline in the loop; `large_response_handler` | — | Tool-call cutoff |
| **hermes** | **Preflight compression**: rough token estimate *including tool schemas* ("20–30K+ tokens the old sys+msg estimate missed"), multi-pass (≤3) | Context compressor w/ protect-first/last-N windows; context-engine plugins; manual compression feedback loop | — (session DB persists text-only summaries of multimodal results) | Multimodal→text summary at persistence |
| **naah** | Compaction middleware (port) | planned | planned | — |
| **shepherd / kernels** | N/A — not LLM loops; the kernel's efficiency property is *representation*: content-addressed dedup of trace facts | — | — | — |

**Analysis.** The state of the art in this directory is a **three-tier ladder** (prune cheaply without an LLM → summarize with a small/model call → overflow-recover as last resort), and yaah + kilocode implement it most completely. kilocode's swe-pruner is the most novel single idea (per-call semantic filtering driven by the model's own declared intent); deepagents' filesystem offload with head/tail previews is the most reusable pattern for SDK consumers; hermes' tool-schema-aware token estimation catches a real accounting blind spot every other framework has (most estimate only messages+system); pi's compaction-purity (functions vs. I/O) is the best-factored implementation. opencode's context *epochs* + output *store* externalization is the most ambitious durable-context design but is still being built out (their own checklist marks several continuation conditions unchecked).

---

## 4. Sub-agent Capabilities

| Framework | Dispatch model | Isolation & limits | Notable |
|---|---|---|---|
| **yaah** | **Role registry**: `SubAgentRole` → `RoleProfile` (tools, `MaxLoopCycles`, `MaxToolTurns`, JSON mode, timeout, nesting depth). No default role — every dispatch resolves an explicit role (built-in + filesystem role files) | Curated sub-agent pipeline (no persistence/compaction/spawning); `MaxSubAgentConcurrency`; per-role timeouts | **Background jobs manager** (session-scoped usage attribution never lost even when loop-scoped event hooks are unwired); `supervised_session` + `supervisor` tools; Shepherd trace per sub-agent (parent can inspect child's causal trace on failure); broker `SubAgentStart/End` events |
| **crush** | Coordinator with named agents ("coder", "task"); `runSubAgent` creates a real SQLite *task session* | Session-per-subagent (persistent, inspectable); cost propagated to parent | Sub-agent results are first-class sessions (resumable, browsable) — the nicest persistence story |
| **goose** | `subagent_handler`: recipe-driven subagent tasks | `max_turns` per task; cancellation tokens; `return_last_only` mode | **`final_output_tool` contract** — the subagent must call `final_output` to terminate; the loop warns and continues if it hasn't. Streams notifications back to the parent |
| **opencode** | `task` tool: `subagent_type` + prompt; agent configs marked `mode: "subagent"` (excluded from primary listing) | `deriveSubagentSessionPermission`; optional `task_id` **resume** of a prior subagent session; step limits per agent | Background subagents behind an experimental flag, with strong prompt-side guardrails ("DO NOT sleep, poll…") |
| **kilocode** | Same `task` tool, plus `task-background-process` | **Hard one-level delegation** (recursive chains forbidden); permission ceilings *inherited from parent* and merged over the subagent's own policy (upstream removed inheritance; Kilo deliberately preserves it — documented divergence) | Agent Manager (VS Code extension): multi-session orchestration with **git worktree isolation** per session |
| **pi** | **None built in** — subagents ship as an *extension example* (`examples/extensions/subagent/agents`) | Depends on extension | Architectural statement: the harness core stays loop-pure; teams add delegation policy |
| **deepagents** | **Richest programmatic surface**: sync `SubAgent` specs (name/description/system_prompt/tools/model/middleware) via `task` tool, plus `AsyncSubAgentMiddleware` exposing `start_task`/`check_task`/`update_task`/`cancel_task`/`list_tasks` | Async tasks run as LangGraph async graph executions; state carries live task list | Async subagents are state-machine citizens, not bolt-ons |
| **hermes** | `delegate_task` (2.8k-line tool): single + batch parallel dispatch | `max_spawn_depth`, `max_concurrent_children`, child timeout, MCP-toolset inheritance from parent, sub-agent approval callbacks (auto-deny/auto-approve modes), interrupt propagation | Plus `mixture_of_agents_tool` (fan-out/fan-in) and a **kanban board dispatcher plugin** (multi-agent work queue) |
| **shepherd** | A subagent is just another *retained run*; parent/child linkage is causal edges in the trace DAG | Placement ("jail" containers); nothing applied without selection | Only framework where sub-agent execution is formally auditable via the kernel calculus |
| **naah** | Phase 4 roadmap (not yet built) | — | — |

**Analysis.** Three schools: (1) **role/registry-based** (yaah, crush's named agents, opencode/kilo's agent configs) — declarative, auditable, tool-restricted; (2) **ad-hoc programmatic** (hermes `delegate_task`, deepagents specs) — maximum flexibility, policy lives in code; (3) **infrastructural** (shepherd — delegation is a trace relationship, not a feature). The most production-grade limits are hermes' (depth × concurrency × timeout × approval plumbing all configurable); the cleanest conceptual model is yaah's roles-as-data; the best async story is deepagents' five-tool task state machine; the best observability story is the crush/goose "sub-agent = real session" persistence.

---

## 5. Tool Use

| Framework | Built-in count | Registry pattern | Signature |
|---|---:|---|---|
| **hermes** | **75+ registered** (79 tool files) | Import-time `registry.register()`; auto-discovery via `tools/registry.py`; **toolset system** (`research`, `development`, `safe` composites + distributions) | Largest surface; toolsets are user-facing product |
| **yaah** | ~46 (49 files) | `tools.Registry`; `path_validator`, `conflict_tracker` cross-cutting; go-specific suite (`go_outline`, `go_refactor`, `go_test`, `go_mod`, `bisect`, `staticcheck`) | Deepest language-specific tooling of any framework here |
| **crush** | ~37 | Tool interface + **`.md` description file pairs** (self-documenting tools); `hooked_tool.go` decorator injects PreToolUse hooks; **LSP suite as first-class tools** (definition, references, symbols, rename, replace-symbol, call-hierarchy, diagnostics) + sourcegraph | LSP integration is the standout |
| **opencode V2** | ~14 core built-ins | **Opaque `Tool.make` values**; Location-scoped `ToolRegistry` overlaying process-scoped `ApplicationTools`; permissions attached at invocation-context construction; output bounding only at settlement | Most formally specified tool architecture (its own AGENTS.md defines the one-entry-type rule) |
| **kilocode** | upstream + `repo_clone`, `recall` (LanceDB memory), `lsp`, `suggest`, `apply_patch` | Upstream + mirror rules | Indexing worker (LanceDB) feeds `recall` |
| **goose** | Tools **are MCP extensions** (developer, memory, computer, ai, …) + frontend tools + platform tools | Extension manager spawns MCP servers; `tool_confirmation_router`; malware-check gate for community extensions | Maximal externalization — even core capabilities live out-of-process |
| **pi** | ~12 (read, write, edit, edit-diff, bash, grep, find/glob, ls, truncate) | `Tool.define` w/ zod schema + auto-truncation; `file-mutation-queue` serializes writes | Smallest viable set; quality over breadth |
| **deepagents** | FS tools derived from **`BackendProtocol`** (state, filesystem, sandbox, composite, context_hub, langsmith) | Backend abstraction means the same `read_file` tool works against in-memory state, a sandbox, or a hub | Cleanest hexagonal tool boundary |
| **shepherd** | None — capability = function-signature grants | Tasks declared as Python functions; grants derived from parameter types | Permission model *is* the tool model |

**Approval/permissions** (cross-cutting): yaah pipeline approval+permission middlewares with `classifyGate` + configurable rules; crush `permission` package + hooks (hooks run *before* permission checks); opencode `PermissionV2` with typed sources; kilocode merged ceilings; goose confirmation router + goose *modes* (auto-approve tiers); hermes per-subagent approval callbacks; naah `ApprovalMiddleware` + `IApprovalHandler`; deepagents `HumanInTheLoopMiddleware` with interrupt configs. All twelve treat approval as a first-class pipeline stage — table stakes in 2026.

---

## 6. Overall Architecture

**Process & packaging**

- Single static binary: **yaah** (cross-compile matrix in CI), **crush** (CGO_ENABLED=0 + greenteagc), **shepherd-kernel-go**.
- Runtime-dependent binaries: **goose** (Rust + tokio; Electron desktop + Ink text UI), **naah** (.NET 10; Spectre.Console REPL + SignalR web).
- Node-runtime monorepos: **opencode** (bun, turbo, SST infra, ~30 packages incl. sdk/console/desktop/function/slack), **kilocode** (bun, turbo; VS Code extension packaging), **pi** (npm, lockstep versioning, 5 packages).
- Python: **hermes** (pip/uv installable app + Docker images), **deepagents** (uv monorepo, multiple libs), **shepherd** (uv workspace w/ formal-design docs tree).

**State & persistence**

| Store | Frameworks |
|---|---|
| SQLite (embedded) | yaah (modernc, FTS5 sessions+memory), crush (sqlc codegen), opencode V2 (Drizzle + migrations), hermes (SessionDB FTS5), goose (session manager), shepherd kernels |
| Filesystem JSON | kilocode v1 storage (`~/.local/share/kilo/storage/`, path-array keys) — upstream moved to SQLite; fork keeps JSON |
| LangGraph checkpoints + DeltaChannel | deepagents |
| Content-addressed append-only DAG | shepherd (the entire point of the kernel) |

**Eventing / observability**

- **OTel-first**: yaah (tracing spans per prompt/turn/tool + Shepherd trace middleware + in-memory span buffer), kilocode (kilo-telemetry: PostHog + OTel), crush (PostHog events), goose (tracing crate), hermes (observability plugin), deepagents (LangSmith integration incl. header propagation into subagents).
- **Event-sourced**: opencode V2 (EventV2 sequence numbers, replayable projections, session input inbox), shepherd (trace facts).
- **In-process pub/sub**: yaah typed broker (`PublishMustDeliver` semantics for terminal events), crush pubsub broker, kilocode/opencode v1 `Bus`, pi `EventStream` (push/end result channel — the simplest).

**Extension models**

1. **MCP clients**: everyone (yaah stdio+HTTP+serve-as-server, crush, goose — goose is MCP-native, opencode/kilo, pi, hermes w/ toolset inheritance into subagents, crush even embeds MCP server *instructions* into its system prompt).
2. **Middleware/pipeline**: yaah, naah, hermes (context-engine/memory/model-provider plugin types), opencode plugins.
3. **Skills** (SKILL.md discovery): yaah, crush, kilocode/opencode, hermes, deepagents (`SkillsMiddleware`) — an emerging cross-tool standard.
4. **Hooks** (user shell commands on lifecycle events): crush (Claude-Code-compatible protocol), goose (stop-hook block caps), yaah (HookEvent bus).
5. **Out-of-process everything**: goose (extensions are servers), shepherd (function-signature tasks).

---

## 7. SOLID Design Assessment

Grading engineering *as found in code*, weighted by consequence:

### SRP — Single Responsibility

- **Best: pi.** The loop file is 792 lines *total*; compaction is "pure functions + session-manager I/O"; agent-domain vs LLM-wire message models are separated by design. Nothing in pi's core does two jobs.
- **Excellent: opencode V2.** Self-documenting: "Keep this as orchestration over smaller collaborators rather than rebuilding the legacy `SessionPrompt` monolith." Tool registry, permission, publisher, compaction, and store are separate services with written invariants (one executable tool type; settlement is the only bounding boundary).
- **Excellent: yaah.** Loop/pipeline/middleware/tools/jobs are separate packages; the loop file itself contains only orchestration; every middleware is one file + tests. The AGENTS.md architecture map matches the code.
- **Good: crush** at the *package* level (config/agent/session/message/hooks/permission are clean), weaker at the *file* level — `agent.go` mixes dispatch concurrency, streaming callbacks, caching placement, summarization policy, and persistence in one 8k-line package. `coordinator.go` separately owns 1.4k+ lines of provider wiring.
- **Good: naah** — Phase A intentionally thin; each middleware one concern.
- **Mixed: deepagents** — the *SDK* is well-layered (graph assembly vs middleware vs backends protocol), but it inherits LangChain's boundary blur: middleware can mutate model requests, state, and tools simultaneously.
- **Weakest: goose and hermes.** `agent.rs` (4.4k lines) and `conversation_loop.py` (4.6k lines) each hold loop + approval + retry + compaction + truncation + repair + metrics. Both show active decomposition efforts (hermes' forwarder methods into `agent/*` modules; goose' `execute_commands`, `reply_parts`, `retry` modules), but the god-file remains the load-bearing wall in both.

### OCP — Open/Closed

- **Middleware pipelines are the OCP win** wherever they exist: yaah (16 middlewares, config-driven enable/disable by name), naah, hermes context-engine plugins, deepagents AgentMiddleware stack. New behavior (e.g. yaah's `conflictdetect`) ships as a new middleware without touching the loop.
- **goose's MCP-everything** is OCP via process boundaries — adding capabilities never touches core, at the cost of IPC latency and a 3.3k-line extension manager.
- **crush's hooks + skills** extend behavior without code changes; but loop policy changes (summarization thresholds, cache placement) still require editing `agent.go`.
- **kilocode's fork model is an anti-OCP pressure**: upstream evolution regularly conflicts with Kilo divergence; the team compensates with CI-enforced mirror-file discipline (`check-opencode-annotations`, `check-kilocode-change`) — process substituting for architecture.

### LSP — Liskov Substitution

Most honestly assessed via substitutability of the core abstractions:

- **yaah `Middleware`** and **pi `StreamFn`/`AgentTool`**: small interfaces where every implementation observed obeys the contract (yaah even synthesizes tool-result messages for *removed* tool calls so provider invariants hold — middleware can't break the `tool_call_id` pairing rule because `SynthesizedResults` routes around them; the field's doc comment explains exactly why).
- **crush `fantasy.Agent`**: the loop contract is externalized; crush passes `PrepareStep`/streaming callbacks that must satisfy library expectations (e.g., returning created assistant messages) — substitutable but the contract lives outside the repo, which is a reviewability cost.
- **deepagents `BackendProtocol`**: genuine behavioral substitution (state vs sandbox vs composite filesystems behind identical tools).
- **The Shepherd kernel trio is LSP at the extreme**: three languages, one ABI, byte-identical digests — substitutability *provable* by golden vectors.

### ISP — Interface Segregation

- **Go idiom enforced**: crush's AGENTS.md mandates consumer-defined small interfaces; yaah's `Middleware` (3 methods), `Compactor`, `TurnCheckpointer` are narrow by construction.
- **opencode's service-per-concern** (`SessionStore`, `SessionRunnerModel`, `SystemContextRegistry`, `SkillGuidance`, `ToolRegistry`, `Snapshot`…) is fine-grained DI, though the *count* of services a feature touches is high.
- **hermes `AIAgent`** exposes the union of ~60 constructor params and hundreds of attributes — consumers see everything; ISP is the SOLID principle it most violates.
- **pi's `AgentLoopConfig`** is a config-bag (not an interface hierarchy) — pragmatic; consumers implement only the hooks they need (`getSteeringMessages?`, `prepareNextTurn?`, `shouldStopAfterTurn?` all optional). Effectively ISP via optional functions.

### DIP — Dependency Inversion

- **Strongest: opencode** — Effect `Layer`s *are* a DI graph with compile-time composition and scoped lifetimes (`InstanceState` per directory; `makeRuntime` memoization). The most industrial DI discipline in the directory.
- **naah** uses `Microsoft.Extensions.DependencyInjection` — idiomatic, boring, effective (explicitly listed as a key decision: "eliminates Go's manual wiring").
- **yaah** inverts through a hand-written composition root (`cmd/yaah/wiring*.go`) — explicit, greppable, no framework; the `Loop` depends on `Compactor`/`TurnCheckpointer` interfaces it defines.
- **deepagents**: `resolve_model` + `BackendFactory` + middleware injection — dependency inversion at SDK seams.
- **goose/hermes** lean on concrete managers (`SessionManager::instance()`, `PermissionManager::instance()` — global singletons) — the least inverted designs here.
- **pi** threads dependencies explicitly through constructors; the `agent` package depends only on `pi-ai/compat` types.

### Verdict table

| | SRP | OCP | LSP | ISP | DIP | Overall feel |
|---|---|---|---|---|---|---|
| pi | ●●● | ●● | ●●● | ●●● | ●● | Small, honest, readable |
| opencode V2 | ●●● | ●●● | ●● | ●● | ●●● | Industrial; two generations coexist |
| yaah | ●●● | ●●● | ●●● | ●●● | ●● | Best middleware factoring |
| naah | ●●● | ●●● | ●● | ●● | ●●● | Promising port, early |
| crush | ●● | ●● | ●● | ●●● | ●● | Concurrency-mature, file-heavy |
| deepagents | ●● | ●●● | ●●● | ●● | ●● | Elegant SDK on a blurry platform |
| kilocode | ●● | ● | ●● | ●● | ●● | Product velocity via fork discipline |
| goose | ● | ●●● | ●● | ●● | ● | Extension-native monolith |
| hermes | ● | ●● | ●● | ● | ● | Battle-tested maximalism, refactoring |

---

## 8. Head-to-Head Summary

| Dimension | Winner (code-justified) | Runner-up |
|---|---|---|
| **Agent loop design** | **yaah** (checkpoint/restore, overflow-adoption, curated sub-pipeline) | pi (cleanest minimal), opencode V2 (durable semantics) |
| **Loop simplicity/readability** | **pi** | naah |
| **Token efficiency breadth** | **kilocode** (prune+chunk+recovery+swe-pruner) | yaah (3-tier ladder), deepagents (FS offload) |
| **Token efficiency novelty** | **kilocode swe-pruner** (per-call semantic filtering) | hermes (tool-schema-aware estimation) |
| **Sub-agent machinery** | **hermes** (depth/concurrency/timeouts/approvals) | deepagents (async task state machine), yaah (roles-as-data) |
| **Sub-agent auditability** | **shepherd** (causal trace DAG) | crush (sub-agent = persistent session) |
| **Tool breadth** | **hermes** (75+ tools, toolsets) | crush (LSP suite) |
| **Tool architecture rigor** | **opencode V2** (opaque tools, settlement boundary) | deepagents (BackendProtocol) |
| **Extensibility** | **goose** (MCP-everything) | yaah/opencode middleware+plugins |
| **Persistence/durability** | **opencode V2** (event-sourced, durable admission) | crush (SQLite sessions) |
| **Observability** | **yaah** (OTel spans + Shepherd facts) | opencode (EventV2 replay), kilocode telemetry |
| **Determinism/formality** | **shepherd kernels** (byte-identical tri-language ABI; formal semantics) | — |
| **SOLID overall** | **yaah / pi / opencode V2** (different weights) | naah |
| **Testability culture** | **hermes** (~17k tests) | crush (golden-file TUI testing via catwalk), pi (faux-provider harness, no paid-token tests) |

---

## 9. Per-Framework One-Paragraph Verdicts

**yaah** — The best factored loop-and-pipeline in the directory, with the most defensible failure semantics (turn checkpoints with bounded restore, overflow-adoption, synthesized-denial tool results that preserve provider invariants). Its middleware system is what crush's `agent.go` would be if it were decomposed, and its role-based sub-agent registry is cleaner than config-file agent definitions. Weakest area: single-maintainer velocity — the Go tool suite is deep but the surface (web UI, TUI, ACP, MCP server) is very broad for its size.

**naah** — A faithful architectural translation of yaah into .NET idioms (DI, `IChatClient`, Channels for streaming backpressure). Currently a skeleton-plus-core; the plan's honesty (explicit phases, "no TUI port" decision) is a model for port projects. Judge it in a year.

**crush** — The most concurrency-mature Go harness (accepted runs, cancel sequencing, queue draining, RunComplete guarantees for every admitted call) with the best LSP tooling and pragmatic library delegation (`fantasy`) — at the price of loop policy living in an 8k-line file and the actual iteration logic being outside the repo. Auto-summarization is context-window-aware and local-model-safe (skips when window unknown).

**goose** — The extension-native framework: everything is an MCP server, which buys unmatched openness (desktop app, CLI, recipes, subagents, community extensions with malware screening) and pays in a 4.4k-line orchestration monolith with global singletons. Loop features are production scar tissue: stop-hook block caps, approval routing, large-response handling, retry manager. Choose it for ecosystem, not for code aesthetics.

**opencode** — The most ambitious architecture in flight: durable event-sourced sessions, context epochs, opaque tool settlement, Effect-TS DI throughout, and a self-documenting migration checklist. Also the least settled: V1 and V2 loops coexist, and several V2 continuation paths are explicit TODOs. If the migration lands, this becomes the reference durable-agent design; today it's a construction site with excellent blueprints.

**kilocode** — Proof that a disciplined fork can out-product its upstream: one-level delegation, parent-inherited permission ceilings, aggressive context pruning, swe-pruner, LanceDB recall, and Agent Manager worktree orchestration — while CI bots (annotation checks, promise-facade ratchets, workflow allowlists) hold the fork line against upstream drift. Architecturally conservative (Effect ratchet forbids new Promise facades), productively aggressive.

**pi** — The minimalist thesis executed: a 792-line loop with the two best loop-level safety details in the directory (truncated-tool-call refusal, steering-before-next-turn), pure-function compaction, and a clean AgentMessage/LLM-message boundary. No built-in subagents is a feature if you agree with it. The code you'd hand to someone learning what an agent loop fundamentally is.

**deepagents** — Not a harness — the middleware SDK for building harnesses on LangChain/LangGraph. Delivers the richest *programmatic* sub-agent surface (sync + async state-machine), filesystem-offload token strategy, and a genuinely clever persistence optimization (DeltaChannel O(N²)→O(N) checkpoints). Value is inversely proportional to how much you already dislike LangChain's boundaries.

**hermes-agent** — The maximalist: 75+ tools, toolsets, dual-budget loop, multi-pass preflight compression, provider-tailbreaking message repair, the deepest sub-agent limit system, 15+ gateway platforms, plugins, cron, ~17k tests. The SOLID violations are real (god-objects, 60-param constructors) but so is the active decomposition (forwarder modules) and the hardening — many loop defenses here exist nowhere else in the directory because nobody else has hit those bugs yet.

**shepherd + kernels** — A different species: agent execution as a content-addressed, formally specified trace calculus with retained outputs and signature-derived permissions. Nothing touches your files until you select a run. The tri-language kernel with byte-identical digests is the most rigorous engineering artifact in the directory. Not a competitor to the harnesses — the substrate that could one day sit under them (yaah already ships a Shepherd trace middleware; the ideas are already flowing).

---

## 10. Cross-Pollination Map (what each should steal)

- **Everyone ← pi**: fail tool calls when `stopReason === "length"`; keep agent-domain message models separate from wire formats.
- **Everyone ← hermes**: include tool schemas in token estimates; repair protocol-invalid tails before they become infinite empty-response loops.
- **Everyone ← kilocode**: swe-pruner's per-call focus question; prune at cache-invalidating boundaries only.
- **Everyone ← opencode**: durable prompt admission (inbox rows) so crashes can't lose an admitted user turn.
- **Everyone ← crush**: every admitted run gets exactly one terminal event, even when canceled before starting or dropped from a queue.
- **Everyone ← goose**: stop-hook block caps (bounded override of infinitely-blocking lifecycle hooks).
- **Everyone ← yaah**: turn-level checkpoint/restore with bounded retries; synthesized tool results for denied/dropped calls.
- **Everyone ← shepherd**: make delegation a trace relationship, not a feature — causal edges between parent and child runs.
- **goose/hermes ← the field**: decompose the god-file; the middleware pattern demonstrated by yaah/naah is the proven path.

---

*Method note: findings reference specific files (`yaah/internal/agent/loop.go`, `crush/internal/agent/agent.go`, `opencode/packages/core/src/session/runner/llm.ts`, `pi/packages/agent/src/agent-loop.ts`, `deepagents/libs/deepagents/deepagents/graph.py`, `hermes-agent/agent/conversation_loop.py`, `goose/crates/goose/src/agents/agent.rs`, `naah/src/Naah.Core/Agent/AgentLoop.cs`, and the shepherd packages) so every claim above is re-verifiable at the cited location. LOC excludes vendored/generated/build directories; the "Sum" line pygount emits for `go.sum` files was excluded from language totals.*
