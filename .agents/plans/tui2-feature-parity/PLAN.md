---
name: tui2-feature-parity
description: Close the feature gaps between tui2 (tview) and the production tui (bubbletea), starting with code reorganization (one file per concern) and ending with full parity.
status: in-progress
---

# tui2 Feature Parity Plan

## Goal

Bring the tui2 (tview/tcell) implementation to full feature parity with the
production tui (bubbletea), while improving code organization — one file per
concern, no monoliths.

## Current state

**Phase 1 (Code reorganization) — COMPLETE ✅**

tui2 codebase has been reorganized with:
- Split monolithic `tui2.go` (~700 → 140 lines) into focused files
- Single source of truth: `conversationLog` with inline block references
- Duplicate state eliminated: removed `toolBlocks`, `reasoningBlocks`, `subagentBlocks` slices
- New files: `proxy.go`, `usage.go`, `blocks.go`, `components/error/error.go`
- 50+ unit tests across `proxy_test.go`, `usage_test.go`, `blocks_test.go`, `error_test.go`, `approval_test.go`, `agent_safety_test.go`

**Phase 1.5 (Threading fixes) — COMPLETE ✅**

Critical threading and UX issues discovered through SigNoz OTel trace analysis:

- **Approval never wired**: `SetApproveFn` was only called in `web.go`. When bash
  required approval, `approveTool()` fell through to `bufio.Scanner(os.Stdin)` —
  but tview captures stdin for keypresses, so it blocked the agent goroutine forever.
  Fixed by wiring `SetApproveFn` in `cmd/yaah/tui2.go` using the `CtrlApproval` →
  control channel → `ShowApproval` modal pattern.

- **tview `QueueUpdate()` blocks the forwarder**: Every `HandleEvent` call blocked
  the forwarder goroutine in `BrokerView.forward()`. During an LLM stream at 50+
  tokens/sec, the 100-slot tview updates channel flooded and the main thread was
  pinned in `draw()` between callbacks. Fixed by writing TokenDeltaEvent directly
  to `pendingTokens` from the forwarder goroutine via mutex (zero `QueueUpdate`
  calls during streaming). All other event types use `go QueueUpdate(Draw)`.

- **Goroutine dispatch invisible in traces**: When a tool goroutine blocked before
  reaching `Registry.Execute()`, no OTel span was exported because `defer span.End()`
  only runs when the goroutine returns. Fixed by adding `RecordToolGoroutine`
  checkpoint spans with explicit `span.End()` (no defer).

- **Diagnostic tracing**: Added `RecordTurnResponse`, `RecordToolDispatch`,
  `RecordToolDispatchDone`, and `RecordToolGoroutine` short-lived spans for
  crash/block diagnostics. Added `tui2.refresh` span with timing breakdown.

Feature completeness: ~85% vs tui. Cleaner layout (tview flex/grid) and plugin
system are in place. Cumulative usage/cost tracking and error overlay are
implemented. Approval flow properly wired with modal. Streaming is non-blocking.
Remaining gaps: some keybindings, search, multi-line input.

## Progress

| Phase | Status | Notes |
|-------|--------|-------|
| Phase 1: Code reorganization | ✅ COMPLETE | Monolith split, duplicate state eliminated, `events.go` → `proxy.go` |
| Phase 1.5: Threading fixes | ✅ COMPLETE | Async dispatch, approval wiring, direct token write, goroutine diagnostics |
| Phase 1.6: Nonblocking architecture hardening | ✅ COMPLETE | queue/lifecycle hardening, bounded queue, coalescing, observability, and tests |
| Phase 2: Usage & cost tracking | ✅ COMPLETE | Implemented in `usage.go` with model pricing table |
| Phase 3: Error overlay | ✅ COMPLETE | Implemented in `components/error/` with auto-dismiss and timer management |
| Phase 4: Keybinding completion | ⏳ TODO | 10 actions need handlers |
| Phase 5: Component health | ⏳ TODO | Messages dual-implementation, question selection, multi-line input |
| Phase 6: Polish | ⏳ TODO | Theme consolidation, lolcat refactor, scroll improvements |
| Phase 7: Testing | 🔄 IN PROGRESS | Unit tests added; integration tests still TODO |

### Latest Commit

```
5afa9fe fix(tui2): wire approval function, async token dispatch, goroutine diagnostics
```

