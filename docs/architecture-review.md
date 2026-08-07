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
in §3; deep-dive architectural review of tui2's internals in §4.

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

## 4. TUI2 deep-dive architectural review

> Scope: `internal/tui2/` — 19 component subpackages, ~3,500 LOC, tview/tcell-based.

### 4.1 Package topology

```
internal/tui2/
├── tui2.go              # App struct — central nervous system (~440 LOC)
├── control.go           # Control — per-frame delta aggregator
├── events.go            # agent.View implementation (HandleEvent)
├── keymap.go            # Key binding registry + input capture
├── markdown.go          # Markdown → tview conversion (tviewmd fork)
├── helpers_msg.go       # Append message to conversation log
├── helpers_tool.go      # Append tool result to conversation log
├── helpers_subagent.go  # Sub-agent cycle management
├── colors/              # ANSI color palette (256-color, dim/bold variants)
├── lolcat/              # Rainbow text effect
└── components/
    ├── messages/        # Chronological conversation log
    │   ├── assistant/   #   Assistant message (markdown body)
    │   └── tool/        #   Tool invocation/result pair
    ├── subagent/        # Sub-agent sponsorship + lifecycle display
    ├── toolblock/       # Tool execution with output folding
    ├── reasoning/       # Reasoning block (collapsible, lolcat header)
    ├── thinking/        # Animated spinner indicator
    ├── todo/            # Current todo list display
    ├── approval/        # Tool approval overlay
    ├── question/        # Interactive multi-choice question modal
    ├── input/           # Chat input box with history
    ├── statusbar/       # Bottom status line
    ├── infobar/         # Token usage + cost counter
    ├── infopane/        # Context/help detail pane (partial)
    ├── banner/          # Figlet title banner with lolcat
    └── help/            # Keybinding help modal
```

### 4.2 Component model — the flat-factory pattern

Every component subpackage exports **exactly one constructor** and returns a fully
initialized `tview.Primitive`. There is no abstract `Component` interface — components are
plain Go `struct` types, each owning its tview widgets and state, and they communicate
through the central `App` via method calls. This is **not** the Elm/React model (where
components return a virtual tree) — it's an **imperative widget tree** pattern.

**The pattern, by example** (`components/toolblock/toolblock.go`):

```go
func New(app *App) ToolBlock { ... }    // constructor, takes *App for callbacks
type ToolBlock struct { ... }           // concrete struct
func (tb *ToolBlock) AddTool(name, args string)   { ... }
func (tb *ToolBlock) AddResult(content string)    { ... }
func (tb *ToolBlock) SetStatus(status string)     { ... }
func (tb *ToolBlock) Primitive() tview.Primitive  { ... }  // for layout
```

**Strengths:**
- Dead simple. No virtual DOM, no diffing, no reconciliation.
- Each component owns its lifecycle; the `App` just calls methods at the right times.
- Zero abstraction overhead — tview widgets ARE the component state.

**Weaknesses:**
- **No standard lifecycle.** Some components have `Primitive()` for layout, others are
  registered directly via `App.Pages.AddPage()`. There is no compile-time guarantee that a
  component provides a layout primitive.
- **No isolation.** Every component receives `*App` and can reach into any other component
  or mutate global state. In practice this is disciplined (only the `App` mutates), but
  nothing prevents a component from calling `app.Messages.AddMessage(...)` directly.
- **The `App` struct is a god object.** At 440 LOC with 30+ fields, it holds every widget,
  every state variable, and every helper method. This is the unavoidable consequence of
  the "flat" component model — without a hierarchy, the root must know about everything.

**Contrast with old tui (bubbletea):** The old TUI has a single `Model` struct with a
`Update(msg tea.Msg) (tea.Model, tea.Cmd)` function — the Elm Architecture. Every component
state lives in `Model`, and `Update` is a single large `switch msg := msg.(type)` with 30+
cases. TUI2's approach is arguably _less_ monolithic at the state level (each component is a
separate struct), but _equally_ monolithic at the control level (all routing flows through
`App`).

