---
name: agent-context-split
description: Split the 718-line agent_context.go into tokens/compaction/compactor concerns, make the LoopConfig/LoopState/ContextManager state ownership explicit by de-duplicating compaction state, and (optionally) promote tokens and compaction to sub-packages.
status: draft
---

# Agent Context Split — Compaction & Token Estimation Extraction

## Context

`internal/agent/agent_context.go` is 718 lines mixing four concerns: token
estimation, request-time message preparation, pure compaction algorithms, and
the LLM compaction orchestrator (238-line `compactContext`). Chunked
compaction was already extracted to `agent_chunked.go` (252 lines), and a
`ContextManager` struct (`context_manager.go`) was created as a "Phase 1"
config/state holder — but Phase 2 ("migrate the methods here") never happened,
leaving two problems this plan resolves:

1. **agent_context.go is a god file** (>500-line guideline in
   `docs/code-organization.md`).
2. **Loop state ownership is ambiguous.** `LoopState` and `ContextManager`
   duplicate eight compaction-state fields; `lifecycle.go:97-101` copies
   `State → CtxMgr` once, then every compaction method reads/writes
   `l.State.*` — the `CtxMgr` copies are effectively dead writes.

### Current state (verified 2026-08-05, main @ `496d422`, tests green)

```
internal/agent/
├── types.go              134   Loop (40 fields) + LoopConfig (33) + LoopState (14)
├── agent_context.go      718   🔴 tokens + prep + compaction algs + orchestrator
├── agent_chunked.go      252   chunked/recursive compaction fallback
├── context_manager.go    137   ContextManager (Phase-1 stub, duplicated state)
├── lifecycle.go          192   applyDefaults + State→CtxMgr sync (lines 97-124)
├── loop.go               185   Run loop, Loop.Compact adapter
├── tools.go               78   addUsage, llmCompact/llmTrim adapters
├── turn.go               223   guardContextBeforeCall (payload guard)
└── options.go            197   NewLoop + functional options
```

### Findings from analysis

- **F1 — mixed concerns in agent_context.go.** Four cleanly separable groups
  (inventory below); the pure groups have zero `Loop` coupling.
- **F2 — duplicated compaction state.** `PreviousSummary`,
  `LastCompactionTokens`, `IneffectiveCompactions`,
  `CompactionForcedByOverflow`, `CompactionBudgetMultiplier`,
  `CompactionSavingsHistory` exist in BOTH `LoopState` and `ContextManager`.
  Runtime code uses `l.State.*` exclusively; the `CtxMgr` copies are written
  once in `applyDefaults` and never read.
- **F3 — dead flag.** Nothing in the repo sets
  `CompactionForcedByOverflow = true` — the `"overflow"` compaction reason
  path is unreachable. Out of scope to fix; preserved as-is during moves.
- **F4 — Config mutated after Run.** `applyDefaults` fills `l.Config.*`
  defaults at Run start and `loop.go:131-134` updates
  `Config.Model/Fallback*` after provider negotiation. LoopConfig is
  "immutable" in name only; the construction contract needs a doc and a
  single defaults-application point.
- **F5 — external API surface is small.** `cmd/yaah` reads only
  `loop.State.{Messages,TotalTokens,LastPromptTokens}` and
  `loop.Config.{Model,ContextWindow}` (agent_frame.go:967-993, serve.go,
  subagent_runner.go). No external code touches compaction-state fields.

### Baseline

```
go build ./...                    → ok
go test ./internal/agent/... -count=1 → ok (agent 14.4s + llm, pipeline, subagent, errorclassify all pass)
```

### Related plan: extract-llm-compactor (superseded)

The untracked draft `.agents/plans/extract-llm-compactor/` proposes an
`LLMCompactor` in the **pipeline** package. This plan supersedes it:
`pipeline` is deliberately provider-agnostic (it defines the `Compactor`
interface but has no Provider type), while compaction needs providers,
`memory.DB`, `prompts`, and `observability`. The right home for Phase 4 is
`internal/agent/compaction/`. When Phase 1 of this plan lands, delete or mark
the old draft superseded.

---

## Phase 1 — In-package file split (zero behavior change, 5 commits)

