# Web UI

How `yaah web` delivers a browser-based chat interface with SSE streaming,
collapsible tool results, and interactive approval/question modals.

## Overview

`yaah web` starts an HTTP server that serves a single-page application at
`http://127.0.0.1:8080` (by default). The agent session persists across
prompts for the lifetime of the server, and all events stream to the
browser via Server-Sent Events (SSE).

```
Browser (SPA)                          Go server (web.go)
─────────────                         ──────────────────
Alpine.js + Pico CSS                  net/http + embed.FS
     │                                       │
     ├── GET / ──────────────────────────────┤ serve index.html
     ├── GET /api/stream ────────────────────┤ SSE event stream
     │   (EventSource)                       │   (sseView.HandleEvent)
     ├── POST /api/action ───────────────────┤ prompt / abort / answer / compact / model
     │   (fetch JSON)                        │   (webServer.handleAction)
     └──                                    ┘
```

## Architecture

Files involved:

| File | Purpose |
|---|---|
| `cmd/yaah/web.go` | HTTP server, SSE stream handler, action endpoint, prompt runner, approval flow |
| `cmd/yaah/web_view.go` | SSE view adapter (`sseView`), wire event types (`sseWireEvent`), `forwardCtrl` goroutine, answer map, tool summary engine |
| `cmd/yaah/web/index.html` | Single-page app: Alpine.js reactive data, Pico CSS styling, Marked.js markdown rendering |
| `cmd/yaah/web/marked.min.js` | Marked.js (markdown-to-HTML) |
| `cmd/yaah/web/alpine.min.js` | Alpine.js (reactive data binding) |
| `cmd/yaah/web/pico.min.css` | Pico CSS (semantic light/dark styling) |

All web UI assets are embedded in the yaah binary via `//go:embed web`.

### Server (`web.go`)

`webServer` is the HTTP layer:

- **`handleStream`**: Accepts one SSE client at a time. Creates an `sseView`,
  wires it to the session's event broker, and starts `forwardCtrl` for
  control-plane messages (questions, approvals, todos). Blocks until the
  client disconnects.

- **`handleAction`**: Accepts JSON POST commands from the browser:
  - `prompt` — start a new agent turn (HTTP 409 if one is already running)
  - `abort` — cancel the running agent
  - `answer` — respond to a question/approval modal
  - `compact` — trigger context compaction
  - `model` — switch provider/model

- **`runPrompt`**: Runs the agent in a background goroutine. Sets up
  a cancellable context and calls `Session.RunPrompt`.

### SSE View (`web_view.go`)

`sseView` adapts the browser's EventSource connection to the agent's `View`
interface. It receives typed events from the agent loop and serializes them
as JSON lines (`data: {...}\n\n`).

#### Wire event types

All events use a single `sseWireEvent` struct with a `type` discriminator:

| Event type | Direction | Purpose |
|---|---|---|
| `token` | Agent → Browser | Streaming token delta |
| `thinking` | Agent → Browser | Reasoning/thinking text |
| `flush` | Agent → Browser | Streaming segment complete; commit to message list |
| `tool.start` | Agent → Browser | Tool execution begins (name, args, tool_id, summary) |
| `tool.end` | Agent → Browser | Tool execution completes (name, result, ms, error, summary) |
| `subagent.start` | Agent → Browser | Sub-agent dispatch begins |
| `subagent.end` | Agent → Browser | Sub-agent dispatch completes |
| `done` | Agent → Browser | Agent loop finishes (response, error, context stats) |
| `ctrl.status` | Agent → Browser | Ephemeral status message (compaction progress, etc.) |
| `ctrl.error` | Agent → Browser | Error messages |
| `ctrl.question` | Agent → Browser → Agent | Interactive question modal |
| `ctrl.approval` | Agent → Browser → Agent | Tool approval modal |
| `ctrl.todos` | Agent → Browser | Todo list update |
| `ctrl.context` | Agent → Browser | Context window fill percentage |
| `ctrl.models` | Agent → Browser | Available model list |
| `ctrl.header` | Agent → Browser | Header metadata: provider, model, and MCP server list |

#### Tool summary engine

The server computes one-line tool summaries that match the TUI's
`internal/tui/tool_component.go` patterns:

