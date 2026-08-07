# yaah — Architectural Review

> Review date: 2026-08-07
> Scope: full codebase, ~51.7k LOC across 345 Go files (89 test files), Go 1.25.
> State at review: builds clean (`go build ./...`), `go vet ./...` clean, on commit `dd3ef72`.

## 1. Executive summary

yaah is a well-engineered, vendor-free agent harness: ~51.7k LOC, single static binary,
clean `internal/` boundary. The standout strength is a disciplined **engine–view
separation** with a sealed, typed event system and a uniform middleware pipeline. The
standout liabilities are (a) **two shipping TUI frameworks** that roughly double the
terminal-UI dependency footprint, and (b) **stale architecture docs** that no longer match
the refactored types.

The TUI decision: **`tui` (bubbletea v2) is production-ready; `tui2` (tview) is a
cleaner-layout prototype missing ~40% of the features.** Recommendation and migration path
in §3.

---

## 2. What's working well

**Engine–view separation is exemplary.** `agent.View` (`internal/agent/view.go:12`) is a
one-method interface. The loop owns an internal `pubsub.Broker[Event]` + `BrokerView`
adapter; consumers (TUI, REPL, sub-agents, MCP, ACP) never get callbacks on `Loop`. Events
are a sealed interface (`internal/agent/events/events.go:30`) with pointer receivers, so
type switches are exhaustive-checked at compile time — adding an event forces every
`HandleEvent` to grow a `case`. This is the single best design decision in the codebase.

**The agent package is well-factored into subpackages** with clear, single concerns:
`events/`, `llm/` (retry/fallback/streaming), `pipeline/` (middleware), `subagent/` (role
profiles), `runner/` (task-tool composition), `context/` (pure leaf helpers — tokens,
split, prune, truncate), `errorclassify/`. The `context/` package being a pure leaf with no
imports back into `agent` is exactly right.

**Wiring is centralized and DRY.** `LoopBuilder` (`internal/agent/builder.go`) + functional
options (`options.go`) replaced the prior 30-field struct literals duplicated across
`wiring.go`, `serve.go`, `tui.go`. `newAgentSession` (`cmd/yaah/wiring.go`) is the single
composition root. The `Session` interface (`cmd/yaah/session.go:46`) is a clean driver
contract with a compile-time check.

**Middleware pipeline is uniform.** Every middleware implements
`PrepareStep/PostModel/PostTool` (`internal/agent/pipeline/pipeline.go`); the loop calls
them in three well-defined phases (`loop.go:32,189,207`). Adding middleware (approval,
permission, compaction, loop-detect, steer, followup, prompt-caching, staleness,
tool-concurrency) does not touch loop internals.

**Sentinel error types are idiomatic.** `MaxIterationsError`, `ToolTimeoutError`,
`ToolNotFoundError`, `ToolDeniedError` each implement `Is(target error) bool` so callers
match with `errors.Is(err, X{})`. This is the correct Go pattern.

**Context compaction is genuinely sophisticated** (`context_manager.go`): dual triggers
(effective vs. raw tokens), adaptive budget multiplier from a 5-sample savings window,
ineffective-compaction cooldown persisted to SQLite, single-pass → chunked map-reduce
fallback → trim fallback, reasoning-turn protection. This is production-grade context
management.

**Tool registry generation counter** (`tools.go:137`) lets the loop cache OpenAI tool
definitions keyed on `Generation()` and cheaply invalidate on mutation — a nice
micro-optimization.

---

## 3. TUI vs TUI2 — comparison and recommendation

### 3.1 What each is

| | `internal/tui` (bubbletea v2) | `internal/tui2` (tview/tcell) |
|---|---|---|
| Maturity | **Production** | Prototype |
| Component model | Monolithic `Model` + 22 files; manual layout math | 22 component subpackages; native Flex/Grid/Pages |
| Rendering | Full re-render of viewport string each tick | `QueueUpdateDraw` per widget |
| Markdown | glamour + custom table/tree/list rendering | tviewmd (native tview tags) |
| Modals | In-model state + manual palette-line math | `tview.Pages` overlays (cleaner) |
| Conversation model | `[]Message` with role-switch render | `conversationLog []convItem` (chronological, interleaved) |

### 3.2 Feature parity (what tui2 is missing)

Grep confirms `tui2` is **missing or has unwired**: in-message **search**
(`keymap.go:13` declares `ActionSearch` but `globalInputCapture` never handles it),
**follow-up** queuing, **steer** (Ctrl-T), **`:login`/`:logout`** (declared in
`command.go:25-26` but `HandleCommand` only handles Quit/Clear/Help/Compact/Model),
**`:verbose`** toggle, **`:copyview`**, **`:banner`** toggle, **mouse hover zones** with
OSC 22 pointer cursor, the OTel `RecordTUIView` instrumentation, and a **model
picker** (list-population wired, but filter/scrolling UI needs work).

