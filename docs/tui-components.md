# TUI Component System

How the yaah TUI (`internal/tui/`, tview-based) composes its visual elements
through focused component packages, all styled from the shared theme.

> Rewritten 2026-08-21 when the tview TUI (formerly `tui2`) became the one and
> only `yaah tui`; the previous bubbletea component system was removed.

## Layout

The app is a `Pages → Flex` hierarchy owned by the `App` struct in
`internal/tui/app.go`:

```
┌──────────────────────────────────────────────┐
│ Header (banner, provider/model info)         │
├───────────────────────────┬──────────────────┤
│ Conversation              │ Info pane        │
│ (messages / tool blocks / │ (session, context│
│  reasoning / sub-agents)  │  MCP, config)    │
│                           ├──────────────────┤
│                           │ Todo sidebar     │
├───────────────────────────┴──────────────────┤
│ Background jobs · Input (multi-line)         │
└──────────────────────────────────────────────┘
```

## Component packages

| Package | Responsibility |
|---|---|
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
| `components/error` | Error card rendering |
| `colors` | Theme tokens (single source of truth: `colors/theme.go`) |
| `lolcat` | Rainbow color tags for banner/thinking |

## Event flow

Agent events arrive on one goroutine via the broker forwarder
(`proxy.go` implements `agent.View`). Streaming token deltas bypass the queue
(direct write to a mutex-guarded pending buffer) and are flushed by a debounce
timer; every other mutation is funneled through an ordered event queue onto the
tview main goroutine (`QueueUpdate` / `QueueUpdateDraw`). Refresh renders are
debounced and instrumented (`tui.refresh.*` spans, `yaah.tui.*` metrics).

## Conventions

- Components are plain structs wrapping tview primitives; no global state.
- All colors come from `colors/theme.go` tokens — never hardcode hex values.
- One file per concern inside each package.
- Tests live next to the code (`*_test.go`), using tview's test screen.