- Approval wired via `SetApproveFn` → `CtrlApproval` → `ShowApproval` modal
- TokenDeltaEvent writes directly to `pendingTokens` via mutex (zero QueueUpdate during streaming)
- `RecordToolGoroutine` diagnostic spans with explicit `span.End()`
- `ShowApprovalFn` override field for testability
- Tests: 3 approval/continue, 4 approveTool integration, 6 async dispatch

## Phase 1: Code reorganization (foundation)

### 1.1 Split monolithic `tui2.go` (~700 lines)

**Done.** Current file layout:

```
internal/tui2/
├── tui2.go              # TUI2 struct + constructor
├── run.go               # Run(), control loop, debounce timer
├── input.go             # Input capture, global input, mode switching
├── state.go             # Search state
├── proxy.go             # agent.View bridge: HandleEvent + HandleContextInfo
├── commands.go          # Command palette dispatch
├── control.go           # Control message handling (CtrlQuestion, CtrlApproval, etc.)
├── modals.go            # Modal wrappers (ShowQuestion, ShowApproval, ShowHelp, etc.)
├── followup.go          # Follow-up / cancel-reprompt logic
├── scroll.go            # Scroll-preserving render + refreshMessages
├── thinking.go          # Thinking indicator lifecycle
├── blocks.go            # All block management (reasoning, tool, sub-agent)
├── usage.go             # Cumulative token/cost tracking
├── helpers_msg.go       # Message rendering helpers
├── markdown.go          # tviewmd fork
├── panes.go             # Right-pane update methods
├── banner.go            # Banner display
├── colors/              # Theme colors (authoritative source)
├── lolcat/              # Rainbow text rendering
└── components/
    ├── messages/
    ├── subagent/
    ├── toolblock/
    ├── reasoning/
    ├── thinking/
    ├── todo/
    ├── approval/
    ├── question/
    ├── input/
    ├── infopane/
    ├── banner/
    ├── help/
    ├── error/
    ├── contextinfo/
    ├── mcpinfo/
    ├── sessioninfo/
    ├── command/
    ├── modal/
    ├── modelpicker/
    ├── backgroundjobs/
    └── spinner/
```

### 1.2 Group struct fields into sub-structs

**Deferred.** Grouping `t.ui.conversation`, `t.state.mode`, etc. would require
updating ~60+ field accesses across all files with low value relative to the risk.

### 1.3 Eliminate duplicated state

**Done.** Removed `toolBlocks`, `reasoningBlocks`, `subagentBlocks` slices.
`conversationLog` is the single source of truth. All block operations now
iterate `conversationLog` directly via inline `convItem` references.

## Phase 1.5: Threading & event dispatch fixes

### Async event dispatch

tview's `QueueUpdate()` is fundamentally blocking — it writes to a 100-slot
channel and blocks on a `done` channel until the main thread finishes the
callback + `draw()`. The forwarder goroutine in `BrokerView.forward()` calls
`HandleEvent()` synchronously. Every event type blocked the forwarder.

**Fixes applied:**

| Event type | Before | After | Rationale |
|---|---|---|---|
| TokenDeltaEvent | `QueueUpdate` (sync) | Direct write via mutex | Eliminates all QueueUpdate calls during streaming. Debounce timer handles rendering. |
| All other events | `QueueUpdate(Draw)` (sync) | `go QueueUpdate(Draw)` | Forwarder returns immediately. tview serializes callbacks on main thread. |
| Control loop messages | `QueueUpdateDraw` (sync) | Same (sync) | Must preserve message ordering for approval question/answer pairs. |
| Debounce timer | `QueueUpdateDraw` (sync) | Same (sync) | Must preserve tick pacing. |

### Approval function wiring

`SetApproveFn` was only called in `cmd/yaah/web.go`. For tui2, it was nil,
causing `approveTool()` to fall through to a blocking `os.Stdin` read.
tview captures stdin for keypress handling, so `scanner.Scan()` blocked
the agent goroutine forever. The TUI never showed an approval modal.

**Fix:** Wire `SetApproveFn` in `runTUI2()` using the `CtrlApproval` →
control channel → `ShowApproval` modal pattern, identical to the existing
question tool wiring.

### Goroutine diagnostic spans

