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
- Duplicate state eliminated: removed `plainMessages`, `toolBlocks`, `reasoningBlocks`, `subagentBlocks`
- New files: `proxy.go`, `usage.go`, `blocks.go`, `components/error/error.go`
- 29+ new unit tests covering usage, blocks, and error components
- All bugs fixed (prefix matching, empty tool results, goroutine leaks)

Feature completeness: ~75-80% vs tui. Cleaner layout (tview flex/grid) and plugin
system are in place. Cumulative usage/cost tracking and error overlay are now
implemented. Remaining gaps: some keybindings, search, multi-line input.

## Progress

| Phase | Status | Notes |
|-------|--------|-------|
| Phase 1: Code reorganization | ✅ COMPLETE | Monolith split, duplicate state eliminated, new files created, 29+ unit tests added |
| Phase 2: Usage & cost tracking | ✅ COMPLETE | Implemented in `usage.go` with model pricing table |
| Phase 3: Error overlay | ✅ COMPLETE | Implemented in `components/error/` with auto-dismiss |
| Phase 4: Keybinding completion | ⏳ TODO | 10 actions need handlers |
| Phase 5: Component health | ⏳ TODO | Messages dual-implementation, question selection, multi-line input |
| Phase 6: Polish | ⏳ TODO | Theme consolidation, lolcat refactor, scroll improvements |
| Phase 7: Testing | ✅ COMPLETE | Unit tests added; integration tests still TODO |

### Latest Commit

```
4535c85 tui2: complete Phase 1 reorganization with bug fixes and unit tests
```

- Bug fixes: prefix matching randomness, empty tool result as failure, goroutine leak in error auto-dismiss, model drift in cost estimate
- New files: proxy.go, usage.go, blocks.go, components/error/error.go
- Tests: usage_test.go (6), blocks_test.go (14), error/error_test.go (14)

## Phase 1: Code reorganization (foundation)

### 1.1 Split monolithic `tui2.go` (~700 lines, 38-field `App` struct)

Current file does: App struct definition, constructor, all helpers, input routing,
mode switching, action dispatch, keybinding, banner init, resize handling, streaming
delta handling, follow-up logic. This must be split:

**Target file layout** (one file per concern):

```
internal/tui2/
├── tui2.go              # App struct + constructor ONLY (no helpers)
├── run.go               # Run(), ticker lifecycle, shutdown
├── view.go              # View() layout assembly (pages/flex/grid)
├── input.go             # inputCapture, globalInputCapture, mode switching, focus
├── state.go             # conversationLog, mode, dims — all pure state
├── proxy.go             # agent.View bridge: HandleEvent + QueueUpdateDispatch
├── commands.go          # command palette (:foo) dispatch table
├── events.go            # event handling (keep, clean up duplicate-state writes)
├── control.go           # resize/tick tracking (keep existing)
├── keymap.go            # key bindings (keep)
├── markdown.go          # tviewmd fork (keep)
├── theme.go             # CENTRALIZE all colors here from colors/colors.go
├── usage.go             # cumulative token/cost tracking (NEW — see Phase 3)
├── helpers_msg.go       # message rendering helpers (keep, refactor)
├── helpers_tool.go      # tool result helpers (keep, refactor)
├── helpers_subagent.go  # sub-agent helpers (keep, refactor)
├── followup.go          # follow-up/cancel-reprompt logic (extract from tui2.go)
├── scroll.go            # scroll-preserving render logic (extract from tui2.go)
├── colors/              # delete; merge into theme.go
├── lolcat/              # delete; use internal/banner package directly
├── components/          # (keep existing, add missing)
│   ├── messages/
│   ├── subagent/
│   ├── toolblock/
│   ├── reasoning/
│   ├── thinking/
│   ├── todo/
│   ├── approval/
│   ├── question/
│   ├── input/
│   ├── infopane/
│   ├── banner/
│   ├── help/
│   ├── error/           # NEW: error overlay modal
│   └── spinner/
└── plugins/             # (keep)
```

### 1.2 Group `App` struct fields into sub-structs

```go
type App struct {
    // tview plumbing
    app    *tview.Application
    pages  *tview.Pages
    flex   *tview.Flex

    // UI components (grouped)
    ui struct {
        messages   *messages.Messages
        subagent   *subagent.SubAgent
        toolblock  *toolblock.ToolBlock
        reasoning  *reasoning.Reasoning
        thinking   *thinking.Thinking
        todo       *todo.Todo
        banner     *banner.Banner
        infopane   *infopane.InfoPane
        input      *input.Input
        modal      *modal.Manager // approval, question, help, error — all modals
    }

    // UI state
    state struct {
        mode      Mode // Insert, Normal
        width     int
        height    int
        scrolled  bool // user manually scrolled up
    }

    // Agent integration
    agent struct {
        presenter agent.Presenter
        abortFunc context.CancelFunc
        onAbort   func()
        cancelFn  context.CancelFunc
    }

    // Conversation — single source of truth
    conversation struct {
        items []convItem // THE canonical list; all renderers derive from this
    }

    // Input history
    history struct {
        items []string
        index int
    }
}
```

