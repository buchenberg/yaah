---
goal: Make yaah's context compaction genuinely best-of-breed by incorporating proven patterns from opencode/Kilo Code and filling the gaps
version: 1.0
date_created: 2026-07-26
owner: yaah
status: Planned
tags: optimization, compaction, context, efficiency
---

# yaah Compaction Optimization Plan

## Context: Three-Generation Comparison

| Feature | opencode V2 | Kilo Code V1 | yaah |
|---------|-------------|--------------|------|
| **Core algorithm** | summarization + verbatim tail | same | same (ported from Kilo Code) |
| **Cache-aware threshold** | no | no | **yes** |
| **Dual threshold (effective + raw)** | no | no | **yes** |
| **Self-resetting cooldown** | no | no | **yes** |
| **Budget scaling (chars/4 vs tokenizer gap)** | no | no | no (removed as unnecessary: both budget and estimate use chars/4, same as competitors) |
| **Reasoning content protection** | no | no | **yes** |
| **Tool-call atomicity in split** | no | no | **yes** |
| **Chunked compaction fallback** | `compactInChunks()` | `KiloCompactionChunks` | **none** (blunt `trimContext()`) |
| **Payload recovery (413 errors)** | no | `KiloCompactionPayloadRecovery` | **none** |
| **Post-flight overflow recovery** | `recoverOverflow()` | `isOverflow()` + replay | **none** |
| **Soft prune policy** | no explicit Pruner | permanent erasure (post-compaction) | **ephemeral stubbing** (runs every PostTool) |
| **Soft prune protect** | none | 40K token budget | 2K tokens + 1 turn + tool names |
| **Compaction events** | `Started/Delta/Ended` | `SessionCompactionEvent` | **none** (blocking, no UI feedback) |
| **Chunked summarization concurrency** | no | concurrency=3 | **serial only** |
| **Provider overflow pattern detection** | `isContextOverflowFailure()` | `OVERFLOW_PATTERNS` | **none** (pre-flight only) |
| **OTel tracing** | none | none | **yes** (spans + hooks) |
| **System context epoch** | `context-epoch.ts` | none | **N/A** (no composable system context) |

## Gap Analysis: 7 Optimizations

### OPT-1: Chunked compaction fallback (replaces `trimContext()`)

**Problem**: When the compaction request itself exceeds the compact model's context window, yaah calls `trimContext()` — a simple oldest-message removal. This loses information without any LLM involvement.

**Design**: Port opencode's `KiloCompactionChunks` pattern. If the compaction request overflows, split old messages into N chunks, summarize each independently (goroutine pool, concurrency=3), then recursively merge.

```
compactContext() → estimate exceeds CompactModel window →
  split oldMsgs into chunks (each ≤ 60% of compact model window) →
  par-for-each chunk: summarize with compact model → collect partial summaries →
  if count(partial) > 1: recursive reduce → final summary
  else: use single partial summary as final
```

**Relevant code**: Kilo Code at `kilocode/packages/opencode/src/kilocode/session/compaction-chunks.ts` (412 lines). The split, concurrency, and reduce logic are the valuable parts.

**Constants to port**:
- Chunk budget: 60% of usable context
- Minimum chunk: 1000 tokens
- Max reduce depth: 3
- Concurrency: 3

**Implementation**: New file `agent_chunked_compact.go` in `internal/agent/`.

| Task | File | Lines | Risk |
|------|------|-------|------|
| `chunkSplit()` — greedy accumulation up to budget | `internal/agent/agent_chunked.go` | ~40 | Low |
| `chunkReduce()` — recursive merge of partial summaries | `internal/agent/agent_chunked.go` | ~50 | Low |
| `chunkedCompact()` — orchestrator: split → par-summarize → reduce | `internal/agent/agent_chunked.go` | ~50 | Medium (goroutine sync) |
| Wire into `compactContext()` fallback path | `internal/agent/agent_context.go` | ~5 | Low |
| Tests: chunk boundary, reduce depth, concurrency correctness | `internal/agent/agent_chunked_test.go` | ~80 | Medium |

---

### OPT-2: Provider overflow recovery + retry

**Problem**: yaah's pre-flight guards can be wrong (chars/4 undercounts code/JSON). When a provider rejects the request with a 413/context-too-large error, yaah just returns an error to the user.

**Design**: Add a post-flight recovery handler that wraps the LLM provider call. If the provider returns a context-overflow error:
1. Detect via pattern matching on error text (port `OVERFLOW_PATTERNS` from opencode and Kilo Code — covers 15+ providers)
2. If compaction already failed (compaction produced no savings), fall through to chunked compaction (OPT-1)
3. Force compaction at aggressive threshold (0.3)
4. If the provider error matches "payload too large" pattern: strip tool outputs from input, retry the request