### 4.3 Event handling — the `agent.View` bridge

`HandleEvent` (`events.go:10`) is the sole method satisfying `agent.View`. The call chain:

```
agent.Loop goroutine
  → pubsub.BrokerView.Send(Event)          // pubsub marshals to subscriber channel
  → forwarder goroutine (cmd/yaah/tui.go)  // reads from channel, calls HandleEvent
  → tui2.HandleEvent(ev)                   // type-switch on the sealed interface
  → app.QueueUpdateDraw(func() { ... })    // marshals onto tview's main thread
  → component methods called inside QUD    // safe widget mutation
```

**What it gets right:**
- `QueueUpdateDraw` is the correct tview mechanism for cross-goroutine mutations.
- The `agent.Event` sealed interface forces exhaustive handling — compile-time guarantee
  that new event types can't be silently dropped.
- The forwarder goroutine is shared by both TUIs (`cmd/yaah/tui.go`); TUI2 plugs into
  the same event bus without special treatment.

**What it gets wrong / missing:**
- The type-switch in `HandleEvent` has ~45 cases. This is the same problem as the old
  TUI's `Update` switch — it will grow unboundedly as event types are added.
- `UserCancelledReasoning` and `UserCancelledThinking` are handled separately from the
  main cancellation flow (`tui2.go:100-106`), but both code paths manipulate the same
  spinner state — this is a latent race if cancellation can arrive during rendering
  (unlikely with QUD marshaling, but structurally fragile).
- **No error path in `HandleEvent`.** If a QueueUpdateDraw call fails or panics, there
  is no recovery. The old TUI at least returns `tea.Cmd` which can be nil to signal
  "no update." Here, a nil `ev` is just silently dropped.

### 4.4 The conversation model — two sources of truth

This is TUI2's most significant internal quality issue. The conversation state lives in
**two data structures**:

| Variable | Type | Purpose | Location |
|---|---|---|---|
| `plainMessages` | `[]string` | Vestigial — only written, never read | `tui2.go:59` |
| `conversationLog` | `[]convItem` | Actual render source | `tui2.go:68` |

`helpers_msg.go:18-19` appends to **both** on every message:
```go
a.plainMessages = append(a.plainMessages, text)
a.conversationLog = append(a.conversationLog, convItem{...})
```

`Clear()` resets both (`tui2.go:296-299`), but every mutation site must remember to touch
both. The `plainMessages` slice is **never read anywhere** in the codebase — grep confirms
zero reads. It is dead weight that doubles the mental cost of every conversation mutation.

**Recommendation:** Delete `plainMessages` entirely. If it was intended for a backup or
debugging purpose, extract it as a `conversationLog.PlainText()` method that derives from
the canonical `convItem` slice on demand.

### 4.5 Layout architecture — tview's layered approach

TUI2 uses tview's **Pages → Flex → widget** hierarchy:

```
tview.Application
  └── Pages (app.Pages) — named pages for modal overlays
      ├── "main" — Flex (vertical)
      │   ├── Banner (TextView, row 0, fixed height)
      │   ├── Conversation Log (TextView, row 1, flex weight 1)
      │   ├── Reasoning (collapsible, row 2)
      │   ├── Tool Output (collapsible, row 3)
      │   ├── Todo (TextView, row 4, fixed height)
      │   ├── Input (InputField, row 5, fixed height)
      │   └── StatusBar (TextView, row 6, fixed height 1)
      ├── "approval" — modal overlay
      ├── "help" — modal overlay
      └── "question" — modal overlay
```

This is **significantly cleaner** than the old TUI's manual `paletteLines` / `maxModelLines`
math. tview's `Flex` and `Pages` handle resizing automatically.

**But there are issues:**