Tool goroutine checkpoints (`spawned`, `acquire_concurrency`, `publish_start`,
`published`) use explicit `span.End()` (no defer) so spans are exported even
when the goroutine blocks. Turn-level diagnostic spans (`turn.response`,
`turn.dispatch_tools`, `turn.dispatch_done`) are short-lived child spans
that survive parent crashes.

## Phase 1.6: Nonblocking architecture hardening

⏳ TODO:

### Why this phase exists

Phase 1.5 solved the major forwarder stall by moving token streaming off
`QueueUpdate`. However, the current design still has three structural risks:

- **Unsafe token buffer mutation**: `flushPendingTokens()` resets
  `pendingTokens` once under lock and again outside the lock.
- **Unbounded goroutine fan-out**: non-token events use `go QueueUpdate...`
  per event, which can accumulate blocked goroutines if draw throughput drops.
- **Always-on tick scheduling**: the 100ms `time.Tick` loop continuously calls
  `QueueUpdateDraw`, even when idle.

### Design target

Keep the broker forwarder nonblocking while making UI scheduling bounded,
predictable, and race-free.

### 1.6.1 Fix correctness first (race + lifecycle)

✅ COMPLETE (implemented and validated)

Files:
- `internal/tui2/proxy.go`
- `internal/tui2/run.go`

Actions:
- Remove the second `pendingTokens.Reset()` in `flushPendingTokens()` so all
  `pendingTokens` mutation occurs under `tokenMu`.
- Replace `time.Tick` with a stoppable `time.NewTicker` managed by `Run()/Stop()`
  lifecycle.
- Add a `done` channel for background loops (`control` and `debounce`) so
  shutdown is explicit and tests can assert clean exit.

Acceptance criteria:
- `go test -race ./internal/tui2/...` reports no race involving
  `pendingTokens`.
- No goroutine leak in repeated start/stop tests.

Validation run:
- `go test ./internal/tui2/...` ✅
- `go test -race ./internal/tui2/...` ✅
- `go build ./...` ✅

### 1.6.2 Replace goroutine-per-event with bounded UI event queue

✅ COMPLETE (implemented and validated)

Files:
- `internal/tui2/proxy.go`
- `internal/tui2/run.go`
- (new) `internal/tui2/event_queue.go`

Actions:
- Introduce a bounded `uiEventCh` owned by TUI2 (single consumer worker).
- `HandleEvent` should enqueue lightweight closures/messages instead of
  spawning a new goroutine per non-token event.
- Worker serially invokes `QueueUpdate`/`QueueUpdateDraw`, preserving order and
  naturally applying backpressure to `uiEventCh` instead of runtime goroutine
  growth.
- Define overflow behavior explicitly (drop/coalesce low-priority events; never
  drop terminal events like `DoneEvent`).

Acceptance criteria:
- Under synthetic high event rate, goroutine count remains bounded.
- Forwarder stays responsive (no long `HandleEvent` stalls).

Implementation notes:
- Added `internal/tui2/event_queue.go` with bounded queue (`uiEventCh`) and
  single consumer worker.
- `HandleEvent` now routes non-token events through queue helpers instead of
  spawning a goroutine per event.
- Non-critical events drop on queue overflow; critical events wait briefly and
  then fall back to direct async dispatch so they are not dropped.

Validation run:
- `go test ./internal/tui2/...` ✅
- `go test -race ./internal/tui2/...` ✅
- `go build ./...` ✅

### 1.6.3 Edge-triggered refresh scheduling

✅ COMPLETE (implemented and validated)

Files:
- `internal/tui2/scroll.go`
- `internal/tui2/run.go`

Actions:
- Convert periodic polling refresh to edge-triggered scheduling:
  - `markDirty()` requests a refresh only on transition false→true.
  - UI worker batches multiple updates into one `refreshMessages()` call.
- Keep a low-frequency safety tick only if needed (debug fallback), disabled by
  default.

Acceptance criteria:
- Idle UI performs zero redraw callbacks.
- During streaming/tool bursts, redraw frequency is capped and stable.

Implementation notes:
- Removed periodic debounce polling from `Run()`.
- `markDirty()` now schedules refresh only on false→true dirty transitions.
- Added queued refresh guard to prevent duplicate refresh callbacks while one
  is already queued.