**Relevant code**: 
- opencode `packages/llm/src/provider-error.ts` — regex patterns
- opencode `packages/opencode/src/kilocode/session/compaction-payload-recovery.ts` — strip + retry logic

**Implementation**: Extend `llm/client.go` `Call()` to accept a `CompactFunc` callback for overflow retry:

```go
type CallOptions struct {
    CompactFunc func(context.Context) error  // retry after compaction
}

func Call(ctx context.Context, provider Provider, req ChatRequest, opts CallOptions) (ChatResponse, bool, Usage, error) {
    resp, err := provider.Send(ctx, req)
    if err != nil && matchesOverflowPattern(err) && opts.CompactFunc != nil {
        compactErr := opts.CompactFunc(ctx)
        if compactErr != nil {
            return ChatResponse{}, false, Usage{}, err  // return original error
        }
        return provider.Send(ctx, req)  // retry with compacted context
    }
    return resp, err
}
```

| Task | File | Lines | Risk |
|------|------|-------|------|
| `matchesOverflowPattern()` — regex match across providers | `internal/agent/llm/overflow.go` | ~30 | Low |
| `matchesPayloadTooLarge()` — detect 413 | `internal/agent/llm/overflow.go` | ~10 | Low |
| Add `CompactFunc` to client call options | `internal/agent/llm/client.go` | ~15 | Low |
| Wire overflow retry in `Loop.run()`, after context guard | `internal/agent/agent.go` | ~25 | Medium (error-flow change) |
| Wire payload-recovery retry in `Loop.run()` | `internal/agent/agent.go` | ~20 | Medium |
| Tests: overflow patterns, retry flow | `internal/agent/agent_test.go` | ~60 | Low |

---

### OPT-3: Incremental compaction events (async UI feedback)

**Problem**: Compaction blocks the TUI for 2-10 seconds with no progress indication. The user sees a frozen screen and the compacted result appears all at once.

**Design**: Publish compaction events through the broker so the UI can show progress:
- `CompactionStarted` — sent when compaction begins (with estimated tokens before)
- `CompactionChunkDone` — sent per completed chunk (for chunked mode)
- `CompactionDone` — sent when compaction completes (with final token count)

**Relevant code**: opencode V2 `Compaction.Started/Delta/Ended` events.

```go
type CompactionStartedEvent struct {
    BeforeTokens int
    TargetTokens int
}
type CompactionDoneEvent struct {
    BeforeTokens int
    AfterTokens  int
    SavingsPct   float64
}
```

Then the TUI can show `Compacting — [████░░░░] 60%` in the status bar.

| Task | File | Lines | Risk |
|------|------|-------|------|
| Add compaction event types to sealed `Event` interface | `internal/agent/events.go` | ~20 | Low |
| Publish events from `compactContext()` via broker | `internal/agent/agent_context.go` | ~15 | Low |
| Handle events in TUI `Model.HandleEvent()` | `internal/tui/tui.go` | ~15 | Low |
| Handle events in REPL `terminalView` | `cmd/yaah/agent_frame.go` | ~10 | Low |
| Tests: event emission during compaction | `internal/agent/events_test.go` | ~30 | Low |

---

### OPT-4: Persistent compaction cooldown across sessions

**Problem**: yaah's compaction cooldown (`ineffectiveCompactions` counter) is per-`Loop` instance. If the user restarts yaah, the counter resets to 0 and compaction retries on a conversation that has already proven resistant to compaction.

**Design**: The SQLite DB already has `GetCompactionCooldown`/`SetCompactionCooldown` (lines 601-606 of agent_context.go). But this is only checked at the start of `compactContext()`; the in-memory `l.ineffectiveCompactions` field diverges from the DB value. Fix: load cooldown from DB on every `compactContext()` call, not just the first.

**Current code** (agent_context.go:400-408):
```go
if l.SessionID != "" && l.DB != nil {
    cooldown, ineffective, err := l.DB.GetCompactionCooldown(l.SessionID)
    if err == nil && cooldown > 0 && time.Now().Unix() < cooldown {
        return
    }
    if err == nil && ineffective > l.ineffectiveCompactions {
        l.ineffectiveCompactions = ineffective
    }
}
```
The field `l.ineffectiveCompactions` can be stale (higher in DB than in memory). The existing code only syncs if `ineffective > l.ineffectiveCompactions` — it should **always** sync. Also, the cooldown expiry (lines 387-398) resets `l.ineffectiveCompactions` but does not sync to DB.

| Task | File | Lines | Risk |
|------|------|-------|------|
| Inline loading from DB: always sync, not just when DB is higher | `internal/agent/agent_context.go` | ~5 | Very low |

---

### OPT-5: Adaptive budget scaling with feedback loop