### 1.3 Eliminate duplicated state

- **Delete `plainMessages`.** It is never read — dead code.
- **Ensure `conversationLog` is the SINGLE source of truth.** All render passes
  (conversation view, tool blocks, reasoning blocks, subagent blocks) derive from it.
  No separate `toolBlocks`/`reasoningBlocks`/`subagentBlocks` slices.
- **Add a `conversation.RenderKey()` method** that returns a hash of the current state
  so the view can skip re-rendering when nothing changed (replaces `lastRenderKey`).

## Phase 2: Cumulative usage & cost tracking

### 2.1 Add cumulative usage tracking to the info pane

The `DoneEvent` already carries a `.Usage` field (`types.Usage`) with
`PromptTokens`, `CompletionTokens`, and `TotalTokens`. tui2 currently ignores it
— only `ContextTokens` and `ContextWindow` are captured.

**New file `usage.go`:**
- Accumulate `PromptTokens`, `CompletionTokens` across all turns
- Derive cost from token counts × model pricing table
- Store in `App` as `cumulativeUsage types.Usage` and `cumulativeCost float64`
- Update on each `DoneEvent`

**Wire into info pane:**
- Add a "Usage" section to the infopane showing:
  - Total prompt tokens
  - Total completion tokens
  - Estimated cost (USD)
- Update via `t.renderInfoPane()` call which already fires on DoneEvent

### 2.2 Model pricing table

Add a lookup in `usage.go`:

```go
var modelPrices = map[string]struct{ input, output float64 }{
    "claude-sonnet-4-20250514": {3.00, 15.00},  // per 1M tokens
    "claude-opus-4-20250514":  {15.00, 75.00},
    "gpt-4o":                   {2.50, 10.00},
    "gpt-4o-mini":              {0.15, 0.60},
    // ... extend as needed
}
```

Prices come from provider docs; display is an estimate only.

## Phase 3: Error overlay component

### 3.1 Create `components/error/` 

Replace the ephemeral `app.QueueUpdateDraw` error text (currently inline in
the event handler) with a proper modal overlay:

- Display error title + detail with error color styling
- [Dismiss] button (`Esc` or `Enter`)
- [Retry] button if the error is retryable
- [Copy] button to copy error text to clipboard
- Auto-dismiss after 5 seconds for transient errors
- Stack multiple errors (show count: "3 errors — [View] [Dismiss All]")

### 3.2 Wire into HandleEvent

Replace the inline error handling in `events.go` with `t.ui.modal.ShowError(...)`.

## Phase 4: Keybinding completion

Wire the 10 declared-but-unhandled actions in `keymap.go`:

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

### Add keybinding conflict detection

In `keymap.go` init, validate that no two actions share the same key combination.
Panic at startup if a conflict is found — fail fast.

## Phase 5: Component health

### 5.1 Fix the `messages/` dual-implementation

The `components/messages/` subpackage exists but `App` maintains `conversationLog`
separately. After Phase 1.3 (single source of truth), ensure `messages.Messages`
is THE renderer and `App.conversation.items` is THE data source.

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

### 6.1 Unify color system

- Delete `colors/colors.go` — merge constants into `theme.go`
- Delete `lolcat/` — use `internal/banner` package directly
- Add light/dark mode detection via `$TERM_BG` or config
- Define semantic color roles: `ColorUser`, `ColorAssistant`, `ColorTool`, `ColorError`, etc.

### 6.2 Add scroll-preserving render (extract from tui2.go into `scroll.go`)

Currently `TextView.ScrollToEnd()` is called on every append, preventing the user from
scrolling back during streaming. Fix:
- Track `state.scrolled` flag (set when user scrolls up, cleared on `ScrollToEnd` action)
- Only auto-scroll when `!state.scrolled`
- Add `G`/`gg` keys for bottom/top

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

- Theme: color constants are valid hex, no duplicates
- Keymap: no conflicting bindings, all actions reachable
- State: `conversation` append/render/clear cycle
- Usage: accumulation math, cost calculation

### 7.2 Integration tests

- Run tui2 headless, send event sequence, verify render output
- Resize: 80×24 → 120×40 → 80×24, verify layout correct
- Long conversation: 500 messages, verify no OOM or degradation

## Execution order

```
Phase 1 (organization)  ← START HERE
  ↓
Phase 2 (cumulative usage/cost)   ← small, low-risk
  ↓
Phase 3 (error overlay)           ← small, isolated
  ↓
Phase 4 (keybinding completion)   ← independent tasks
  ↓                              can fan out across sub-agents
Phase 5 (component health)
  ↓
Phase 6 (polish)
  ↓
Phase 7 (testing)
```

Each phase produces a compilable, runnable TUI. No phase should leave the build broken.

## Risk: Don't break tui while fixing tui2

Both TUIs share the forwarder goroutine in `cmd/yaah/tui.go`. Changes to the
`agent.View` interface or event types affect both. Rule:
- **Never change the `agent.Event` sealed interface** as part of this plan.
- **Never change `cmd/yaah/tui.go`** wiring unless adding a new event type
  that both TUIs handle (add a no-op case in the old tui).
- Run `go build ./...` after every phase.
