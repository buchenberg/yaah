# Agent Loop Comparison

Analysis of how the agent loop works in yaah vs. other agentic frameworks
(crush, goose, opencode, hermes-agent, kilocode, deepagents). Focused on
patterns that translate well to Go and would strengthen yaah.

See `docs/middleware-pipeline-plan.md` for the implementation plan that
covers *how* to structure these improvements via a composable middleware
pipeline. This doc describes *what* to build; that doc describes the
architecture.

## 1. Loop Structure

### yaah — Single linear `for` loop with channel steering

`internal/agent/agent.go:123-224`

```go
func (l *Loop) Run(ctx context.Context, userInput string) (string, error) {
    for iter := 0; iter < l.MaxIterations; iter++ {
        drainSteer()
        drainFollowups()
        msg := callModel(req)
        if no tool calls { return msg.Content }
        executeToolsParallel(toolCalls, &messages)
        compactContext()
    }
}
```

Strengths: clean, simple, idiomatic Go channels for steering/follow-ups.
Weaknesses: single monolithic method; no task-queue abstraction separating
admission from execution; tool parallelism is unbounded.

### crush — Provider owns the loop via callback-based steps

`internal/agent/agent.go:557-1053`

Crush delegates the `while` loop to `fantasy.Agent.Stream()`, a
provider-agnostic abstraction. The application provides callbacks:

- `PrepareStep` — runs before each model call: drains queued follow-ups,
  creates a new assistant DB row, applies cache-control breakpoints, returns
  tools and messages for the next model call.
- `StopWhen` — conditions for context overflow (summarization) and loop
  detection (repeated tool calls).
- `OnToolCall`, `OnToolResult`, `OnTextDelta`, `OnReasoningDelta`, etc.

This callback-based step model is elegant: the agent doesn't own the
`while` loop; the provider abstraction does. Callbacks are invoked at
well-defined points.

**Takeaway for yaah**: The `PrepareStep` pattern (single callback that
returns messages + tools for the next iteration) is cleaner than inlining
everything in `Run`. It makes the loop testable and composable.

### goose — Explicit `loop { }` with manual control

`crates/goose/src/agents/agent.rs:1951`

```rust
loop {
    if is_token_cancelled(&cancel_token) { break; }
    drain_pending_steers();
    check_compaction();
    call_model();
    execute_tools_sequentially();
    check_stop_hooks();
    append_results();
    turns_taken += 1;
}
```

Goose owns the loop explicitly. Key additions beyond basic iteration:
`turns_taken` counter (distinct from iterations), stop hooks that can block
exit, auto-compaction detection before each turn, per-tool permission
routing.

**Takeaway for yaah**: Separate `turns_taken` from `iterations`. A turn
should mean "one user → one model → one tool batch → next model."
Stop hooks (user-defined scripts that gate exit) are a clean extension point.

### hermes-agent — Single `while` with dense retry/fallback/vendor logic

`agent/conversation_loop.py:775`

```python
while (api_call_count < max_iterations and budget.remaining > 0) or budget_grace_call:
    drain_steer()
    inject_memory_prefetch()
    build_api_messages()
    apply_prompt_caching()
    sanitize_surrogates()
    for retry in range(max_retries):
        response = streaming_api_call()
        if rate_limited: try_fallback()
        if context_overflow: compress_and_retry()
        if invalid: classify_and_retry()
    dispatch_tools_sequentially()
    check_guardrails()
    check_context_compression()
    persist_session()
```

The loop body is ~3,000 lines. It handles 20+ classified error types,
provider fallback, preflight compression, tool schema sanitization, prompt
caching, per-turn guardrails, and post-turn hooks. The sheer density comes
from years of production hardening.

**Takeaway for yaah**: Hermes's error classification (context-limit →
compact, rate-limit → backoff + fallback, auth → re-auth) is the gold
standard. The budget grace call (one extra iteration after exhaustion to
produce a final summary) is a small feature with outsized UX impact.

### opencode — V2 session runner with `while(true)` and task queue

`packages/opencode/src/session/prompt.ts:1081-1088`

```typescript
while (true) {
    filterCompactedMessages()
    checkFinishState()
    dequeueSubtaskOrCompaction()
    checkOverflow()
    resolveTools()
    callModel()
    processToolCalls()
    continue
}
```

Opencode separates session admission (V2 `prompt()`) from execution
(V2 `SessionRunner`). Admitting a prompt enqueues a durable
`session_input` row; the runner drains it at safe boundaries. The loop
also dequeues tasks (subtasks, compactions) from a durable queue — these
are not inlined in the loop body.

**Takeaway for yaah**: Separate admission from execution. The loop
should pull work from a durable queue, not just react to a single `Run()`
call. This enables session resume, queued follow-ups, and scheduled
compaction.

---

## 2. Tool Execution

| Framework | Parallelism | Concurrency cap | Ordering |
|---|---|---|---|
| yaah | Parallel (goroutines) | None | FIFO via channel |
| crush | Provider-side | Not exposed | Provider-controlled |
| goose | Sequential | N/A | Call order |
| hermes-agent | Sequential | N/A | Call order |
| opencode | Parallel | Not capped | Re-ordered before next call |

