---
name: context-degradation-fix
description: Fix progressive output slowdown caused by unbounded context growth, unpruned reasoning content, and per-turn O(n) waste
status: planned
---

## Context Degradation Fix: Analysis & Implementation Plan

### 0. Problem Statement

yaah output gets progressively slower the longer a session runs. Root cause analysis
identifies five compounding issues, ordered by impact:

1. **Cache-aware compaction trigger suppresses context reduction** — the unique
   `LastCachedPromptTokens` subtraction (`agent_context.go:310-312`) means compaction
   never fires in cached conversations, allowing context to fill the entire window.
2. **Reasoning content accumulates and is never pruned** — `ReasoningContent` on
   assistant messages is re-serialized in every provider request but never stripped,
   truncated, or counted in token estimates.
3. **Tool definitions rebuilt every turn** — `buildToolDefs()` (`agent.go:435`)
   re-reads all schemas from the registry and allocates `json.RawMessage` per tool
   per iteration, with no caching.
4. **No payload-size guard** — unlike kilocode's 1.25MB check, yaah has no
   byte-level safety net to force compaction when the serialized request is too large.
5. **Per-turn O(n) allocations** — `repairOrphans` allocates 2 maps + a new slice
   every turn (`agent_orphan.go:7-45`); `applyPruning` copies the full array when
   pruning is active.

### 1. Current State Analysis

#### 1.1 The Compaction Trigger (agent_context.go:282-323)

```go
estimatedTokens := l.LastPromptTokens
if estimatedTokens > 0 && l.LastCachedPromptTokens > 0 {
    estimatedTokens -= l.LastCachedPromptTokens   // ← cache subtraction
}
if estimatedTokens < target {
    return   // ← compaction skipped
}
```

Steady-state scenario: 100k prompt tokens, 90k cached → `estimatedTokens = 10k`,
target = 64k → compaction never fires. The conversation grows to fill the context
window while the trigger reports "plenty of room."

The pre-flight guard at `agent.go:483-487` only fires when `LastPromptTokens >
ContextWindow` (absolute hard limit) — by then the provider is already processing
the maximum payload.

#### 1.2 Reasoning Content (types.go:14, stream.go:191, agent.go:501)

`ReasoningContent` is captured in `assembleStreamed` (`stream.go:191`), appended
to the message history (`agent.go:501`), and serialized in every subsequent
`json.Marshal(req)` (`providers.go:47`). It is:

- **Not counted** by `messageTokens()` (`agent_context.go:74-83`) — only `Content`
  is measured, so token estimates undercount by the full reasoning size.
- **Not pruned** by soft-prune (`pruner.go:208-222`) — only `tool` messages are
  stubbed.
- **Not truncated** by `pruneMessages()` (`agent_context.go:248-269`) — only
  `Content` on non-tool-call assistant messages is truncated; `ReasoningContent`
  is untouched.
- **Not stripped** by `repairOrphans()` or `applyPruning()`.

With the default `deepseek-v4-pro` model, each turn can produce 2k-10k reasoning
tokens. Over 20 turns, that's 40k-200k invisible tokens re-sent every request.

#### 1.3 Tool Definition Rebuild (agent_tooldefs.go:10-27)

`buildToolDefs()` is called at `agent.go:435` (step.Tools) and
`buildToolsForLevel()` at `agent.go:451` (req.Tools) — both every iteration.
Each call iterates `Registry.List()`, calls `Registry.Get(name)` per tool, reads
`Schema()` (which returns `json.RawMessage`), and allocates a new `ToolDef` struct.
With 20+ built-in tools plus MCP tools, this is ~50+ allocations per turn.

The `Registry` struct (`tools.go:99-101`) is a plain `map[string]Tool` with no
versioning or change notification.

#### 1.4 Dead Code: isContinuation (agent_context.go:108-125)

Defined and tested (`agent_context_test.go:93-172`) but never called in production
code. The BENCHMARKS.md analysis (lines 58-62) recommended removing the guard when
token-budgeted splitTail is active — the guard was removed from the compaction path
but the function was left behind.

---

## 2. Implementation Phases

### Phase 1: Dual-Threshold Compaction Trigger

**Goal:** Prevent unbounded context growth by adding a raw-token ceiling that
compacts regardless of cache hit rate.