**Problem**: The preserve budget is fixed at 25% of context window (clamped 2000-8000). If compactions consistently save >40%, a tighter budget would reclaim more. If they save <10%, a looser budget would improve accuracy.

**Design**: After each successful compaction, compare the actual percentage saved to target. Apply a gradient:
- If savings > 40% for 3 consecutive compactions → tighten budget by 10% (min 2000)
- If savings < 10% for 2 consecutive compactions → loosen budget by 20% (max 8000)

The budget scales proportionally less aggressively than the cooldown latch (which is a hard on/off for thrashing). This is a soft feedback loop.

| Task | File | Lines | Risk |
|------|------|-------|------|
| Track running savings window on `Loop` | `internal/agent/agent_context.go` | ~10 | Low |
| Apply budget adjustment after successful compaction | `internal/agent/agent_context.go` | ~15 | Low |
| Persist budget adjustment to session config | `internal/agent/agent_context.go` | ~10 | Low |
| Tests: history tracking, budget clamping | `internal/agent/agent_context_test.go` | ~40 | Low |

---

### OPT-6: Structured tool-content serialization (better compaction input)

**Problem**: When building the compaction prompt, tool outputs are serialized as raw text with a 2000-char truncation:
```go
fmt.Sprintf("%s: %s\n", m.Role, m.Content)
for _, tc := range m.ToolCalls {
    fmt.Sprintf("[tool:%s] %s\n", tc.Function.Name, tc.Function.Arguments)
}
```
This is wasteful for large tool results. Kilo Code's V2 uses `serializeToolContent()` which produces compact structured entries like `[tool: read, result: 1523 chars, 47 lines]`.

**Design**: For the compaction prompt, serialize tool results as structured stubs with size metadata, not full tool output. The LLM summarizer doesn't need the content; it needs a semantic hint of what the tool did and how much data it produced. The `Pruner` already has the stubbing logic; reuse that pattern at compaction input time.

**Side benefit**: Smaller compaction prompts means fewer context tokens consumed and less risk of the compaction request itself overflowing.

| Task | File | Lines | Risk |
|------|------|-------|------|
| `summarizeToolContent()` — produce structured stub | `internal/agent/agent_context.go` | ~25 | Low |
| Replace raw tool output in compaction prompt with stubs | `internal/agent/agent_context.go` | ~5 | Low |

---

### OPT-7: Post-compaction user-prompt replay

**Problem**: When compaction is triggered by context overflow (model says "too long, try again"), the user's last prompt is consumed by the compaction pass but never answered. yaah requires the user to re-submit. Kilo Code handles this with a replay mechanism.

**Design**: When compaction fires due to overflow (not proactive middleware), capture the user's last message. After compaction completes, automatically re-issue it through the loop so the model responds.

**This requires** distinguishing overflow-triggered compaction from proactive/middleware-triggered compaction. Add a `reason` field to `compactContext()`:

```go
type compactReason int
const (
    compactReasonProactive compactReason = iota
    compactReasonOverflow
)
```

When reason is `overflow`, after compaction succeeds, replay the last user message.

| Task | File | Lines | Risk |
|------|------|-------|------|
| Add `compactReason` parameter to `compactContext()` | `internal/agent/agent_context.go` | ~15 | Medium |
| Capture and replay last user message on overflow | `internal/agent/agent_context.go` | ~25 | Medium |
| Thread reason through pipeline middleware | `internal/agent/pipeline/compaction.go` | ~10 | Medium |

---

### OPT-8: Post-compaction re-pruning (reclaim first-turn waste)

**Problem**: After `compactContext()` calls `resetPruner()`, the pruned set is cleared but never re-applied. The first request after compaction sends full (unpruned) tool outputs from the preserved tail, wasting the window compaction just reclaimed.

**Design**: At the end of `compactContext()`, after rebuilding `l.Messages` and calling `resetPruner()`, immediately call `l.Pruner.Mark(l.Messages, "post_compaction")` to re-mark stale tool results in the fresh tail. This is safe because:
- Mark is pure bookkeeping (records tool-call IDs, never mutates messages)
- The SoftPruneMiddleware will call `Filter()` at request-build time via `applyPruning()`
- If nothing is beyond the protect window, `Mark` simply doesn't commit — no harm

| Task | File | Lines | Risk |
|------|------|-------|------|
| Add `pruner.Mark()` call after reset in `compactContext()` | `internal/agent/agent_context.go` | ~3 | Very low |
| Same in `trimContext()` fallback | `internal/agent/agent_context.go` | ~3 | Very low |
| Tests: verify pruned set is non-empty after compaction | `internal/agent/agent_context_test.go` | ~20 | Low |

---

### OPT-9: Prune on session resume (PrepareStep)