yaah is the only framework other than opencode that executes tools in
parallel. The absence of a concurrency cap is a risk — 12 parallel bash
calls can saturate CPU or hit API rate limits.

**Recommendation**: Add a `ToolConcurrency` config (default: 5). Use
`golang.org/x/sync/semaphore` to cap goroutines. Consider executing
write tools after reads to prevent the model from reading stale state.

---

## 3. Context Management

| Framework | Token counting | Trigger | Method |
|---|---|---|---|
| yaah | char/4 | 80% of ContextWindow | LLM summary → fallback trim |
| crush | Provider-reported tokens | 20K buffer (large) or 20% (small) | LLM summary (Go template) |
| goose | Token counter | 80% threshold | LLM summary + tool-pair summarization |
| hermes-agent | `estimate_request_tokens_rough` | Compressor threshold | Multi-pass middle-turn summarization |
| opencode | Tiktoken (inferred) | After each finish | Separate small-model compaction task |

yaah's char/4 estimation is the crudest. Goose and hermes both use proper
token counting (tiktoken or provider API). Goose additionally supports
`tool_pair_summarization` — collapsing sequential tool call/result pairs
into summaries mid-turn, saving context without needing full compaction.

Hermes does **preflight compression** before entering the loop: if the
loaded conversation history already exceeds the threshold (user switched
to a smaller-context model), it compresses proactively rather than
waiting for an API error.

**Recommendation**:
1. Switch to proper token counting (`tiktoken-go` or similar).
2. Add preflight compression (hermes pattern).
3. Add tool-pair summarization for long code-gen sessions (goose pattern).
4. Keep the separate `CompactProvider`/`CompactModel` pattern — it's correct.

---

## 4. Loop Safety

| Framework | Detection method | Trigger |
|---|---|---|
| yaah | SHA-256 of name + result | 5 repeats in 10 steps |
| crush | SHA-256 of name + input + output | >5 in 10 steps |
| goose | RepetitionInspector + AdversaryInspector | Multi-layered |
| hermes-agent | Per-tool invocation counters in guardrails | Configurable |
| opencode | `doom_loop` permission gate | Ask/deny on patterns |

yaah and crush use nearly identical hash-based loop detection. Goose adds
adversarial pattern detection and egress filtering. Hermes tracks per-tool
invocation counts within a turn.

**Recommendation**: yaah's hash-based detection is solid. Add:
- **Per-tool invocation cap** (hermes guardrails). A tool called 30+ times
  in one turn is a smell even if args differ.
- **Adversarial output filtering** (goose). Scan tool results for escape
  patterns.

---

## 5. Approval / Permissions

| Framework | Model | Highlights |
|---|---|---|
| yaah | allow/ask/deny, hardcoded dangerous-tools set | stdin prompt |
| crush | Permission service with path rules | `*.md` vs `*.go`, hooks before checks |
| goose | PermissionManager + ToolConfirmationRouter | Session allowlists, elicitation dialogs |
| hermes-agent | Auto-approve for subagents | Guardrails can halt turns |
| opencode | Recursive allow/ask/deny per tool + path glob | Per-agent rulesets, external_directory scoping |

yaah's hardcoded dangerous-tools set is the biggest single gap vs. every
other framework. All of them have path-aware permission rules.

**Recommendation**: Implement path-pattern permissions modeled on opencode:
- `allow`/`ask`/`deny` per tool name.
- Glob patterns on file paths (`"*.go": "allow"`, `"*.env": "ask"`).
- `external_directory: { "*": "ask" }` for filesystem scoping.
- Per-agent/subagent rulesets (task subagents get no bash/write by default).
- Keep it simple — start with glob rules, add complexity only when needed.

---

## 6. Sub-agents

| Framework | Sub-agent model |
|---|---|
| yaah | `task` tool, goroutine, restricted tools, returns string |
| crush | Agent profiles (Coder/Task), no sub-loops |
| goose | `subagent_execution_tool` with task_config |
| hermes-agent | `delegate_task` with roles, depth, timeouts, parallel batch |
| opencode | `task` tool with per-agent permissions and steps caps |

Hermes has the most mature sub-agent system:

- **Roles**: leaf (no task/memory tools) vs orchestrator (can spawn further).
- **Spawn depth cap**: prevents infinite recursion (default 2).
- **Child timeout**: subagent must return in N seconds.
- **Parallel batch**: submit multiple `{ goal, context, toolsets }` at once.
- **Concurrency cap**: max concurrent children (default 3).
- **Separate iteration budget**: children don't consume parent's budget.
- **Interrupt propagation**: parent cancel → children cancelled.

**Recommendation**: Model yaah's sub-agent system on hermes:
1. Roles (leaf vs orchestrator).
2. Spawn depth cap.
3. Per-subagent timeout.
4. Parallel batch submission with concurrency cap.
5. Separate iteration budget per subagent.

---

## 7. Steering / Interruption