`tui` has all of the above (`internal/tui/model.go:63` commands, `view.go:253` OSC 22,
`render.go` glamour-rich rendering, `events.go:358` context info).

### 3.3 Code-quality notes per TUI

**tui (bubbletea):** The layout logic in `view.go` (`paletteLines`, `maxModelLines`,
`adjustViewport`) is intricate manual arithmetic. This is inherent to Elm-style full-view
rendering and is the main thing tview removes. The mouse-motion handler
(`model.go:333-373`) iterates three zone slices per motion event — fine at human input
rates, but verbose.

**tui2 (tview):** Cleaner decomposition, but has a real bug-shaped smell: **duplicated
message state**. Both `plainMessages []string` *and* `conversationLog []convItem` hold text
(`tui2.go:59,68`; `helpers_msg.go:18-19` appends to both). `Clear()` resets both, but they
can drift; `plainMessages` appears vestigial. Also `OnAbort` only hides the thinking
indicator when `cancelAgent != nil` (`tui2.go:100-106`), so an abort press while idle can
leave a stale spinner. The `UpdateInfopane(tab, content)` method has
`_ = tab // TODO: wire infopane tabs` (`tui2.go:541`) — unfinished.

### 3.4 Recommendation

**Short term: keep `tui` as the default `yaah tui` and the documented UI.** It is
feature-complete and exercised. Do **not** delete `tui2` yet — its layout architecture
(Flex/Grid/Pages, chronological conversation log, component subpackages) is the better
long-term foundation, and it already implements `agent.View` correctly.

**Migration path if you choose tview long-term:**

1. Port the ~10 missing features (search, steer, follow-up, verbose, copyview,
   login/logout, model-picker data wiring, mouse zones, OTel recording) into tui2, checking
   each off against the `defaultCommands` list in `tui/model.go:63`.
2. Delete the `plainMessages` duplicate; make `conversationLog` the single source of truth.
3. Decide, then drop the loser package and its dependency cluster from `go.mod`. Until then
   you are compiling **both** `charm.land/{bubbletea,bubbles,glamour,lipgloss}` + `bubblezone`
   **and** `tview` + `tcell` into every binary.

**Lean:** given that you've already invested in the v2 `charm.land` stack and have a
working, rich `tui`, and tview's main win (layout) is obtainable, consolidate on **bubbletea
`tui`** unless the manual viewport/layout math is actively painful. If it is, commit to tui2
and finish it — don't carry both indefinitely.

---

## 4. Architectural concerns (by area)

### 4.1 ContextManager ↔ Loop coupling (medium)

`ContextManager` is "extracted" but not decoupled. It holds a `*LoopState` pointer
(`context_manager.go:51`) **and** its `compactFn` closes over the owning `Loop`
(`lifecycle_init.go:109`). Worse, there are two identical compaction entry points doing the
exact same thing:

- `Loop.Compact` (`loop.go:46-50`): sets `State.Messages`, calls `l.compactContext`, returns.
- `CtxMgr.compactFn` (`lifecycle_init.go:109-113`): sets `State.Messages`, calls
  `l.compactContext`, returns.

So `ContextManager.Compact` → `compactFn` → `Loop.compactContext`, while `Loop.Compact` →
`Loop.compactContext` directly. It's correct (no infinite loop — `compactContext` doesn't
re-enter `Compact`), but the indirection is confusing and the abstraction leaks.
`ContextManager` cannot be reused without a `Loop`. Consider either (a) making
`ContextManager` own `compactContext` outright and having the loop call it directly, or (b)
collapsing the `Loop.Compact` shim. Also `context_manager.go` is 656 lines mixing policy
config, state, LLM calls, map-reduce, and DB cooldown — a candidate to split
(`compact_single.go`, `compact_chunked.go`, `compact_cooldown.go`).

### 4.2 Stale architecture documentation (medium)

`docs/architecture.md` describes a `Loop` struct with fields that no longer exist:
`MaxIterations` (now `MaxLoopCycles`), `DB` and `MsgIdx` (now on `SessionPersister`), `Pipe`,
`MCPServers`, `MaxSubAgentDepth`/`MaxSubAgentDepthByRole` (removed — depth is hardcoded to
1, see `runner.go:45`). It also still says the loop lives in `agent.go`, which is now just a
package doc comment. The AGENTS.md repo layout, by contrast, is accurate. The detailed doc
should be regenerated against the post-#174/#176/#178 structure.

### 4.3 Dual TUI dependency cost (medium → resolves with §3 decision)

Until one TUI is dropped, every release pays for both terminal-UI stacks. `go.mod` lines
6-14 carry `charm.land/{bubbles,bubbletea,glamour,lipgloss}/v2`, `bubblezone/v2`,
`figlet-go` **and** `tcell/v2`, `tview`. This is the single biggest dependency-budget item
and is the strongest argument for making the TUI decision soon.