| Task | Description | File | Completed | Date |
|------|-------------|------|-----------|------|
| TASK-001 | Add `RawCompactionThreshold` field to `Loop` struct (default 0.5). This is the fraction of `ContextWindow` at which compaction fires based on raw `LastPromptTokens`, independent of cache subtraction. | `agent.go` | | |
| TASK-002 | Add `rawCompactionThreshold` constant (`0.5`) to `agent_context.go` as the default when the field is zero. | `agent_context.go` | | |
| TASK-003 | Modify `compactContext` to use a dual trigger: compact if `effectiveTokens >= target` **OR** `LastPromptTokens >= rawTarget` where `rawTarget = ContextWindow * RawCompactionThreshold`. The cache subtraction still applies to the effective-tokens path (preserving the cost-optimization intent), but the raw path catches latency degradation. | `agent_context.go:306-323` | | |
| TASK-004 | Wire `RawCompactionThreshold` through `PipelineConfig` and `CompactionMiddleware` so the middleware passes it to `Compact()`. Update the `Compactor` interface signature or add a `RawThreshold` field to `PipelineConfig`. | `pipeline/config.go`, `pipeline/compaction.go` | | |
| TASK-005 | Add unit tests: (a) compaction fires when effective tokens are low but raw tokens exceed rawTarget; (b) compaction does NOT fire when both effective and raw tokens are under their respective targets; (c) the cache subtraction still works on the effective-tokens path. | `agent_context_test.go` | | |

**Implementation detail for TASK-003:**

Replace `agent_context.go:306-323` with:

```go
estimatedTokens := l.LastPromptTokens

// Effective tokens: subtract cached tokens for cost-based triggering.
effectiveTokens := estimatedTokens
if effectiveTokens > 0 && l.LastCachedPromptTokens > 0 {
    effectiveTokens -= l.LastCachedPromptTokens
}

// Raw threshold: compact when total prompt tokens exceed this fraction
// of the context window, regardless of caching. Cached tokens are cheaper
// but not free — serialization, network transfer, and provider-side cache
// lookup all scale with total context size.
rawThreshold := l.RawCompactionThreshold
if rawThreshold <= 0 {
    rawThreshold = defaultRawCompactionThreshold // 0.5
}
rawTarget := int(float64(l.ContextWindow) * rawThreshold)

if effectiveTokens < target && estimatedTokens < rawTarget {
    return
}
```

**Design rationale:** The dual trigger preserves the original intent (don't
over-compact cached conversations from a cost perspective) while adding a latency
guard. At 50% of context window, the provider is already processing a large prompt;
compacting here keeps TTFT manageable. The `0.5` default is conservative — hermes
uses 0.5, crush uses 0.2 for small windows.

---

### Phase 2: Reasoning Content Management

**Goal:** Prevent reasoning content from accumulating unboundedly in provider
requests. Strip it from old messages while preserving it for recent turns.