| Tool | Summary format | Example |
|---|---|---|
| `grep` | `N matches in N files` | `12 matches in 3 files` |
| `glob` | `N files` | `7 files` |
| `ls` | `N entries` | `14 entries` |
| `bash` | first line of output (≤60 chars) | `go build ./...` |
| `read` | `read <file> (N,N chars)` | `read main.go (1,234 chars)` |
| `write` | `wrote <file> (N,N chars)` | `wrote config.go (567 chars)` |
| `edit` | `edited <file>` | `edited handler.go` |
| `delete` | `deleted <file>` | `deleted temp.txt` |
| `http` | URL from args | `https://api.example.com` |
| `webfetch` | URL from args | `https://docs.example.com` |
| `git` | action from args | `commit` |
| `replace` | `replaced in <file>` | `replaced in utils.go` |
| `spawn_subagent` | `sub-agent: <role> — <specialty> · <description>` | `sub-agent: Developer — Full-stack engineer · Fix auth bug` |
| _(default)_ | first line of output (≤80 chars) | — |

Two summary functions are used at different points:

- **`toolStartSummary(name, args)`** — runs before execution, based only on
  tool name and arguments (preview). E.g. `"bash — go test ./..."`.
- **`toolSummary(name, args, result)`** — runs after execution, based on
  the result content. E.g. `"0 matches"` (empty grep output).

### Control-plane messages (`forwardCtrl`)

The `forwardCtrl` goroutine bridges the session's control channel to the SSE
stream. It handles:

- **Questions** (`CtrlQuestion`): Generates a unique answer ID, registers a
  channel in the `answerMap`, and sends the question to the browser. The
  browser's answer is routed back through `POST /api/action` → `answerMap.deliver`.
- **Approvals** (`CtrlApproval`): Same pattern as questions, but the answer
  is `"true"` or `"false"`.
- **Todos** (`CtrlTodos`): Serialized as `wireTodo` items and sent to the
  browser for inline rendering.
- **Context** (`CtrlContextInfo`): Token/Window ratio displayed as a
  percentage in the footer.
- **Model list** (`CtrlModelList`): Available models from configured
  providers (currently rendered as chat messages).

### Answer map

`answerMap` is the correlation layer between SSE-delivered questions and
HTTP-delivered answers. When the agent asks a question, `forwardCtrl`
generates an ID, registers a channel, and sends the question to the browser
with that ID. When the user answers, `handleAction` calls `am.deliver(id, value)`,
which routes the answer back to the waiting goroutine.

```
Agent asks question
  → CtrlQuestion published
    → forwardCtrl: id = "q1", register ch, send SSE event
      → Browser: show modal
        → User clicks Confirm
          → POST /api/action {type:"answer", id:"q1", value:"option1"}
            → handleAction: am.deliver("q1", "option1")
              → ch ← "option1"
                → CtrlQuestion.AnswerCh ← "option1"
                  → Agent receives answer
```

On SSE disconnect, `am.cancel(id)` cleans up pending answers so the server
goroutines don't leak.

## Frontend (`index.html`)

The frontend is a single HTML file with no build step, using three CDN
libraries:

| Library | Version | Purpose |
|---|---|---|
| [Pico CSS](https://picocss.com) | 2.x | Semantic light/dark styling, no class names needed |
| [Alpine.js](https://alpinejs.dev) | 3.x | Reactive data binding with `x-data`, `x-show`, `x-for`, `x-html` |
| [Marked](https://marked.js.org) | latest | Markdown-to-HTML rendering with GFM support |

### Reactive state

The Alpine.js component (`x-data="yaah"`) manages all UI state:

| State | Type | Purpose |
|---|---|---|
| `msgs` | `Message[]` | All messages in the chat log (user, assistant, error, tool) |
| `stream` | `string` | Live streaming markdown being assembled |
| `think` | `string` | Accumulated reasoning/thinking text |
| `live` | `bool` | Agent is currently running a prompt |
| `on` | `bool` | SSE connection is alive |
| `status` | `string` | Footer status text |
| `ctx` | `number\|null` | Context window fill percentage |
| `modal` | `object` | Current modal state (question or approval) |
| `_tools` | `object` | Map of running tool messages keyed by `tool_id` |

### Message types in the chat log

Each message object has `id`, `role`, and role-specific fields:

| Role | Fields | Rendered as |
|---|---|---|
| `user` | `html` | Right-aligned primary-color bubble |
| `asst` | `html` | Left-aligned card-background bubble |
| `error` | `html` | Left-aligned red bubble |
| `tool` | `name`, `args`, `summary`, `result`, `duration`, `error`, `running`, `expanded` | Inline collapsible tool card |

### Tool message lifecycle

Tools are rendered inline in the chat log, not in a separate strip:

1. **`tool.start`** → Creates a new tool message with `running: true`,
   icon `⏳`, and the pre-execution summary. Registers it in `_tools[tool_id]`
   for later update. The icon pulses via CSS animation.

2. **Streaming** → `token` and `thinking` events continue to arrive; tool
   messages remain in the log below the streaming content.

3. **`tool.end`** → Looks up the message in `_tools[tool_id]`, sets
   `running: false`, populates `result`, `duration` (formatted with `fmtMs`),
   `error`, and the post-execution `summary`. Icon changes to `✓` (success)
   or `✗` (error). On error, the tool auto-expands to show the result.

4. **Click** → Toggles `expanded` on/off, revealing or hiding the bordered
   result code block (max height 280px, scrollable).

### Duration formatting

```js
function fmtMs(ms) {
  if (ms < 1000) return ms + 'ms'
  if (ms < 60000) return (ms / 1000).toFixed(1) + 's'
  const m = Math.floor(ms / 60000)
  const s = ((ms % 60000) / 1000).toFixed(0)
  return m + 'm ' + s + 's'
}
```

### Modals

The browser supports two modal types via the native `<dialog>` element:

- **Question**: Radio buttons (single-select) or checkboxes (multi-select),
  with optional description text per option.
- **Approval**: Tool name + arguments display with Allow/Deny buttons.

### Input

The textarea auto-resizes from 1 to 8 lines. Enter sends; Shift+Enter
inserts a newline. The Send button is disabled while `live` is true; a
Stop button appears instead.

### Footer

Left side: connection dot (green = connected, red = disconnected) + status text.
Right side: context window fill percentage.

## Limitations

- **Single SSE client**: Only one browser tab can connect at a time.
  The `sseView` is a singleton on the `webServer`.
- **No chat search**: Relies on the browser's built-in Ctrl+F.
- **No copy button**: No clipboard integration (TUI has `Ctrl+Y`).
- **No follow-up/steer**: Cannot send additional input while the agent is
  running (TUI supports mid-turn messages).

## Compared to the TUI

The TUI (`yaah tui`) is the reference implementation. The web UI tracks it
closely on core functionality but trails on advanced features:

| Feature | TUI | Web UI |
|---|---|---|
| Tool results (collapsible) | ✅ | ✅ |
| Tool summaries | ✅ | ✅ |
| Thinking/reasoning display | ✅ | ✅ |
| Questions (radio/checkbox) | ✅ | ✅ |
| Tool approvals | ✅ | ✅ |
| Context fill display | ✅ (fraction bar) | ✅ (percentage) |
| Todo list | ✅ (persistent table) | ✅ (inline messages) |
| Model switching UI | ✅ (searchable palette) | ✅ (command palette) |
| Commands (`:compact`, `:clear`, etc.) | ✅ | ✅ (command palette) |
| Chat search (`/`) | ✅ | ✗ |
| Copy to clipboard | ✅ (`Ctrl+Y`) | ✗ |
| Sub-agent visual bracketing | ✅ (`╭─` / `╰─`) | ✗ |
| Header with provider/model | ✅ | ✅ |
| Status bar (CWD, msg count) | ✅ | ✗ |
| Theme switching | ✅ (5 themes) | ✗ (browser light/dark only) |
| MCP server status | ✅ | ✅ (badge) |
| Follow-up / steer injection | ✅ | ✗ |
| Ephemeral feedback messages | ✅ | ✗ |
| Mid-turn compaction button | ✅ (`:compact`) | ✅ (palette) |
| Multiple clients | N/A | ✗ (single SSE) |

## Extending

- **New event type**: add a case in `sseView.HandleEvent` → add
  corresponding handling in `onEvt()` in the frontend.
- **New tool summary**: add a case in `toolSummary()` in `web_view.go`.
- **New action**: add a case in `webServer.handleAction` and a matching
  `post()` call in the frontend.
- **New modal type**: add a `<template x-if="modal.type === '...'">`
  block in the dialog, update `onEvt` to set the modal, and add a
  corresponding action handler.