Branch `agent-context-split` from clean main. All moves are same-package
cut/paste: public API, names, and behavior unchanged; **no test edits
required** (`agent_context_test.go` etc. compile against the package, not
files). Build after each step.

### Step 1: `tokens.go` — token estimation (~110 lines)

| Symbol | From | Lines |
|---|---|---|
| `defaultEstimateFactor` | agent_context.go:21 | const |
| `maxPayloadBytes` | agent_context.go:29-34 | const (pairs with estimatePayloadBytes) |
| `messageTokens()` | agent_context.go:73-82 | pure |
| `preflightTokens()` | agent_context.go:140-152 | pure |
| `estimatePayloadBytes()` | agent_context.go:158-170 | pure |
| `EstimatedTokens()` | agent_context.go:49-55 | Loop method, fine in tokens.go |

Imports: `math`, `types`.

Commit: `refactor(agent): extract token estimation to tokens.go`.

### Step 2: `compaction.go` — pure compaction algorithms (~300 lines)

| Symbol | From | Lines |
|---|---|---|
| `minPreserveTokens`, `maxPreserveTokens` | agent_context.go:39-42 | consts |
| `defaultRawCompactionThreshold` | agent_context.go:27 | const |
| `summaryTemplate` | agent_context.go:46 | var |
| `adaptiveSavingsWindow` | agent_context.go:403 | const |
| `pruneMessageMaxLen` | types.go:58 | const (move next to its only user) |
| `lastUserPrompt()` | agent_context.go:59-66 | pure |
| `turnRange`, `turns()` | agent_context.go:175-196 | pure |
| `preserveBudget()` | agent_context.go:201-210 | pure |
| `splitResult`, `splitTail()`, `splitTurn()` | agent_context.go:214-274 | pure |
| `ProtectReasoningTurns()`, `EarliestReasoningIndex()` | agent_context.go:285-321 | pure |
| `truncateRunes()`, `pruneMessages()`, `formatToolStub()` | agent_context.go:441-497 | pure |

Imports: `fmt`, `strings`, `prompts`, `types`.

Commit: `refactor(agent): extract pure compaction algorithms`.

### Step 3: `compact.go` — LLM compaction orchestration (~420 lines)

| Symbol | From | Notes |
|---|---|---|
| `compactContext()` | agent_context.go:510-748 | 238-line orchestrator |
| `applyCompactedSummary()` | agent_context.go:323-392 | |
| `trackCompactionSavings()` | agent_context.go:405-436 | |
| `trimContext()` | agent_context.go:750-791 | fallback |
| `persisterDB()` | agent_context.go:394-401 | |
| `Compact()` | loop.go:27-31 | pipeline.Compactor adapter |
| `llmCompact()`, `llmTrim()` | tools.go:65-78 | llm.Compactor adapters |

Imports: `context`, `fmt`, `log`? (none), `strings`, `time`, otel, `memory`,
`observability`, `types`.

Optionally rename `agent_chunked.go` → `compact_chunked.go` in the same
commit for naming cohesion (pure rename, `git mv`).

Commit: `refactor(agent): extract LLM compaction orchestrator to compact.go`.

### Step 4: slim `agent_context.go` (~45 lines)

What remains — request-time preparation only:
- `prepareRequestMessages()` (lines 99-110)
- `StripAllReasoning()` (116-122)
- `countReasoningMessages()` (124-133)

Update the file's top-of-file purpose comment. Commit:
`refactor(agent): narrow agent_context.go to request preparation`.

### Step 5: Quality gates

```powershell
gofmt -l internal/agent/            # empty
go vet ./internal/agent/...
staticcheck ./internal/agent/...
go test ./internal/agent/... -count=1   # unchanged, no test edits
go test ./... -count=1                  # full suite
git diff main -w -- internal/agent      # whitespace-ignoring diff shows only moves
```

---

## Phase 2 — Explicit state ownership (de-duplicate LoopState/ContextManager)

After Phase 1 the files are organized, but F2 (duplicated state) remains.
Phase 2 makes the three owners explicit:

```
LoopConfig       immutable configuration (set before Run via options)
LoopState        conversation + usage state (messages, token totals, last-call facts)
ContextManager   context policy + ALL compaction state
```

### Step 1: Move compaction state to ContextManager exclusively

Move these **six fields** out of `LoopState` (types.go:143-158), keeping them
only on `ContextManager` (which already declares all six):

- `PreviousSummary`
- `LastCompactionTokens`
- `IneffectiveCompactions`
- `CompactionForcedByOverflow` (dead-write flag, F3 — preserve semantics)
- `CompactionBudgetMultiplier`
- `CompactionSavingsHistory`

Keep in `LoopState` (turn/usage facts with external readers):
`Messages`, `TotalTokens`, `LastPromptTokens`, `LastCachedPromptTokens`,
`TotalReasoningTokens`, `TotalCachedPromptTokens`, `LastFinishReason`,
`LastResponseModel`.

Rewrite the six fields' reads/writes in `compact.go` from `l.State.X` →
`l.CtxMgr.X` (~14 sites, all identified in the inventory commit). External
consumers are unaffected (F5 — `cmd/yaah` never touches these fields).

### Step 2: Delete the State→CtxMgr sync

In `lifecycle.go:95-114`, remove the five state-copy lines (97-101); keep
`CtxMgr` creation, config-mirror lines (103-110), `ReasoningProtectTurns`
default, and `EnsurePruner()`. Move the `CompactionBudgetMultiplier <= 0 →
1.0` default (lifecycle.go:126-128) into `NewContextManager()` so the
manager initializes its own state.

Also remove the now-redundant lazy `CtxMgr` nil-check at the top of
`compactContext` (agent_context.go:511-513) — `applyDefaults` guarantees it.

### Step 3: Fix the 16 test literals

`agent_context_test.go` constructs `Loop{State: LoopState{...}}` 16 times.
Sites setting moved fields must set `CtxMgr` instead:

| Line | Field set | Change |
|---|---|---|
| 643 | `PreviousSummary` | → `CtxMgr: &ContextManager{PreviousSummary: ...}` |
| 674-686, 717-745, 824-835 | `LastCachedPromptTokens` | unchanged (stays in LoopState) |
| 799-812 | `IneffectiveCompactions`, `LastCompactionTokens` | → CtxMgr literal; assertions at 812-813 read `loop.CtxMgr.IneffectiveCompactions` |

All other literals (`Messages` only) compile unchanged.

Gates: full `go test ./internal/agent/... -count=1` plus
`go test ./internal/agent/... -race -count=1` (compaction touches the
cooldown DB and chunked goroutines).

---

## Phase 3 — Explicit Loop construction contract

Small, doc-heavy step that makes the immutable/mutable boundary enforceable
by reading rather than by convention.

### Step 1: Isolate config defaulting

Extract the `l.Config.*` default block from `applyDefaults`
(lifecycle.go:129-146) into:

```go
// applyConfigDefaults fills zero-valued LoopConfig fields. Called exactly
// once at Run start; Config is treated as read-only afterward (exception:
// Model/Fallback* updates after provider negotiation in loop.go).
func applyConfigDefaults(cfg *LoopConfig)
```

`applyDefaults` calls it first. No value changes.

### Step 2: Document the contract on the types

- `Loop` struct doc: three ownership paragraphs (Config/State/CtxMgr).
- `LoopConfig` doc: "immutable after the first Run call" + the documented
  fallback exception.
- `LoopState` doc: "owned by Loop; modified only during Run; safe to read
  between Runs (session resume uses WithMessages)".
- `ContextManager` doc: delete the stale "Phase 1/Phase 2 will migrate"
  language; it is now the single owner of compaction state (Phase 2 of
  *this* plan completes that migration at the state level).

### Step 3: Optional — rename `WithMessages` → `WithResumedMessages`

Clarifies that this is the only option writing `State`. Cheap (3 call
sites: agent_frame.go:917, serve.go:357, tests) but a public API rename —
include only if the reviewer wants it.

---

## Phase 4 — Sub-packages (the original recommendation; optional, separate PR)

The recommendation suggested `internal/agent/compaction/` and
`internal/agent/tokens/`. Feasibility analysis:

### `internal/agent/tokens/` — easy ✅

Zero Loop coupling. Export `MessageTokens`, `PreflightTokens` (with
default-factor handling), `EstimatePayloadBytes`, `MaxPayloadBytes`.
Imports only `internal/types`. Callers (`compact.go`, `turn.go`,
`agent_chunked.go`, `tokens.go` removal) switch to qualified names. Loop's
`EstimatedTokens()` method stays, delegating to `tokens.Estimate(msgs)`.

### `internal/agent/compaction/` — requires design ⚠️

Move the pure algorithms (Phase-1 `compaction.go`) plus the orchestrator.
Two hard constraints:

1. **Import cycle.** `internal/agent` imports the compaction package, so
   compaction must NOT import `internal/agent` — but
   `CompactionStartedEvent`/`CompactionDoneEvent` live in
   `internal/agent/events.go`. Resolution: define the events (or a
   `Result` struct) in the compaction package and have `Loop` re-publish
   them as agent events, **or** inject a `publish func(any)` closure
   created on the agent side. Recommended: closure injection — smallest
   surface, no type duplication.
2. **Dependencies.** The orchestrator needs a provider (use `llm.Provider`
   from `internal/agent/llm` — no cycle, `llm` does not import `agent`),
   `memory.DB`, `prompts`, `observability`, `pipeline.Pruner`. The
   sub-package compactor absorbs today's `ContextManager` struct wholesale
   (it already holds exactly these injected deps), and `Loop.CtxMgr`
   becomes `*compaction.Compactor`. `pipeline.Compactor` interface
   satisfaction moves to the new type.

Deliverable shape:

```
internal/agent/tokens/
└── tokens.go            MessageTokens, PreflightTokens, EstimatePayloadBytes
internal/agent/compaction/
├── compactor.go         Compactor struct (today's ContextManager + orchestrator)
├── split.go             turns, preserveBudget, splitTail, reasoning protection
├── prepare.go           pruneMessages, formatToolStub, truncateRunes
└── chunked.go           chunkSplit, summarizeChunk, reducePartialSummaries
```

Decision gate: land Phases 1-3 first; Phase 4 is worthwhile only if a
second consumer of the compactor emerges (e.g., tui2 background compaction)
or if `compact.go` grows again. Until then the in-package split delivers the
same clarity at near-zero risk. Supersede/delete
`.agents/plans/extract-llm-compactor/PLAN.md` when Phase 1 merges.

---

## What does NOT change

- Public API: `Loop`, `LoopConfig`, `LoopState` (minus six moved fields),
  `ContextManager`, `NewLoop`, all options, `EstimatedTokens`,
  `ProtectReasoningTurns`, `EarliestReasoningIndex` (exported pure helpers).
- Compaction behavior: thresholds, triggers, budgets, adaptive multiplier,
  cooldowns, reasoning protection, chunked fallback.
- `cmd/yaah` construction code (Phases 1-3; Phase 4 touches imports).
- `pipeline`, `llm`, event types, sub-agent loop semantics.

## Risks

| Risk | Likelihood | Mitigation |
|---|---|---|
| Missed `l.State.X` → `l.CtxMgr.X` rewrite in Phase 2 | Medium | Compiler catches removed fields; grep audit for the six names before merging |
| Test literals set removed fields | Known (16 sites) | Step table above enumerates every affected site |
| Dead `CompactionForcedByOverflow` flag gets "fixed" mid-refactor | Low | Explicitly out of scope (F3); file a follow-up bead instead |
| Phase 4 event-boundary design splits opinions | Medium | Gated behind Phases 1-3; recommend closure injection |
| Dirty working tree on main | Low | Branch from clean base only |

## Rollback

Phases 1-3 are independent commits on one branch; revert in reverse order.
Phase 4 lands as a separate PR and reverts as a unit.

## Documentation updates (with merge)

- `docs/code-organization.md`: update the stale `internal/agent` table
  (agent.go is already 2 lines; agent_context.go split done), mark
  Future-Work items 3 and 5 complete.
- `AGENTS.md` repo-layout block for `internal/agent/`: replace the
  `agent.go` comment with the new file list.