**Problem**: `SoftPruneMiddleware.PrepareStep` is a no-op. When loading a session from SQLite, old tool results from previous turns sit unpruned until the next tool call triggers `PostTool`. If the first user message in a resumed session produces no tool calls, the full historical tool output goes to the provider.

**Design**: Make `PrepareStep` call `pruner.Mark(step.Messages, "prepare")`. This is safe because `Mark` is idempotent for already-pruned messages (the walk terminates at the first one). Overheads:
- Each `Mark()` walks at most `MinTurns + 1` turns of messages (the walk terminates at the first already-pruned or system boundary)
- In the no-op case (nothing new to prune), the walk is fast and `Filter` is identity (empty pruned set)

```go
func (m *SoftPruneMiddleware) PrepareStep(ctx context.Context, step *Step) (*Step, error) {
    if m.pruner == nil {
        return step, nil
    }
    stats := m.pruner.Mark(step.Messages, "prepare")
    if m.otel != nil {
        m.otel(ctx, stats)
    }
    if m.emit != nil {
        m.emit(stats)
    }
    return step, nil
}
```

This is essentially the same as the `PostTool` handler but at `PrepareStep` time — de-duplicate into a shared `doMark()` helper.

| Task | File | Lines | Risk |
|------|------|-------|------|
| Extract `doMark()` helper from `PostTool` handler | `internal/agent/pipeline/softprune.go` | ~10 | Low |
| Call `doMark()` in `PrepareStep` | `internal/agent/pipeline/softprune.go` | ~5 | Low |
| Tests: resume session with old tool results, verify pruned on first request | `internal/agent/pipeline/softprune_test.go` | ~30 | Low |

---

## Implementation Order

| Priority | OPT | Effort | Impact | Dependencies |
|----------|-----|--------|--------|-------------|
| **P0** | OPT-1: Chunked compaction | High | Critical (replaces data loss) | OPT-3 (events) |
| **P0** | OPT-2: Overflow recovery + retry | Medium | Critical (unhandled errors today) | OPT-1 (chunked for compaction-on-recover) |
| **P0** | OPT-8: Post-compaction re-pruning | Trivial | High (wastes first-turn window) | none |
| **P0** | OPT-9: Prune on session resume | Low | High (unpruned on resume) | none |
| **P1** | OPT-3: Compaction events | Low | High (UX, would block on) | none |
| **P1** | OPT-4: Persistent cooldown | Trivial | Medium (long-running sessions) | none |
| **P2** | OPT-5: Adaptive budget | Low | Low (nice to have) | OPT-3 (feedback needs savings measurement) |
| **P2** | OPT-6: Structured tool serialization | Low | Low (tokens saved) | none |
| **P2** | OPT-7: Post-compaction replay | Medium | Medium (UX polish) | OPT-2 (overflow detection) |

## Files Modified

| File | Change |
|------|--------|
| `internal/agent/agent_chunked.go` | **new** — chunked compaction algorithm |
| `internal/agent/agent_chunked_test.go` | **new** — chunked compaction tests |
| `internal/agent/llm/overflow.go` | **new** — overflow pattern matching |
| `internal/agent/agent_context.go` | Add `compactReason`, chunked fallback, structured serialization, budget feedback, persistent cooldown fix, post-compaction re-mark |
| `internal/agent/llm/client.go` | Add `CompactFunc` callback for overflow retry |
| `internal/agent/agent.go` | Wire overflow retry in `Run()`, add `runWithOverflowRecovery()` |
| `internal/agent/events.go` | Add `CompactionStartedEvent`, `CompactionChunkDoneEvent`, `CompactionDoneEvent` |
| `internal/agent/pipeline/compaction.go` | Thread reason through middleware |
| `internal/agent/pipeline/softprune.go` | Extract `doMark()` helper, wire into `PrepareStep` |
| `internal/agent/pipeline/softprune_test.go` | **new or extended** — resume + prune test |
| `internal/tui/tui.go` | Handle compaction events for progress display |
| `cmd/yaah/agent_frame.go` | Handle compaction events in terminalView |

## Risks

- Chunked compaction triggers more LLM calls (N chunks + reduction steps). With concurrency=3 and typical N=2-4, this is 2-5 calls vs 1 today. Most sessions won't hit chunked mode because the compact model's context window (e.g., 128K) is large enough for the head. The cost impact is ~2x token cost on <10% of compaction-triggering turns.
- Overflow recovery increases surface area in the error path. The retry must be bounded (max 1 retry per turn) to avoid infinite loops. The `CompactFunc` must be idempotent or failure-safe.
- Budget feedback operates on the same Loop instance. If the user switches to a different conversation (new session), the budget resets. This is correct behavior.
- OPT-8 and OPT-9 involve no risk: `Mark` is already idempotent, the mutex is held, and `Filter` in the empty-pruned-set case is a no-op allocation.