| Task | Description | File | Completed | Date |
|------|-------------|------|-----------|------|
| TASK-006 | Fix `messageTokens()` to include `ReasoningContent` in the token estimate: `tokens += len(m.ReasoningContent) / 4`. This makes all downstream estimates (preflight, EstimatedTokens, splitTail budget) accurate. | `agent_context.go:74-83` | | |
| TASK-007 | Create `stripOldReasoning(messages []types.Message, protectTurns int) []types.Message` in a new file `agent_reasoning.go`. Walk messages backwards, count user-message turns, and for assistant messages older than `protectTurns` turns, set `ReasoningContent = ""` on the copy. Return the input slice unchanged (zero alloc) if nothing was stripped. | `agent_reasoning.go` (new) | | |
| TASK-008 | Integrate reasoning stripping into the request-build path. Replace the inline call at `agent.go:450` with a new `prepareRequestMessages(messages)` method that chains: `repairOrphans` → `applyPruning` → `stripOldReasoning`. This keeps the stored `l.Messages` intact (reasoning is only stripped from the provider copy). | `agent.go:450`, `agent_reasoning.go` | | |
| TASK-009 | Add `ReasoningProtectTurns` field to `Loop` (default 2, matching the pruner's `MinTurns`). Wire through `applyDefaults`. | `agent.go` | | |
| TASK-010 | Add unit tests: (a) reasoning stripped from messages older than protect window; (b) reasoning preserved on recent messages; (c) tool-call/result pairs not split; (d) zero-alloc fast path when no reasoning content exists; (e) `messageTokens` now counts reasoning. | `agent_reasoning_test.go` (new), `agent_context_test.go` | | |

**Implementation detail for TASK-007:**

```go
// stripOldReasoning returns a copy of messages with ReasoningContent
// removed from assistant messages older than protectTurns user-message
// turns. The original messages are not mutated. Fast path: returns the
// input slice unchanged when no stripping is needed.
func stripOldReasoning(messages []types.Message, protectTurns int) []types.Message {
    // Find the cutoff index: walk backwards counting user turns.
    turns := 0
    cutoff := 0
    for i := len(messages) - 1; i >= 0; i-- {
        if messages[i].Role == "user" {
            turns++
            if turns >= protectTurns {
                cutoff = i
                break
            }
        }
    }

    // Check if any message before cutoff has reasoning content.
    needsStrip := false
    for i := 0; i < cutoff; i++ {
        if messages[i].ReasoningContent != "" {
            needsStrip = true
            break
        }
    }
    if !needsStrip {
        return messages // zero-alloc fast path
    }

    out := make([]types.Message, len(messages))
    copy(out, messages)
    for i := 0; i < cutoff; i++ {
        if out[i].ReasoningContent != "" {
            out[i].ReasoningContent = ""
        }
    }
    return out
}
```

**Design rationale:** Stripping reasoning from old messages is safe because:
(a) the model generates fresh reasoning each turn — old reasoning is not needed
for continuation; (b) providers like DeepSeek/OpenAI don't require reasoning
content in subsequent requests; (c) the stored messages in `l.Messages` and the
DB retain full reasoning for session replay/debugging. Only the ephemeral
provider request is stripped.

---

### Phase 3: Tool Definition Caching

**Goal:** Eliminate per-turn tool definition rebuilds by caching the result
and invalidating only when the registry changes.

| Task | Description | File | Completed | Date |
|------|-------------|------|-----------|------|
| TASK-011 | Add a `generation int` field to `tools.Registry`. Increment it in `Register()`. Add a `Generation() int` accessor. | `tools/tools.go:99-101,156-158` | | |
| TASK-012 | Add `toolDefsCache []types.ToolDef`, `toolDefsGen int` fields to `Loop`. In `buildToolDefs()`, return the cache if `l.toolDefsGen == l.Registry.Generation()`. Otherwise rebuild and update the cache. | `agent.go`, `agent_tooldefs.go:10-27` | | |
| TASK-013 | Add unit test: register tools, call `buildToolDefs` twice, verify the second call returns the same slice (pointer equality). Register a new tool, verify the cache is invalidated. | `agent_tooldefs_test.go` (new) | | |

**Implementation detail for TASK-012:**

```go
func (l *Loop) buildToolDefs() []types.ToolDef {
    gen := l.Registry.Generation()
    if l.toolDefsCache != nil && l.toolDefsGen == gen {
        return l.toolDefsCache
    }
    toolNames := l.Registry.List()
    toolDefs := make([]types.ToolDef, 0, len(toolNames))
    for _, name := range toolNames {
        t := l.Registry.Get(name)
        if t == nil {
            continue
        }
        toolDefs = append(toolDefs, types.ToolDef{
            Type: "function",
            Function: types.ToolFn{
                Name:        t.Name(),
                Description: t.Description(),
                Parameters:  json.RawMessage(t.Schema()),
            },
        })
    }
    l.toolDefsCache = toolDefs
    l.toolDefsGen = gen
    return toolDefs
}
```

---

### Phase 4: Payload-Size Guard

**Goal:** Add a byte-level safety net that forces compaction when the serialized
request payload exceeds a threshold, regardless of token estimates.

| Task | Description | File | Completed | Date |
|------|-------------|------|-----------|------|
| TASK-014 | Add `maxPayloadBytes` constant (1,250,000 = ~1.25MB, matching kilocode's `prompt.ts:107`) to `agent_context.go`. | `agent_context.go` | | |
| TASK-015 | Add a payload-size pre-flight check in `runMiddleware` alongside the existing token pre-flight guard (`agent.go:483-487`). Estimate payload bytes by summing `len(Content) + len(ReasoningContent) + tool-call arg lengths` across all messages plus tool definition sizes. If over `maxPayloadBytes`, call `compactContext(turnCtx, 0.5)` and rebuild `req.Messages`. | `agent.go:483-487` | | |
| TASK-016 | Add unit test: construct a message list whose estimated payload exceeds `maxPayloadBytes`, verify compaction is triggered. | `agent_context_test.go` | | |

**Implementation detail for TASK-015:**

Insert after the existing pre-flight guard at `agent.go:487`:

```go
// Payload-size guard: force compaction when the serialized request
// would exceed the byte threshold, regardless of token estimates.
// Token heuristics (chars/4) can undercount by 2-4x for code/JSON;
// a byte-level check catches cases the token trigger misses.
if payloadBytes := estimatePayloadBytes(messages, req.Tools); payloadBytes > maxPayloadBytes {
    l.compactContext(turnCtx, 0.5)
    messages = l.Messages
    req.Messages = l.applyPruning(repairOrphans(messages))
}
```

With helper:

```go
func estimatePayloadBytes(messages []types.Message, tools []types.ToolDef) int {
    total := 0
    for _, m := range messages {
        total += len(m.Content) + len(m.ReasoningContent)
        for _, tc := range m.ToolCalls {
            total += len(tc.Function.Arguments) + len(tc.Function.Name) + len(tc.ID)
        }
    }
    for _, t := range tools {
        total += len(t.Function.Description) + len(t.Function.Parameters) + len(t.Function.Name)
    }
    return total
}
```

---

### Phase 5: Dead Code Cleanup

**Goal:** Remove unused code that creates confusion about compaction behavior.

| Task | Description | File | Completed | Date |
|------|-------------|------|-----------|------|
| TASK-017 | Remove `isContinuation()` function from `agent_context.go:104-125`. It is defined and tested but never called in production code. The BENCHMARKS.md analysis (lines 58-62) confirmed the guard is unnecessary with token-budgeted splitTail. | `agent_context.go` | | |
| TASK-018 | Remove the `isContinuation` test cases from `agent_context_test.go:93-172`. | `agent_context_test.go` | | |

---

## 3. Alternatives Considered

- **ALT-001: Remove cache subtraction entirely.** Simpler but loses the
  cost-optimization intent. A conversation with 95% cache hit rate would compact
  aggressively, wasting the warm cache and increasing cost. The dual-threshold
  approach preserves cost optimization while adding a latency guard.

- **ALT-002: Strip reasoning content in the Pruner (soft-prune).** Would mix two
  concerns (tool-result elision vs reasoning stripping) in one component. The
  Pruner's `Filter` operates on tool-call IDs; reasoning is on assistant messages
  with no tool-call ID keying. A separate function is cleaner.

- **ALT-003: Use a middleware for reasoning stripping.** Fits the pipeline
  architecture but adds a middleware that runs every turn for a simple O(n)
  operation. The `prepareRequestMessages` approach is simpler and keeps the
  request-build path in one place.

- **ALT-004: Cache tool defs with a hash of tool names.** More complex than a
  generation counter and doesn't handle tool replacement (same name, new schema).
  The generation counter increments on any `Register()` call, covering both
  additions and replacements.

- **ALT-005: Persist reasoning content stripping state to DB.** Unnecessary —
  stripping is idempotent and cheap. Re-deriving the cutoff from the message list
  each turn is O(n) but the fast path (no reasoning to strip) is O(1) after the
  initial scan.

## 4. Dependencies

- **DEP-001:** No new external dependencies. All changes use stdlib and existing
  internal packages.
- **DEP-002:** Phase 2 depends on Phase 1 (the `messageTokens` fix in TASK-006
  affects the compaction trigger behavior in Phase 1). Implement Phase 1 first,
  then Phase 2.
- **DEP-003:** Phases 3, 4, 5 are independent of each other and of Phases 1-2.
  They can be implemented in any order or in parallel.

## 5. Files Affected

- **FILE-001:** `internal/agent/agent.go` — Loop struct fields, runMiddleware
  request-build path, applyDefaults
- **FILE-002:** `internal/agent/agent_context.go` — compactContext dual trigger,
  messageTokens fix, maxPayloadBytes constant, isContinuation removal
- **FILE-003:** `internal/agent/agent_reasoning.go` — NEW: stripOldReasoning,
  prepareRequestMessages
- **FILE-004:** `internal/agent/agent_tooldefs.go` — buildToolDefs caching
- **FILE-005:** `internal/agent/pipeline/config.go` — RawCompactionThreshold in
  PipelineConfig
- **FILE-006:** `internal/agent/pipeline/compaction.go` — pass raw threshold
- **FILE-007:** `internal/tools/tools.go` — Registry generation counter
- **FILE-008:** `internal/agent/agent_context_test.go` — new tests, remove
  isContinuation tests
- **FILE-009:** `internal/agent/agent_reasoning_test.go` — NEW: reasoning tests
- **FILE-010:** `internal/agent/agent_tooldefs_test.go` — NEW: caching tests

## 6. Testing

- **TEST-001:** Dual-threshold trigger: compaction fires on raw tokens even when
  effective (cache-adjusted) tokens are below target.
- **TEST-002:** Dual-threshold trigger: compaction does NOT fire when both
  effective and raw tokens are under their targets.
- **TEST-003:** Cache subtraction still works on the effective-tokens path.
- **TEST-004:** `messageTokens` counts `ReasoningContent`.
- **TEST-005:** `stripOldReasoning` strips reasoning from old messages, preserves
  recent ones, handles edge cases (empty messages, no reasoning, single turn).
- **TEST-006:** `stripOldReasoning` zero-alloc fast path when no reasoning exists.
- **TEST-007:** `prepareRequestMessages` chains repairOrphans → applyPruning →
  stripOldReasoning correctly.
- **TEST-008:** `buildToolDefs` returns cached result on second call; invalidates
  after `Register()`.
- **TEST-009:** Payload-size guard triggers compaction when estimated bytes
  exceed `maxPayloadBytes`.
- **TEST-010:** Existing tests pass unchanged (`go test ./...`).

**Verification commands:**

```bash
go test ./internal/agent/... -v -count=1
go test ./internal/tools/... -v -count=1
go vet ./...
gofmt -l .    # must be empty
go run honnef.co/go/tools/cmd/staticcheck@latest ./...
```

## 7. Risks & Assumptions

- **RISK-001:** The raw compaction threshold (0.5) may be too aggressive for
  some workflows, causing unnecessary compaction that loses useful context.
  **Mitigation:** The threshold is configurable via `Loop.RawCompactionThreshold`.
  Monitor via the existing OTel compaction spans and JSONL hook events.

- **RISK-002:** Stripping reasoning content may degrade model quality for
  providers that use prior reasoning as context (e.g., some o-series models).
  **Mitigation:** The `ReasoningProtectTurns` field (default 2) preserves recent
  reasoning. Providers that need full reasoning history can set this to a high
  value or disable stripping via `PipelineDisabled`.

- **RISK-003:** The generation counter on `Registry` assumes `Register()` is the
  only mutation path. If tools are removed or modified without calling `Register()`,
  the cache won't invalidate. **Mitigation:** The Registry has no `Unregister`
  method; all mutations go through `Register()`.

- **ASSUMPTION-001:** Providers do not require `ReasoningContent` in subsequent
  requests for correct behavior. This is true for DeepSeek, OpenAI, and
  Anthropic-compatible APIs. If a provider requires it, the stripping can be
  disabled per-provider.

- **ASSUMPTION-002:** The 1.25MB payload threshold is appropriate for most
  deployments. Local models with smaller context windows will hit the token-based
  trigger first; the byte guard is a safety net for cases where token estimates
  are inaccurate.

## 8. Implementation Order

```
Phase 1 (dual threshold) ──→ Phase 2 (reasoning) ──→ [verify with benchmarks]
                                                          │
Phase 3 (tool cache) ─────────────────────────────────────┤
Phase 4 (payload guard) ──────────────────────────────────┤
Phase 5 (dead code) ──────────────────────────────────────┘
                                                          │
                                                    BENCHMARKS.md run
```

Phase 1 and 2 are the highest-impact changes and should be implemented first.
Phases 3-5 are independent and can be done in parallel or any order.

After all phases, run the B4 Audit benchmark (see BENCHMARKS.md) and compare
turn count, total tokens, and wall-clock time against the `main` branch baseline.
The expected improvement is:
- Fewer turns to completion (context stays smaller → model is more focused)
- Lower total prompt tokens (reasoning stripped, compaction fires earlier)
- Lower TTFT variance (context doesn't grow unboundedly)

## 9. Related References

- `FRAMEWORK-COMPARISON.md` — cross-framework analysis (root of this investigation)
- `BENCHMARKS.md` — benchmark history and continuation guard analysis
- `.agents/plans/engine-view-separation/PLAN.md` — prior refactoring plan (format reference)
- hermes `context_compressor.py:239-240` — 50% threshold, no cache subtraction
- kilocode `prompt.ts:107-108` — 1.25MB payload-limit pruning
- kilocode `compaction.ts:47-53` — PRUNE_MINIMUM/PRUNE_PROTECT constants