### 4.4 Serve-mode globals coupling tui/tui2/serve (low)

`extraOtelProcessors`, `otelInMemoryOnly` are package globals in `cmd/yaah/serve.go` reused
by both `tui.go` and `tui2.go`. Documented in AGENTS.md as an allowed exception, but it
means `tui`, `tui2`, and `serve` share mutable package state. Acceptable now; when you
collapse to one TUI, fold these into the session.

### 4.5 View concurrency contract vs tview (low)

`view.go:8-12` states `HandleEvent` is "called sequentially from a dedicated forwarder
goroutine" and "must be safe from a single goroutine." `tui2.HandleEvent` (`events.go:10`)
is invoked from that forwarder goroutine but immediately marshals via
`t.App.QueueUpdateDraw` — which is correct because `QueueUpdateDraw` is thread-safe. So tui2
*works*, but it relies on every code path going through `QueueUpdateDraw`; a future method
that touches widget state without marshaling would silently violate the contract. Worth a
one-line comment on `TUI2.HandleEvent` asserting "all mutations must go through
QueueUpdateDraw." The bubbletea path is unambiguously safe (`prog.Send` → main loop).

### 4.6 Tool dispatch hook ordering (low)

In `executeAndCollect` (`agent_tools.go:41-56`), the `deny`/`ask` rejection path emits
`ToolStart`+`ToolEnd` hooks **inline** (before the goroutine), while the normal path emits
them **inside the goroutine**. With concurrent tool calls this means hook events for denied
tools always precede hooks for approved ones regardless of call order. Not wrong, but the
two emission sites could be unified by moving approval into the goroutine (after the
concurrency semaphore is acquired) for consistent ordering.

### 4.7 Vestigial Go <1.22 loop-variable capture (trivial)

`agent_tools.go:39` still has `i, tc := i, tc`. The module is `go 1.25.8`, so per-iteration
loop variables are the default. Safe to delete everywhere `grep` finds the pattern (a sweep
would be cheap and tidy).

---

## 5. Code-quality observations

- **Sentinel errors are consistently good** — keep this standard; it's a model for the rest
  of the codebase.
- **`tools.go` package doc** is an excellent "how to add a tool" guide (`tools.go:1-62`);
  the `leafTools` map + `NewLeafTool` ensures sub-agent registries can't drift from the main
  registry (`tools.go:154`). Strong.
- **`runner.go`** has a 41-arg constructor (`NewTaskTool`, line 41) and a 17-field
  `taskRunnerOpts`. This is the most arg-heavy spot in the codebase and a natural future
  refactor target (a `TaskToolBuilder` would help), but it's tolerable since it's a
  composition layer called once.
- **Test coverage is uneven**: `internal/tools` (66 files, many `*_test.go`), `pipeline`
  (well-tested), `agent` (good). But `internal/tui2` has **no tests** visible in the listing
  (only `component_test.go`/`tui_test.go` under `tui`), and the ACP/MCP HTTP paths look
  thin. If tui2 is a real candidate, it needs tests.
- **Working-tree hygiene**: `yaah.exe` (52 MB) and `observability.test.exe` (19 MB) sit in
  the repo root. They are correctly gitignored (`*.exe`) and untracked, so not a repo
  problem — but they clutter the working tree and `ls`. A `go clean` or moving build outputs
  to `dist/` would tidy this.

---

## 6. Priority recommendations

| Priority | Action | Effort |
|---|---|---|
| **P0** | Make the TUI decision (§3). Pick one; port the missing features or delete the loser. Removes ~half the terminal-UI dependency weight and the dual-maintenance burden. | Med |
| **P1** | Refresh `docs/architecture.md` against current types (`LoopConfig`/`LoopState`/`ContextManager`/`SessionPersister`). The AGENTS.md layout is already correct — use it as the source of truth. | Sm |
| **P1** | Simplify the compaction indirection (§4.1): collapse `Loop.Compact` into one path; decide whether `ContextManager` owns `compactContext`. | Sm |
| **P2** | If keeping tui2: delete `plainMessages` duplicate; finish `UpdateInfopane`; wire search/steer/follow-up/verbose/login/logout/model-picker; add tests. | Med |
| **P3** | Sweep `i, x := i, x` loop captures (Go 1.25). | Xs |
| **P3** | Unify tool hook emission ordering (§4.6). | Sm |
| **P3** | Move build artifacts out of the repo root (`dist/`). | Xs |

---

## 7. Verdict

The codebase is in good shape — the concerns above are refinements, not structural defects.
The engine core (loop, events, pipeline, context management, tool registry, sub-agent
runner) is the strongest part; the TUI layer is where the open decisions and the duplicated
cost live.