1. **The conversation log is a monolithic `TextView`.** All message content — assistant
   markdown, tool invocations, tool results — is rendered as a single tview-tagged string
   appended to a `TextView`. This means:
   - No per-message interactivity (copy, expand, retry) — everything is flat text.
   - Scroll position is managed by `TextView.ScrollToEnd()` which forces scroll on every
     append — the user can't scroll back during streaming without being yanked.
   - No virtual scrolling — the entire conversation history is in a single `TextView`
     buffer, which will degrade with very long conversations.

2. **The `messages` subpackage exists but isn't used as the primary render target.**
   `components/messages/messages.go` builds a chronological `tview.TextView` from
   `convItem` records — but `App` maintains its own conversation log separately and
   `Messages` appears to be a parallel implementation rather than the canonical one.
   This is likely a refactoring-in-progress artifact.

3. **Modal overlays are correctly implemented** via `Pages.ShowPage()` / `HidePage()`,
   which is the idiomatic tview pattern. `approval`, `help`, and `question` all use this
   correctly.

### 4.6 Color system — pragmatic but inconsistent

`colors/colors.go` defines a 256-color palette with semantic names:
```go
Dim        = "#5f5f5f"
UserText   = "#87afd7"
ToolName   = "#afd787"
SubName    = "#d7af87"
ErrorTitle = "#d75f5f"
```

These are used via tview's inline tag syntax: `[#5f5f5f]text[-]`.

**Strengths:**
- Centralized in one file — changing the palette touches one location.
- 256-color values are tview/tcell compatible (no lipgloss dependency).

**Weaknesses:**
- **Two color systems coexist.** The old TUI uses lipgloss `AdaptiveColor` + theme
  profiles (`theme.go`). TUI2 uses raw hex strings. If both TUIs remain, a color change
  requires updating two systems.
- **No light/dark mode.** Colors are hardcoded hex — they look correct on dark terminals
  but may be unreadable on light backgrounds.
- **lolcat/ is duplicated logic.** `components/banner/banner.go` calls
  `banner.LolcatRGB(charIdx)` (from `internal/banner/`), while `lolcat/` has its own
  `Rainbow()` function. Two implementations of the same rainbow effect.

### 4.7 Key binding — declarative but incomplete

`keymap.go` defines an `Action` enum with 25 actions and a `KeyMap` that maps
`tcell.Key`+`tcell.ModMask` combinations to `Action`s. The global input capture
(`globalInputCapture`) dispatches to `HandleAction`.

**Strengths:**
- Clean separation: key bindings are data, not logic.
- Vi-style mode switching (`Insert`/`Normal` modes) is architecturally sound.

**Weaknesses:**
- Of 25 declared actions, ~10 are wired (`Quit`, `Clear`, `Help`, `Compact`, `Model`,
  `Abort`, `ScrollUp`/`Down`/`Top`/`Bottom`, `SwitchFocusToInput`, `Submit`).
  The rest (`Search`, `FindNext`, `FindPrev`, `Verbose`, `CopyView`, `Login`, `Logout`,
  `FollowUp`, `Steer`, `Banner`, `NewSession`, `Interrupt`) are declared-but-unhandled
  — pressing their keys does nothing or falls through to default.
- No conflict detection — binding `Ctrl+C` to both `Quit` and `Interrupt` would be
  silently ambiguous.
- The `globalInputCapture` function at ~85 lines mixes input routing, mode switching,
  and action dispatch. As more actions are wired, this will grow into a
  `model.go:Update`-style monolith.

### 4.8 Markdown rendering — the tviewmd fork

`markdown.go` is a fork of `github.com/noborus/tviewmd`, customized for yaah's needs.
It converts Markdown to tview-tagged strings suitable for `TextView.SetText()`.

**What's customized:**
- Code blocks rendered as `[dim]` regions with background tint
- Tables rendered inline (tviewmd supports tview.Table primitives; the fork may use
  text-based fallback)