Validation run:
- `go test ./internal/tui2/...` ✅
- `go test -race ./internal/tui2/...` ✅
- `go build ./...` ✅

### 1.6.4 Coalescing policy (throughput without loss of meaning)

✅ COMPLETE (implemented and validated)

Files:
- `internal/tui2/event_queue.go`
- `internal/tui2/proxy.go`

Actions:
- Coalesce repetitive transient events:
  - Multiple `ThinkingEvent` updates collapse to latest label.
  - Multiple context info updates collapse to latest snapshot.
- Preserve strict ordering for semantic events:
  - `ToolStart`/`ToolEnd`
  - `SubAgentStart`/`SubAgentEnd`
  - `DoneEvent`

Acceptance criteria:
- No malformed block lifecycle in conversation log.
- Info/thinking panes remain current without flooding queue.

Implementation notes:
- Added coalesced scheduling helpers for high-frequency updates:
  - `ThinkingEvent` collapses to latest label.
  - context info updates collapse to latest token/window snapshot.
- Preserved strict ordering for semantic lifecycle events (`ToolStart/End`,
  `SubAgentStart/End`, `DoneEvent`) by keeping those on critical queue paths.

Validation run:
- `go test ./internal/tui2/...` ✅
- `go test -race ./internal/tui2/...` ✅
- `go build ./...` ✅

### 1.6.5 Observability and test gates

✅ COMPLETE (implemented and validated)

Files:
- `internal/tui2/proxy_test.go`
- (new) `internal/tui2/event_queue_test.go`
- `internal/tui2/run_test.go`

Actions:
- Add tests for:
  - bounded queue behavior and overflow policy
  - event ordering guarantees for start/end/done
  - start/stop lifecycle with no goroutine leaks
  - race-focused token flush regression
- Add OTel counters/gauges:
  - queue depth
  - dropped/coalesced events by type
  - refresh cadence and duration

Progress update:
- Added `internal/tui2/event_queue_test.go` covering:
  - non-critical overflow drop behavior
  - critical overflow fallback behavior
  - thinking coalescing to latest
  - context-info coalescing to latest
- Existing lifecycle/race validations continue to pass.
- Added queue ordering and lifecycle start/stop tests.
- Added OTel-backed queue/refresh metrics for:
  - queue depth samples
  - dropped/fallback/coalesced queue event outcomes
  - refresh duration and cadence

Validation run:
- `go test ./internal/tui2/...` ✅
- `go test -race ./internal/tui2/...` ✅
- `go build ./...` ✅

Acceptance criteria:
- `go test ./internal/tui2/...` green.
- `go test -race ./internal/tui2/...` green.
- `go build ./...` green.

## Phase 2: Cumulative usage & cost tracking

**Done.** Implemented in `usage.go`:

- `cumulativeUsage` struct tracking prompt/completion tokens across turns
- `calculateCost()` with longest-match prefix lookup in model pricing table
- Displayed in infopane as "Prompt: N / Completion: N / Cost: $X.XXXX"
- Annotated "(at current model rates)" to indicate cost is per-model
- `resetUsage()` called on conversation clear

## Phase 3: Error overlay component

**Done.** Implemented in `components/error/`:

- `Manager` with error stack and count display
- Auto-dismiss after 5 seconds for non-retryable errors (via `time.AfterFunc`)
- Timer properly stopped on manual dismiss / dismiss-all
- `Dismiss()` and `DismissAll()` methods with render refresh
- `ShowRetryable` for errors that offer a retry path

**Not yet wired** into `HandleEvent`. The error component is ready but
no agent events trigger it yet.

## Phase 4: Keybinding completion

⏳ TODO — 10 actions need handlers:

| Action | Binding | Handler |
|---|---|---|
| `Search` | `/` in normal mode | Open search bar, filter conversation |
| `FindNext` | `n` | Jump to next search match |
| `FindPrev` | `N` | Jump to previous search match |
| `Verbose` | `Ctrl+V` | Toggle verbose output |
| `CopyView` | `Ctrl+Y` | Copy visible text to clipboard |
| `FollowUp` | `Ctrl+Enter` during streaming | Cancel and reprompt |
| `Steer` | `Ctrl+T` | Open steer prompt |
| `Banner` | `Ctrl+B` | Toggle banner visibility |
| `NewSession` | `Ctrl+N` | Start a new conversation |
| `Interrupt` | `Ctrl+C` (double-tap) | Force-quit agent loop |