| Framework | Mechanism |
|---|---|
| yaah | `Steer` + `FollowUps` Go channels, drained at iteration top |
| crush | `Cancel(sessionID)` with accept-sequence tracking |
| goose | `pending_steers: Mutex<HashMap<VecDeque<Message>>>` + `CancellationToken` |
| hermes-agent | `_interrupt_requested` boolean, thread-scoped, steer injected into tool result |
| opencode | `queue`/`steer` durable inputs, process-local ownership chain |

yaah's channel-based steering is idiomatic and clean. Hermes's pattern of
injecting steer into the last tool result (rather than as a separate user
message) preserves role alternation better.

**Recommendation**: Keep the channel pattern. Add:
- Cancel-token-style context propagation so subagent tool calls are
  cancelled when the parent context is cancelled.
- Steer injection into last tool result rather than as `[STEER]` user message
  (hermes pattern).

---

## 8. Error Handling / Retry

| Framework | Approach |
|---|---|
| yaah | Exponential backoff, MaxRetries, retries any provider error |
| crush | Provider-side retry, OnAuthRefresh, OnRetry resets content |
| goose | RetryManager with attempt tracking, retryable/non-retryable classification |
| hermes-agent | 20+ classified errors, fallback routing, jittered backoff, grace call |
| opencode | Effect-based, exact retry (same session + prompt), SessionRevert |

Hermes's error classification is the most sophisticated:
- Context-limit errors → compress and retry (up to 3 passes).
- Rate limits (429) → jittered backoff.
- Billing/entitlement → user guidance.
- Empty responses → retry with tool-call stripping.
- Invalid JSON tool calls → repair and retry.
- Truncated tool calls → continuation prompt injection.
- Provider fallback → activate fallback model.
- Grace call after budget exhaustion.

**Recommendation**:
1. Classify errors: retryable (429, 5xx, timeout), non-retryable (4xx, auth),
   context-length (need compaction).
2. On context-limit errors, auto-compact instead of failing.
3. Add a budget grace call (one extra iteration for final summary).
4. Add fallback model support (try small model on primary failure).

---

## 9. Prompt Caching

| Framework | Support |
|---|---|
| yaah | None |
| crush | Anthropic cache-control on system + last 2 messages per step |
| hermes-agent | Full Anthropic caching, byte-stable system prompt, ephemeral context in user msg |
| goose | Provider-native caching |
| opencode | Provider-level (AI SDK) |

yaah is the only framework without prompt caching.

Hermes's approach is the most thorough: the system prompt is cached
across turns (stored in session DB to preserve byte-identical prefix),
and all dynamic context is injected into the user message (not the system
prompt) to avoid cache invalidation.

**Recommendation**: High impact, low effort.
1. Anthropic cache-control breakpoints on system prompt + last 1–2 messages.
2. Never mutate the system prompt mid-session. Inject dynamic context into
   user/tool messages instead.

---

## 10. Session Persistence

| Framework | Model |
|---|---|
| yaah | `Loop.Messages` in-memory, no session DB integration in loop |
| crush | Full SQLite, messages per session, resume across restarts |
| goose | SessionManager with conversation storage |
| hermes-agent | Full SQLite, messages + system prompt + usage + todos persisted |
| opencode | V2 durable session_input rows, full message history in SQLite |

yaah already has SQLite + FTS5 for memory. The agent loop doesn't use it
for message persistence.

**Recommendation**:
1. Persist messages to SQLite after each turn (model call + tool results).
2. Support session resume: load messages from DB into `Loop.Messages` on init.
3. `yaah session list`/`yaah session resume` already exists — wire it in.

---

## 11. Task/Todo Tracking

| Framework | Model |
|---|---|
| yaah | `todowrite` tool, in-memory store |
| hermes-agent | `todo_tool.py`, persisted in conversation, hydrated from history |
| opencode | `todowrite` tool |
| deepagents | Todo as markdown file, model MUST update every step |

Deepagents' pattern of enforcing todo updates via system prompt is
notable — the model is *required* to update its plan file before marking
work complete. This discipline reduces the "model wanders off" problem.

**Recommendation**:
1. Persist todos to SQLite alongside messages.
2. Hydrate todo state from conversation history on resume (hermes pattern).
3. Inject a system-prompt reminder that the model should update its todo
   list before claiming completion (deepagents pattern).

---

## Priority Recommendations

Ordered by estimated impact/effort ratio:

| # | Change | Impact | Effort | Model after |
|---|---|---|---|---|
| 1 | Path-pattern permissions | High | Moderate | opencode |
| 2 | Prompt caching | High | Low | hermes-agent |
| 3 | Context-limit → auto-compact | High | Low | hermes-agent |
| 4 | Proper token counting | Moderate | Moderate | goose |
| 5 | Sub-agent lifecycle (roles, depth, timeout, batch) | High | High | hermes-agent |
| 6 | Session persistence in agent loop | High | High | crush |
| 7 | Error classification + fallback model | Moderate | Moderate | hermes-agent |
| 8 | Tool concurrency cap | Low | Low | — |
| 9 | Tool-pair summarization | Moderate | Moderate | goose |
| 10 | Preflight compaction | Low | Low | hermes-agent |