- Link handling adapted for terminal output

**Risk:** A vendored fork of an external library means:
- Upstream bug fixes must be manually merged.
- The fork is ~760 LOC with no tests — any Markdown edge case (nested lists, HTML in
  Markdown, blockquotes with code) may render incorrectly.
- The old TUI uses `glamour` (charmbracelet's mature Markdown renderer), which has
  broader format support and a larger test suite.

### 4.9 Component-by-component health check

| Component | Completeness | Notes |
|---|---|---|
| `messages/` | ⚠️ Partial | Dual implementation (App.conversationLog vs Messages primitive). Assistant and tool subpackages exist but may not be integrated. |
| `subagent/` | ✅ Good | Lifecycle tracking (spawn/running/done), depth-aware indentation, status display. Most mature component. |
| `toolblock/` | ✅ Good | Collapsible output, status tracking, clean Primitive() interface. |
| `reasoning/` | ✅ Good | Lolcat header, collapsible body, clean lifecycle (Begin/Append/End). |
| `thinking/` | ✅ Good | Atomic visibility flag, spinner frames, lolcat rendering. Clean. |
| `todo/` | ⚠️ Partial | Displays current todos but no interactivity (can't check off items). |
| `approval/` | ✅ Good | Correct Pages-based modal pattern. Accept/Deny callbacks wired. |
| `question/` | ⚠️ Partial | Modal display works, but selection flow appears incomplete — `SetCallback` exists but exit path unclear. |
| `input/` | ⚠️ Partial | History navigation works, but no multi-line support, no paste detection, no syntax highlighting. |
| `statusbar/` | ✅ Good | Simple, correct. Just a status line. |
| `infobar/` | ✅ Good | Token+cost counters. Clean. |
| `infopane/` | ❌ Stub | `UpdateInfopane(tab, content)` has `_ = tab // TODO: wire infopane tabs`. |
| `banner/` | ✅ Good | Lolcat figlet, used in header. |
| `help/` | ✅ Good | Modal overlay with keybinding table. |

### 4.10 The `Control` type — an unused abstraction?

`control.go` defines:
```go
type Control struct {
    lastWidth, lastHeight int
    // per-frame delta counters
}
```

With methods `Resize(w, h int)` and `Tick()`. This looks like it was designed for
frame-level delta tracking (terminal resize detection, animation frame counting), but
a grep shows **zero callers** of `Tick()` and only one call site of `Resize` (in
`tui2.go`'s resize handler). The `Control` struct's fields are never read outside its
own methods. Either this is a stub for future use or dead code.

### 4.11 The `App` struct — field inventory

`App` has **38 fields** spanning all concerns:

- **tview plumbing:** `App`, `Pages`, `MainFlex`, `Root`
- **Conversation state:** `conversationLog`, `plainMessages`, `conversationView`
- **Component references:** `Messages`, `SubAgent`, `ToolBlock`, `TodoComponent`,
  `Banner`, `StatusBar`, `InfoBar`, `InfoPane`
- **Overlay components:** `Approval`, `Question`, `Help`
- **Input state:** `Input`, `inputHistory`, `inputHistoryIdx`
- **Animation state:** `Reasoning`, `Thinking`, `ticker`
- **Agent integration:** `Presenter`, `AbortFunc`, `OnAbort`, `cancelAgent`
- **UI state:** `KeyMap`, `Mode`, `Width`, `Height`, `Control`
- **Misc:** `ctx`, `timerCancel`, `done`

This is a **god object**. Every feature touches `App`. The rationale is pragmatic
(tview applications need a root), but 38 fields with no grouping is a maintenance
hazard. Consider grouping into sub-structs:
- `App.ui` — tview primitives
- `App.state` — conversation, mode, dimensions
- `App.agent` — presenter, abort, cancel
- `App.components` — all component references

### 4.12 Dependency graph risk

TUI2's dependencies flow **inward** but not cleanly:

```
agent (events/)  ←  cmd/yaah/tui.go (forwarder)  →  internal/tui2
                                                       ↑
                                              internal/tui (via shared wiring.go)
```

- `tui2` imports nothing from `agent/` directly — it implements `agent.View` via the
  `HandleEvent` method, which is bound at the `cmd/yaah` wiring layer. ✅ Correct.
- `tui2` does import `internal/banner` for lolcat colors. ⚠️ This creates a dependency
  from the UI layer to a domain package — `banner` should be a pure leaf, but UI code
  importing it is a mild layering violation.
- `tui2/lolcat/` has its own rainbow implementation — the `banner` dependency could be
  eliminated by using `lolcat.Rainbow()` in both places.

---

## 5. Architectural concerns (by area)

### 5.1 ContextManager ↔ Loop coupling (medium)

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

### 5.3 Dual TUI dependency cost (medium → resolves with §3 decision)

Until one TUI is dropped, every release pays for both terminal-UI stacks. `go.mod` lines
6-14 carry `charm.land/{bubbles,bubbletea,glamour,lipgloss}/v2`, `bubblezone/v2`,
`figlet-go` **and** `tcell/v2`, `tview`. This is the single biggest dependency-budget item
and is the strongest argument for making the TUI decision soon.

### 4.4 Serve-mode globals coupling tui/tui2/serve (low)

`extraOtelProcessors`, `otelInMemoryOnly` are package globals in `cmd/yaah/serve.go` reused
by both `tui.go` and `tui2.go`. Documented in AGENTS.md as an allowed exception, but it
means `tui`, `tui2`, and `serve` share mutable package state. Acceptable now; when you
collapse to one TUI, fold these into the session.

### 5.5 View concurrency contract vs tview (low)

`view.go:8-12` states `HandleEvent` is "called sequentially from a dedicated forwarder
goroutine" and "must be safe from a single goroutine." `tui2.HandleEvent` (`events.go:10`)
is invoked from that forwarder goroutine but immediately marshals via
`t.App.QueueUpdateDraw` — which is correct because `QueueUpdateDraw` is thread-safe. So tui2
*works*, but it relies on every code path going through `QueueUpdateDraw`; a future method
that touches widget state without marshaling would silently violate the contract. Worth a
one-line comment on `TUI2.HandleEvent` asserting "all mutations must go through
QueueUpdateDraw." The bubbletea path is unambiguously safe (`prog.Send` → main loop).

### 5.6 Tool dispatch hook ordering (low)

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

## 6. Code-quality observations

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

## 7. Priority recommendations

| Priority | Action | Effort |
|---|---|---|
| **P0** | Make the TUI decision (§3). Pick one; port the missing features or delete the loser. Removes ~half the terminal-UI dependency weight and the dual-maintenance burden. | Med |
| **P1** | Refresh `docs/architecture.md` against current types (`LoopConfig`/`LoopState`/`ContextManager`/`SessionPersister`). The AGENTS.md layout is already correct — use it as the source of truth. | Sm |
| **P1** | Simplify the compaction indirection (§5.1): collapse `Loop.Compact` into one path; decide whether `ContextManager` owns `compactContext`. | Sm |
| **P2** | If keeping tui2: delete `plainMessages` duplicate; finish `UpdateInfopane`; wire search/steer/follow-up/verbose/login/logout/model-picker; add tests. | Med |
| **P3** | Sweep `i, x := i, x` loop captures (Go 1.25). | Xs |
| **P3** | Unify tool hook emission ordering (§5.6). | Sm |
| **P3** | Move build artifacts out of the repo root (`dist/`). | Xs |

---

## 8. Verdict

The codebase is in good shape — the concerns above are refinements, not structural defects.
The engine core (loop, events, pipeline, context management, tool registry, sub-agent
runner) is the strongest part; the TUI layer is where the open decisions and the duplicated
cost live.
