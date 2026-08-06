---
name: tui2-task-pane-separation
description: Move task list rendering out of InfoPane and into the dedicated TodoPane in TUI2
status: completed
---

## Goal

Clean up the TUI2 right-side panels and header:
- **InfoPane**: session info (provider, model, version — moved from header), context tokens (accurate, real-time), MCP server status
- **TodoPane**: live task list (moved out of InfoPane)
- **Header**: full-width banner only (no more provider/model in the corner)
- **InfoPane styling**: very light muted pink background

## Current State

### Header
```
┌──────────────────────────────────────────────┐
│  Banner (left 2/3)  │ Provider: openai/gpt...│
│  (goat ASCII art)   │ Model: gpt-4o-2024...  │
│                      │ Agent: yaah v0.7.0    │
└──────────────────────────────────────────────┘
```
- `provider.Build()` renders provider/model/version, placed in header cols 1-2 (right 1/3)
- Same info is duplicated in InfoPane's Session section via `sessioninfo.Format()` — but only as a static placeholder

### Right column
```
┌─── InfoPane (3 parts) ───┐
│ Session: [static fakes]  │
│ Context: [static fakes]  │  ← background: tcell.Color236 (dark gray-blue)
│ MCP:     [static fakes]  │
├─── TodoPane (1 part) ────┤
│ No tasks.                │  ← NEVER updated; real tasks rendered inside InfoPane
└──────────────────────────┘
```
- `infopane.Build()` fills InfoPane with static placeholders
- `renderInfoPane()` **overwrites** InfoPane text with Context + Tasks only (session/MCP lost)
- `CtrlTodos` and `CtrlContextInfo` both call `renderInfoPane()`, mixing concerns
- `TodoPane` exists in the layout but never receives live data

### Context token updates (gaps)
| Path | Updates fields? | Calls `renderInfoPane()`? |
|------|----------------|--------------------------|
| `CtrlContextInfo` (control.go:43) | ✅ | ✅ |
| `DoneEvent` (events.go) | ✅ | ✅ |
| `HandleContextInfo` (events.go:132) | ✅ tokens only | ❌ |
| Mid-streaming token deltas | ❌ | ❌ |

`HandleContextInfo` updates `t.contextTokens` but never refreshes the InfoPane.

## Changes

### 1. Provider/model → InfoPane Session section (`provider/`, `sessioninfo/`)

- Remove `provider.Build()` from the header — no more provider info in the top-right
- Header becomes: Banner full-width across all columns, full header height
- `renderInfoPane()` builds the Session section with live data:
  - Provider: from `t.lastProvider`
  - Model: from `t.lastModel`
  - Agent version: from build-time `version` constant
- `sessioninfo.Format(provider, model, version)` called with real values
- Delete or deprecate `provider/` component if nothing else uses it

### 2. Control handler split (`internal/tui2/control.go`)

- `CtrlTodos` → call a new `t.renderTodoPane()` that writes `todoview.FormatList(t.todoItems)` into `t.TodoPane`
- `CtrlContextInfo` → call `t.renderInfoPane()` — session, context, MCP only (no tasks)
- `CtrlFallback` → also call `renderInfoPane()` so provider/model change updates the Session section
- `renderInfoPane()` → remove Tasks section; add live Session section with provider/model/version

### 3. TodoPane update method (`internal/tui2/control.go`)

New method `renderTodoPane()`:
- Takes `t.todoItems` and passes them through `todoview.FormatList()`
- Sets the result on `t.TodoPane` via `SetText()`
- Updates border title with item count (e.g. `"Tasks (3)"`)

### 4. Header layout simplification (`internal/tui2/tui2.go`)

- `Header` grid: Banner spans all columns (0-2), all rows
- Remove `t.ProviderInfo` from the header grid
- Remove `provider.Build()` call or keep for non-header use if needed
- Grid columns simplify to `-1` (just one full-width column)

### 5. Context token real-time accuracy (`events.go` + `control.go`)

- `HandleContextInfo()` → after setting `t.contextTokens`, call `t.renderInfoPane()` and statusbar update
- Ensure `DoneEvent` path also lines up with same logic (already mostly does)
- Consider whether `TokenDeltaEvent` should also trigger a periodic context refresh (estimate: ~1 token consumed per output token). Could use a debounced update every N deltas.
- Goal: InfoPane context display stays accurate throughout the session, not just at Done events

### 6. InfoPane background: light muted pink (`infopane/infopane.go`)

- Change `tv.SetBackgroundColor(tcell.Color236)` → light muted pink
- Exact color: `tcell.NewHexColor(0xFFE8E8)` — very light muted pink, or `tcell.NewHexColor(0xFFF0F0)` for even lighter
- May also want to set matching border color for cohesion
- Typical light muted pink hexes: `#FFE4E1` (MistyRose), `#FFE8E8`, `#FFF0F0`

## Files touched

| File | Change |
|------|--------|
| `internal/tui2/tui2.go` | Remove ProviderInfo from header; Banner full-width; remove provider import |
| `internal/tui2/control.go` | Split `CtrlTodos`/`CtrlContextInfo`; new `renderTodoPane()`; slim `renderInfoPane()` with live session; `CtrlFallback` calls `renderInfoPane()` |
| `internal/tui2/events.go` | `HandleContextInfo` calls `renderInfoPane()` + statusbar; optional mid-streaming token refresh |
| `internal/tui2/components/todo/todo.go` | May need a `SetItems()` helper |
| `internal/tui2/components/sessioninfo/sessioninfo.go` | Ensure `Format(provider, model, version)` signature for live data |
| `internal/tui2/components/provider/provider.go` | Deprecate/delete if no other callers |
| `internal/tui2/components/infopane/infopane.go` | Background color: `0xFFE8E8`; simplify initial placeholder |

## Non-goals

- Not adding live MCP status data — stays placeholder for now
- Not changing the right-column proportions (3:1 InfoPane:TodoPane)
- Not changing the banner component
