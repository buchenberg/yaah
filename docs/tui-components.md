# TUI Component System

How the yaah TUI (`internal/tui/`, tview-based) composes its visual elements
through focused component packages, all styled from the shared theme.

> Rewritten 2026-08-21 when the tview TUI (formerly `tui2`) became the one and
> only `yaah tui`; the previous bubbletea component system was removed.
> Updated 2026-08-25: activity line, prompt redesign, error dialog.

## Layout

The app is a `Pages → Flex` hierarchy owned by the `App` struct in
`internal/tui/app.go`:

```
┌──────────────────────────────────────────────┐
│ Header (banner, provider/model info)         │
├───────────────────────────┬──────────────────┤
│ Prompt echo (sticky)      │ Info pane        │
│ Conversation              │ (session, context│
│ (messages / tool blocks / │  MCP, config)    │
│  reasoning / sub-agents)  ├──────────────────┤
│                           │ Todo sidebar     │
├───────────────────────────┴──────────────────┤
│ Activity line (spinner + state label)        │
├──────────────────────────────────────────────┤
│ Prompt input (bordered TextArea, pink border)│
└──────────────────────────────────────────────┘
```

The bottom two rows are **dedicated** — always reserved, never resized:

- **Activity line** (1 row): `tvxwidgets.Spinner` + state label. Always present;
  collapsed to blank when idle. Animation driven by a 100ms ticker goroutine.
- **Prompt input** (3 rows): bordered `TextArea` with pink border that switches
  to a double border on focus. Placeholder includes the `❯` glyph.

The **prompt echo** is a 1-row `TextView` pinned at the top of the messages
column showing the current user prompt so it never scrolls away.

## Activity state machine

The activity line (`components/activity`) tracks the agent's current phase
through 9 states with a depth-1 restore stack for overlay states:

| State | Trigger | Label |
|---|---|---|
| `Idle` | DoneEvent, error, abort, stop | *(blank)* |
| `Thinking` | OnSubmit (turn start) | Thinking… |
| `Reasoning` | ThinkingEvent | Reasoning + preview |
| `Responding` | First TokenDelta | Responding |
| `Tool` | ToolStartEvent | Running \<name\>… |
| `SubAgent` | SubAgentStartEvent | Sub-agent \<role\> ×N |
| `Compacting` | CompactionStartedEvent | Compacting 12.3K→4.0K |
| `Approving` | Approval/Continue modal | Awaiting approval… |
| `Asking` | Question modal | Awaiting input… |

Overlay states (Tool, SubAgent, Compacting, Approving, Asking) snapshot the
previous state and restore it when the overlay ends. The spinner animates
continuously during any non-Idle state; during Compacting it is replaced by
a `tvxwidgets.ActivityModeGauge`.

## Error dialog

Errors from `control.Error` and `DoneEvent.Error` are displayed using
`tvxwidgets.MessageDialog` (the `ErrorDailog` variant — note upstream
misspelling). The dialog auto-centers, caps at 30 lines / 1200 chars, and
restores focus to the input on dismiss. The conversation log line is appended
separately as the durable record.

## Component packages

| Package | Responsibility |
|---|---|
| `components/activity` | Activity line: state machine, spinner/gauge, label rendering |
| `components/errdialog` | Error dialog using tvxwidgets.MessageDialog |
| `components/messages` | Conversation view composition; delegates to the block components |
| `components/toolblock` | Collapsible tool-call card: header summary, duration, result body |
| `components/reasoning` | Collapsible thinking/reasoning block with lolcat styling |
| `components/subagent` | Sub-agent lifecycle card: role, task, model, duration, expandable result |
| `components/approval` | Modal for dangerous-tool approval requests |
| `components/question` | Modal for `question` tool prompts with selectable options |
| `components/command` | Colon-command parser + Ctrl+P command palette |
| `components/modal` | Shared modal wrapper for consistent sizing/centering |
| `components/modelpicker` | Live-filtered provider/model picker |
| `components/help` | Keybinding reference overlay |
| `components/banner` | Figlet+lolcat banner rendering |
| `components/infopane` | Info pane container; embeds the four sections below |
| `components/sessioninfo` | Session ID, cwd, uptime section |
| `components/contextinfo` | Context window usage section |
| `components/mcpinfo` | Connected MCP servers section |
| `components/backgroundjobs` | Running/completed background sub-agent jobs |
| `components/todo` | Todo list sidebar |
| `components/input` | Prompt input (bordered TextArea with pink border) |
| `colors` | Theme tokens (single source of truth: `colors/theme.go`) |
| `lolcat` | Rainbow color tags for banner/reasoning |

## Event flow

Agent events arrive on one goroutine via the broker forwarder
(`proxy.go` implements `agent.View`). Streaming token deltas bypass the queue
(direct write to a mutex-guarded pending buffer) and are flushed by a debounce
timer; every other mutation is funneled through an ordered event queue onto the
tview main goroutine (`QueueUpdate` / `QueueUpdateDraw`). Refresh renders are
debounced and instrumented (`tui.refresh.*` spans, `yaah.tui.*` metrics).

The activity line is updated via `setActivity` / `restoreActivity` helpers
called from `proxy.go` (agent events) and `control.go` (control messages).
The 100ms animation ticker uses the droppable non-critical queue path — dropped
ticks only stutter the animation, never block.

## Conventions

- Components are plain structs wrapping tview primitives; no global state.
- All colors come from `colors/theme.go` tokens — never hardcode hex values.
- One file per concern inside each package.
- Tests live next to the code (`*_test.go`), using tview's test screen.
- Visibility is toggled via `Flex.ResizeItem(p, 0, 0)` — tview v0.42.0 has
  no `Box.Show/Hide`.
- tvxwidgets spinner/gauge are not focusable — never pass `focus=true` in
  `Flex.AddItem` or `Grid.AddItem` for them.