## Phase 5: Component health

⏳ TODO:

### 5.1 Fix the `messages/` dual-implementation

The `components/messages/` subpackage exists but `TUI2` maintains `conversationLog`
separately. After Phase 1.3 (single source of truth), ensure `messages.Format`
is THE renderer and `conversationLog` is THE data source.

### 5.2 Complete question selection flow

Complete the question modal's selection:
- Arrow keys to navigate options
- `Space` to toggle (multi-select)
- `Enter` to confirm
- `Esc` to cancel
- Visual feedback on selected items (highlight color)

### 5.3 Add multi-line input support

Enhance `components/input/`:
- `Alt+Enter` for newline
- Visual height grows up to 5 lines, then scrolls
- Paste detection (large paste opens editor)

## Phase 6: Polish & cross-cutting

⏳ TODO:

### 6.1 Unify color system

- Delete `colors/colors.go` — merge constants into `theme.go`
- Delete `lolcat/` — use `internal/banner` package directly
- Add light/dark mode detection via `$TERM_BG` or config
- Define semantic color roles: `ColorUser`, `ColorAssistant`, `ColorTool`, `ColorError`, etc.

### 6.2 Add scroll-preserving render

Done (`scroll.go`). Tracks `userScrolled` flag. Only auto-scrolls to end when
user hasn't scrolled up. `G`/`gg` keys for bottom/top.

### 6.3 Add virtual scrolling consideration

For very long conversations, a single `TextView` holding all text will degrade.
File a follow-up issue: investigate `tview.Table` or custom `tview.TextView` with
windowed content for >1000 message conversations.

### 6.4 Markdown rendering audit

Audit the `tviewmd` fork against glamour's test suite:
- Nested lists
- HTML-in-markdown
- Blockquotes with code blocks
- Image alt text fallback
- Link reference definitions
- Table alignment

File bugs for any rendering gaps found.

## Phase 7: Testing

### 7.1 Unit tests

**Done.** Current test coverage:

| File | Tests | Covers |
|---|---|---|
| `proxy_test.go` | 6 | Async dispatch, token ordering, counter increments |
| `blocks_test.go` | 14 | Block lifecycle, add/end/toggle/blink |
| `usage_test.go` | 6 | Cost calculation, prefix matching, accumulation |
| `approval_test.go` | 3 | CtrlApproval/CtrlContinue round-trip |
| `error_test.go` | 14 | Error stack, dismiss/dismiss-all, timer management |
| `agent_safety_test.go` | 4 | ApproveFn propagation, deny, arg abbreviation |

Component-level tests also exist for `backgroundjobs`, `command`, `contextinfo`,
`infopane`, `mcpinfo`, `messages`, `reasoning`, `sessioninfo`, `subagent`,
`thinking`, `todo`, `toolblock`.

### 7.2 Integration tests

⏳ TODO:
- Run tui2 headless, send event sequence, verify render output
- Resize: 80×24 → 120×40 → 80×24, verify layout correct
- Long conversation: 500 messages, verify no OOM or degradation

## Execution order

```
Phase 1 (organization)  ✅ COMPLETE
  ↓
Phase 1.5 (threading)   ✅ COMPLETE
  ↓
Phase 1.6 (nonblocking hardening)  ← NEXT
  ↓
Phase 2 (cumulative usage/cost)   ✅ COMPLETE
  ↓
Phase 3 (error overlay)           ✅ COMPLETE
  ↓
Phase 4 (keybinding completion)
  ↓
Phase 5 (component health)
  ↓
Phase 6 (polish)
  ↓
Phase 7 (testing)                 ← IN PROGRESS (unit tests done)
```

Each phase produces a compilable, runnable TUI. No phase should leave the build broken.

## Risk: Don't break tui while fixing tui2

Both TUIs share the forwarder goroutine in `BrokerView` (`internal/agent/view.go`).
Changes to the `agent.View` interface or event types affect both. Rule:
- **Never change the `agent.Event` sealed interface** as part of this plan.
- **Never change `cmd/yaah/tui.go`** wiring unless adding a new event type
  that both TUIs handle (add a no-op case in the old tui).
- Run `go build ./...` after every phase.
