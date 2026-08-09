---
name: loop-refactoring
description: Architectural refactoring of the agent loop to eliminate god-object anti-patterns, shared mutable state, and token estimation inefficiencies.
status: not-started
---

# Agent Loop Architectural Refactoring Plan

## Note

This plan has not yet started. Diagnostic spans (`RecordTurnResponse`,
`RecordToolDispatch`, `RecordToolGoroutine`) were added to `loop.go` and
`agent_tools.go` during tui2 debugging sessions but these are additive
instrumentation, not refactoring. The architectural issues below remain.

## Architectural Anti-Patterns & Deficiencies Identified

1.  **God Object (`agent.Loop`) (SRP Violation):**
    `Loop` manages too many concerns. It builds configuration, drives LLM provider switching, coordinates middleware, manages local memory persistence, tracks tokens, tracks sub-agent concurrency, and dispatches external UI events.
2.  **Shared Mutable State (`ContextManager` & `LoopState`):** 
    `ContextManager` holds a direct pointer to `Loop.State` to mutate the `Messages` array to avoid standard synchronization. This breaks encapsulation. `ContextManager` uses a hack to satisfy the `Compactor` interface by delegating back to `Loop.compactFn`, yielding a circular behavioral dependency.
3.  **Awkward Pointers (`**pipeline.Step`):**
    The `executeToolPhase` takes `step **pipeline.Step`. In Go, using double pointers to mutate composite pipeline state is a major code-smell indicating that data flow (in/out) is poorly structured.
4.  **Inefficient Token Recalculation:**
    `estimatedTokens()` uses an $O(N)$ scan to aggregate token counts by re-running `agentctx.MessageTokens(m)` repeatedly across middleware and compaction steps, instead of tracking a running token delta in `LoopState`.
5.  **Go Memory Anti-Patterns (Staticcheck):**
    There are multiple instances of `sb.WriteString(func(...) + "\n")`. Go's `strings.Builder` is meant to avoid allocations; using `+` string concatenation inside the argument defeats the purpose of the builder by forcing heap allocations on every write.

---

## Phased Development Plan

### Phase 1: Tactical Health & Go Idioms (Immediate / Low Risk)
*Goal: Fix compilation warnings, resource inefficiencies, and obvious syntax anti-patterns.*

1.  **Resolve `staticcheck` Concatenation Errors:**
    *   Target: `internal/agent/context_manager.go` (Lines 361, 507, 512, 615).
    *   Action: Replace `sb.WriteString(func(...) + "\n")` with two independent calls: 
        `sb.WriteString(func(...)); sb.WriteByte('\n')` or `sb.WriteString("\n")`.
2.  **Clean up `executeToolPhase` Signatures:**
    *   Target: `internal/agent/turn.go` and `internal/agent/loop.go`.
    *   Action: Refactor the signature from `(..., step **pipeline.Step, ...)` to `(..., step *pipeline.Step) (*pipeline.Step, error)`. Let it return the mutated step as output, which aligns perfectly with how the rest of `pipeline` operates.

### Phase 2: SOLID & Context Uncoupling (Short to Medium Term)
*Goal: Remove circular dependencies and enforce the Single Responsibility Principle.*

1.  **Decouple `ContextManager` from `LoopState`:**
    *   Action: Remove the `State *LoopState` field from `ContextManager`. 
    *   Instead of `cm.estimatedTokens()` reaching into loop state, pass arrays directly to compaction functions: `func (cm *ContextManager) Compact(messages []types.Message, ...) []types.Message`.
2.  **Remove `compactFn` Circular Delegation:**
    *   Action: Move the actual compaction side-effects out of `Loop.compactContext` and fully into `ContextManager`. 
    *   The `Loop` should ask `ContextManager` for a compacted message array, receive it, and update its own state, rather than `ContextManager` modifying the loop's memory directly under the hood.

### Phase 3: Token Estimation & Efficiency Loop (Medium Term)
*Goal: Optimize latency within the agent loop by reducing O(N) array scans and heuristic estimations.*

1.  **Migrate to Delta Token Tracking:**
    *   Action: Update `LoopState` to include `RunningTokenCount`. 
    *   When the `Loop` initializes, calculate the token count once. As `Loop.executeAndCollect` appends tool results and `Loop` appends LLM responses, add only the delta of the newly appended messages to `RunningTokenCount`.
2.  **Refine Request Truncation vs. Estimation (Cost Efficiency):**
    *   Action: `ContextManager` currently uses a crude `EstimateFactor` (1.3x) multiplier for early preflights. Replace this by having `ContextManager` calculate exact fast-tokens for strings internally, falling back to estimation only for deeply nested JSON structures to reduce the chance of triggering an expensive summarization call prematurely.

### Phase 4: Pipeline Execution Extraction (Long Term)
*Goal: Dismantle the `Loop` God-Object.*

1.  **Extract Pipeline Engine:**
    *   Create a true `pipeline.Engine` structural concept. Right now, `Loop` manually calls `RunPrepareStep`, handles LLM calls, then manually calls `RunPostModel` and `RunPostTool`.
    *   Action: Move the LLM dispatch itself into a discrete execution step or terminal pipeline middleware. The `Loop` should just pass `userInput` to a localized engine orchestrator, freeing `Loop` to only care about session lifetime and UI Event bridging.
