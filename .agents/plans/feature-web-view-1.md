---
goal: Add a yaah web command that exposes the agent as a local HTTP server with a browser-based chat interface
version: 1.1
date_created: 2026-07-28
owner: yaah
status: Planned
tags: feature, web, http, sse, view
---

# Introduction

![Status: Planned](https://img.shields.io/badge/status-Planned-blue)

The `agent.View` + `Session` interface established by the view/engine separation refactor maps cleanly to a browser-based chat interface. `TokenDeltaEvent` becomes an SSE chunk, `ToolStartEvent`/`ToolEndEvent` become activity cards, `CtrlQuestion`/`CtrlApproval` become blocking modal dialogs, and the final `DoneEvent` marks the end of a turn. No new abstractions are needed — a web driver is structurally identical to the TUI driver: create a session, implement `agent.View`, wire a control channel, run prompts.

This plan adds a `yaah web` subcommand that starts a local HTTP server. The browser receives agent events over a **Server-Sent Events (SSE)** stream and sends commands (prompts, answers, model changes) via **HTTP POST**. All implementation is in pure Go stdlib (`net/http`, `encoding/json`, `embed`) with an embedded single-page frontend using [PicoCSS v2 classless](https://picocss.com/docs/classless) (inlined CSS, ~10KB) for styling and [Alpine.js](https://alpinejs.dev/) (inlined JS, ~5KB gzipped) for reactive DOM — no npm build step, no external CDN.

## 1. Requirements & Constraints

- **REQ-001**: `yaah web` starts an HTTP server on `127.0.0.1:8080` by default. `--addr host:port` overrides the listen address.
- **REQ-002**: `GET /` returns an embedded single-page HTML/CSS/JS chat application with no external CDN dependencies.
- **REQ-003**: Agent token streaming, tool calls, sub-agent activity, and think-aloud text arrive at the browser in real-time via `GET /api/stream` (SSE, `text/event-stream`).
- **REQ-004**: `CtrlQuestion` and `CtrlApproval` are surfaced in the browser as blocking modal dialogs; the agent loop does not advance until the user responds.
- **REQ-005**: Multi-turn conversation persists across browser prompts exactly as in the REPL — the session is not torn down between turns.
- **REQ-006**: Zero new Go module dependencies. `net/http`, `encoding/json`, and `embed` cover the implementation.
- **REQ-007**: Existing REPL, one-shot, TUI, and MCP serve modes are unchanged. No modifications to `internal/` packages.
- **REQ-008**: The server prints its URL to stderr on startup and shuts down cleanly on SIGTERM or Ctrl-C, calling `sess.Close()`.

- **CON-001**: Single session per server instance. `yaah web` is a personal local tool, not a multi-user server. Concurrent prompts from different tabs are rejected with HTTP 409 Conflict.
- **CON-002**: No WebSocket. SSE for server→browser and `POST /api/action` for browser→server avoids any WS library dependency while handling the `CtrlQuestion` synchronous roundtrip cleanly.
- **CON-003**: No JS bundler, no npm, no external CDN. The entire frontend is one HTML file with inline CSS (PicoCSS classless + ~50 lines chat-specific overrides) and inline JS (Alpine.js + ~90 lines reactive logic), embedded via `//go:embed`.
- **CON-004**: The server binds to the loopback address by default. Users who pass `--addr 0.0.0.0:8080` accept the responsibility of exposing the agent to their network.
- **CON-005**: Approval roundtrip uses the same `sess.SetApproveFn` hook as the TUI, not a separate channel. The approval function blocks waiting for a browser POST.

- **PAT-001**: `sseView` implements `agent.View` via `HandleEvent`. It is created when the browser opens `/api/stream` and wired to the session via `sess.SetView(v)`. It is replaced (or nil'd) on SSE disconnect.
- **PAT-002**: SSE events are newline-delimited JSON in the standard SSE wire format: `data: <json>\n\n`.
- **PAT-003**: Correlation IDs (monotonically incrementing string, e.g. `"q1"`, `"q2"`) bind each `CtrlQuestion` or `CtrlApproval` SSE event to the corresponding browser POST response.
- **PAT-004**: `answerMap` is a `sync.Mutex`-guarded `map[string]chan<- string` tracking pending question/approval channels; `POST /api/action` writes the answer and removes the entry.

## 2. Architecture Overview

```
Browser                          yaah web server (Go)
───────                          ───────────────────
  │  GET /                            │
  │◄──── index.html (embed.FS) ───────│
  │                                   │
  │  GET /api/stream (SSE)            │
  │◄══════════════════════════════════│  long-lived connection
  │                                   │  sseView created + wired
  │                                   │  forwardCtrl goroutine started
  │                                   │
  │  POST /api/action {"type":"prompt","text":"..."} ──►│
  │                                   │  sess.RunPrompt(ctx, text) [goroutine]
  │◄── data: {"type":"token","text":"Hello"} ──────────│
  │◄── data: {"type":"tool.start","name":"bash"} ──────│
  │◄── data: {"type":"tool.end","name":"bash"} ────────│
  │◄── data: {"type":"done"} ──────────────────────────│
  │                                   │
  │  (question roundtrip)             │
  │◄── data: {"type":"ctrl.question","id":"q1",...} ───│  blocks on AnswerCh
  │  POST /api/action {"type":"answer","id":"q1","value":"yes"} ──►│
  │                                   │  AnswerCh ← "yes", loop continues
  │◄── data: {"type":"done"} ──────────────────────────│
```

### Wire Protocol

**Server → Browser (SSE events)**

| `type` field | Source event | Payload fields |
|---|---|---|
| `token` | `TokenDeltaEvent` | `text` |
| `thinking` | `ThinkingEvent` | `text` |
| `flush` | `FlushEvent` | `content` |
| `tool.start` | `ToolStartEvent` | `name`, `args` |
| `tool.end` | `ToolEndEvent` | `name`, `args`, `result`, `ms`, `error` |
| `subagent.start` | `SubAgentStartEvent` | `role`, `model`, `prompt` |
| `subagent.end` | `SubAgentEndEvent` | `role`, `ms`, `error` |
| `done` | `DoneEvent` | `response`, `error`, `context_tokens`, `context_window` |
| `ctrl.status` | `CtrlStatus` | `text` |
| `ctrl.error` | `CtrlError` | `error` |
| `ctrl.question` | `CtrlQuestion` | `id`, `header`, `question`, `options`, `multi` |
| `ctrl.approval` | `CtrlApproval` | `id`, `name`, `args` |
| `ctrl.todos` | `CtrlTodos` | `items` |
| `ctrl.context` | `CtrlContextInfo` | `tokens`, `window` |
| `ctrl.models` | `CtrlModelList` | `models`, `providers` |

**Browser → Server (`POST /api/action`)**

| `type` field | Effect |
|---|---|
| `prompt` | Call `sess.RunPrompt(ctx, text)` in a new goroutine; 409 if already running |
| `abort` | Cancel the in-flight prompt's context |
| `answer` | Write `value` to the channel in `answerMap[id]`; remove entry |
| `compact` | Call `sess.Compact()` |
| `model` | Call `sess.SetModel(provider, model)` |

### Frontend CSS: PicoCSS classless

The UI uses [PicoCSS v2 classless](https://picocss.com/docs/classless) for all standard web concerns. This eliminates ~200 lines of custom CSS that would otherwise be needed for:

- **Layout**: `<main>` acts as a centered container automatically
- **Dark mode**: `data-theme="dark"` on `<html>` — automatic, respects `prefers-color-scheme`
- **Typography**: Responsive type scale across 6 breakpoints
- **Forms**: `<textarea>`, `<button>`, `<select>` styled automatically
- **Modals**: `<dialog>` + `<article>` with header/footer — overlay, backdrop blur, animations, scroll-lock (`.modal-is-open`) built in
- **Loading**: `aria-busy="true"` on buttons/divs shows an animated spinner

Only ~50 lines of chat-specific CSS remain: message bubbles (`.msg.user` right-aligned, `.msg.assistant` left-aligned), `#messages` scroll container, `#tool-activity` strip, and `#status-bar` positioning.

| Concern | Vanilla CSS | PicoCSS |
|---|---|---|
| Layout | Custom flexbox + centered container | `<main>` auto-container |
| Dark mode | `prefers-color-scheme` media queries | `data-theme` attribute, automatic |
| Forms | Custom textarea + button styles | Styled automatically |
| Modals | Custom overlay + positioning + animations | `<dialog>` + `<article>`, built-in |
| Spinner | Custom CSS keyframes | `aria-busy` attribute |
| Typography | Manual font sizing, spacing | Responsive type scale |
| Responsive | Custom media queries | 6 built-in breakpoints |

### Frontend JS: Alpine.js reactive DOM

The UI uses [Alpine.js](https://alpinejs.dev/) (~5KB gzipped, zero build step) for reactive DOM binding. This eliminates ~110 lines of imperative JS that would otherwise be needed for manual DOM query/update code. Alpine provides:

- **`x-data`**: Single reactive state object (`{ messages: [], running: false, ... }`) — replaces manual `document.getElementById()` queries and ad-hoc JS state variables
- **`x-model="prompt"`**: Two-way data binding on the textarea — no `input.value` reads
- **`x-bind:disabled` / `x-bind:aria-busy`**: Reactive attribute binding on buttons — loading state in one attribute
- **`x-show` / `x-if`**: Conditional visibility — replaces `element.style.display = 'none'`
- **`x-html`**: Render markdown content into message bubbles
- **`x-for="msg in messages"`**: Reactive message list — `messages.push(...)` auto-updates the DOM, no manual `createElement`/`appendChild`
- **`@click`, `@submit.prevent`, `@keydown.enter.prevent`**: Event handling — replaces `addEventListener`
- **`$refs.modal.showModal()` / `.close()`**: Direct access to the `<dialog>` element
- **`$dispatch` / `x-on`**: Custom events for cross-component communication (status bar updates from SSE handler)

**Example — what Alpine replaces:**

```js
// Vanilla JS: 7 lines of manual DOM manipulation
const btn = document.getElementById('send-btn');
btn.disabled = true;
btn.setAttribute('aria-busy', 'true');
const input = document.getElementById('prompt');
input.disabled = true;
input.value = '';
const msgDiv = document.createElement('div');
msgDiv.className = 'msg assistant';
document.getElementById('messages').appendChild(msgDiv);
```

```html
<!-- Alpine: 3 lines, declarative -->
<textarea x-model="prompt" :disabled="running"></textarea>
<button :disabled="running" :aria-busy="running">Send</button>
<template x-for="msg in messages">
  <div :class="'msg ' + msg.role" x-html="msg.content"></div>
</template>
```

**Line count impact:**

| | Vanilla JS | Alpine.js |
|---|---|---|
| JS logic | ~200 lines | ~90 lines |
| HTML + directives | ~60 lines | ~70 lines |
| CSS (Pico + chat) | ~50 lines | ~50 lines |
| **Total frontend** | **~310 lines** | **~210 lines** |

Alpine.js code (minified) is inlined in a `<script>` block alongside the application logic — no separate file, no CDN fetch.

## 3. New Files

| File | Purpose |
|---|---|
| `cmd/yaah/web.go` | `webCmd` cobra command, HTTP server, request handlers, session lifecycle |
| `cmd/yaah/web_view.go` | `sseView` (implements `agent.View`), `answerMap`, SSE helpers, wire-format structs |
| `cmd/yaah/web/index.html` | Embedded single-page chat UI — PicoCSS classless (inlined) + Alpine.js (inlined) + ~90 lines reactive logic, ~210 lines total |

Modified: `cmd/yaah/root.go` — register `webCmd` in `init()`.

## 4. Implementation Steps

### Phase 1 — Embedded frontend (`cmd/yaah/web/index.html`)

The frontend is a self-contained HTML file. PicoCSS v2 classless provides layout, dark mode, forms, modals, and spinners. Alpine.js provides reactive DOM binding — a single `x-data` state object drives all UI updates; no manual DOM queries. The JS logic (~90 lines) handles the SSE `EventSource` connection, appends events into the Alpine reactive array, and dispatches `fetch` actions. Message rendering, modal state, input enable/disable, and status bar are all bound declaratively via Alpine directives.

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-001 | Create `cmd/yaah/web/` directory and `index.html`. Self-contained HTML file. `<style>` block: PicoCSS classless (inlined, minified) + ~50 lines chat-specific CSS (message bubbles `.msg.user` / `.msg.assistant`, scroll container, tool-activity strip). Body: Alpine `x-data="{ messages: [], running: false, toolActivity: [], statusText: '', connected: false }"` on `<body>`. Structure: `<main>` with `<template x-for="msg in messages">` message list, tool-activity strip (`x-show="toolActivity.length"`), `<form @submit.prevent="send">` with `<textarea x-model="prompt" :disabled="running">` + `<button :disabled="running" :aria-busy="running">`. `<dialog x-ref="modal">` with `<article>`. `<footer>` status bar (`x-text="statusText"`). `<script>` block: Alpine.js (inlined, minified) + ~90 lines app logic. | | |
| TASK-002 | Implement Alpine `init()`: open `EventSource` to `/api/stream`. Set `connected = true`. On `message`: parse JSON, dispatch to handler functions that mutate the Alpine `$data` (e.g. `this.messages.push(...)`, `this.toolActivity.push(...)`, `this.running = true`). On `error`: set `connected = false`, let `EventSource` auto-reconnect. | | |
| TASK-003 | Implement `token` handler: if no current assistant message in `messages` with `streaming: true`, push one (`{ role: 'assistant', content: '', streaming: true }`). Append `data.text` to `content`. | | |
| TASK-004 | Implement `tool.start` handler: push `{ name, pending: true }` to `toolActivity` array. `tool.end` handler: remove entry by name. | | |
| TASK-005 | Implement `done` handler: set `streaming: false` on the last assistant message, set `running = false`. If `data.error`, push an error message. | | |
| TASK-006 | Implement `ctrl.question` handler: populate modal via `$refs` — set title text, build radio/checkbox `<input>` elements in `#modal-body`, confirm button in `#modal-footer`. Call `$refs.modal.showModal()`. On confirm: `fetch('/api/action', {type:'answer', id, value})`, close modal. | | |
| TASK-007 | Implement `ctrl.approval` handler: populate modal with tool `name` + preformatted `args`, Allow/Deny buttons. Allow → `fetch` with `value: 'true'`, Deny → `value: 'false'`. | | |
| TASK-008 | Implement `ctrl.status` / `ctrl.error` / `ctrl.todos` / `ctrl.context` handlers: write into Alpine `statusText` reactive string. Status bar updates automatically via `x-text`. | | |
| TASK-009 | Implement `send()` function: POST `{type:'prompt', text: this.prompt}`. On success: `running = true`, `prompt = ''`. On 409: set `statusText` for 3 seconds. | | |
| TASK-010 | Handle SSE reconnect: `connected` boolean drives a visual indicator (`<div x-show="!connected">`). `EventSource` auto-reconnects; on `open` event, set `connected = true`. | | |

### Phase 2 — SSE view (`cmd/yaah/web_view.go`)

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-011 | Define `sseWireEvent` struct with a `Type` string field and all optional payload fields (use `omitempty`). This is the single JSON envelope for all server→browser events. | | |
| TASK-012 | Implement `sseWrite(w http.ResponseWriter, data []byte) error`. Writes `"data: "` + data + `"\n\n"` to `w`, then calls `w.(http.Flusher).Flush()`. Returns error if flush fails. | | |
| TASK-013 | Implement `sseView` struct: `{ w http.ResponseWriter; mu sync.Mutex }`. `HandleEvent` marshals each `agent.Event` subtype to an `sseWireEvent` and calls `sseWrite`. The `mu` guard prevents concurrent SSE writes from the agent's control-channel goroutine and the main event path. | | |
| TASK-014 | In `sseView.HandleEvent`, handle all sealed event types: `TokenDeltaEvent`, `ThinkingEvent`, `FlushEvent`, `ToolStartEvent`, `ToolEndEvent`, `SubAgentStartEvent`, `SubAgentEndEvent`, `DoneEvent`. Add a compile-time exhaustiveness check comment listing all types. | | |
| TASK-015 | Implement `answerMap` struct: `{ mu sync.Mutex; pending map[string]chan<- string }`. Methods: `register(id string, ch chan<- string)`, `deliver(id, value string) bool`, `cancel(id string)`. | | |

### Phase 3 — Control channel handler (`cmd/yaah/web.go` or `web_view.go`)

The `forwardCtrl` goroutine reads from the session's `controlCh` and serializes each `types.CtrlMsg` to the SSE stream. For `CtrlQuestion` and `CtrlApproval` it registers a one-shot channel in `answerMap` before writing the SSE event, so the browser POST can unblock the waiting agent.

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-016 | Implement `idGen` — a simple monotonically incrementing counter (`sync/atomic.Int64`) that returns strings like `"q1"`, `"q2"`. Avoids a UUID dependency. | | |
| TASK-017 | Implement `forwardCtrl(ctx context.Context, ch <-chan types.CtrlMsg, v *sseView, am *answerMap)`. Loop: `select { case msg, ok := <-ch: ... case <-ctx.Done(): return }`. Switch on concrete `types.CtrlMsg` types and marshal to `sseWireEvent`. | | |
| TASK-018 | For `*types.CtrlQuestion` in `forwardCtrl`: generate a correlation ID, create `ch := make(chan string, 1)`, call `am.register(id, ch)`, write the `ctrl.question` SSE event, then start a goroutine that reads from `ch` and writes to `msg.AnswerCh`. | | |
| TASK-019 | For `*types.CtrlApproval` in `forwardCtrl`: use the same pattern but the answer goroutine parses the string value as `"true"`/`"false"` and writes to `msg.ApproveCh chan<- bool`. | | |
| TASK-020 | For `*types.CtrlDone` in `forwardCtrl`: return from the goroutine (do not write SSE; the agent `DoneEvent` already signals completion). | | |

### Phase 4 — HTTP handlers and server (`cmd/yaah/web.go`)

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-021 | Define `webServer` struct: `{ sess Session; view *sseView; ctrlCh chan types.CtrlMsg; am *answerMap; promptCtxCancel context.CancelFunc; running atomic.Bool; mu sync.Mutex }`. | | |
| TASK-022 | Implement `GET /api/stream` handler. Sets `Content-Type: text/event-stream`, `Cache-Control: no-cache`, `X-Accel-Buffering: no`. Creates `sseView`, wires via `sess.SetView(v)`. Creates `controlCh`, wires via `sess.SetCtrlCh(controlCh)`. Starts `forwardCtrl` goroutine. Blocks until request context is done (client disconnected). On disconnect: `sess.SetView(agent.NoopView{})`, close `controlCh`. | | |
| TASK-023 | Implement `POST /api/action` handler. Decodes the JSON body into `actionRequest{Type, Text, ID, Value, Provider, Model string}`. Dispatches on `Type`: | | |
| | `"prompt"`: if `ws.running.Swap(true)` returns true, respond 409; else start goroutine `runWebPrompt(ctx, ws, text)` | | |
| | `"abort"`: if `ws.promptCtxCancel != nil`, call it | | |
| | `"answer"`: call `ws.am.deliver(id, value)`; if not found, 404 | | |
| | `"compact"`: call `ws.sess.Compact()` | | |
| | `"model"`: call `ws.sess.SetModel(provider, model)` | | |
| TASK-024 | Implement `runWebPrompt(ctx context.Context, ws *webServer, text string)`. Creates a child context, stores cancel in `ws.promptCtxCancel`. Calls `ws.sess.RunPrompt(ctx, text)`. On return: clears cancel, sets `ws.running.Store(false)`. | | |
| TASK-025 | Wire `sess.SetApproveFn` in `webServer` initialization. The function: generates a correlation ID, creates `ch := make(chan string, 1)`, registers in `ws.am`, writes a `ctrl.approval` SSE event via the current view, blocks `select { case ans := <-ch; case <-ctx.Done() }`, parses `"true"` → `true`. If the view is nil (client disconnected), deny by default. | | |
| TASK-026 | Implement `webCmd` cobra command. Flags: `--addr string` (default `"127.0.0.1:8080"`). `RunE`: calls `newAgentSession()`, builds a `*webServer`, builds an `http.ServeMux`, registers routes (`/` → `http.FileServer(http.FS(webFS))`, `/api/stream`, `/api/action`), prints `"yaah web listening on http://<addr>"` to stderr, calls `http.ListenAndServe` with graceful shutdown on SIGTERM/SIGINT. | | |
| TASK-027 | Add `//go:embed web` and `var webFS embed.FS` declaration above the handler code in `cmd/yaah/web.go`. | | |

### Phase 5 — Command registration and integration

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-028 | In `cmd/yaah/root.go` `init()`, add `rootCmd.AddCommand(webCmd)` alongside `serveCmd`, `versionCmd`, etc. | | |
| TASK-029 | Run `go build ./...` — verify zero compile errors. Confirm the embedded `web/index.html` is included in the binary via `//go:embed`. | | |
| TASK-030 | Run `go vet ./...` and `staticcheck ./...` — verify clean. | | |

### Phase 6 — Smoke tests and validation

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-031 | Manual smoke: `go run . web`, open `http://127.0.0.1:8080` in browser, send a prompt, verify token streaming in the UI. | | |
| TASK-032 | Tool activity test: send a prompt that triggers a `bash` or `read` tool call. Verify tool indicator appears during execution and disappears on completion. | | |
| TASK-033 | Question modal test: trigger a prompt that causes the agent to call the `question` tool. Verify the modal appears, options are clickable, selecting an option resumes the agent. | | |
| TASK-034 | Approval modal test: run with `--approval ask`, send a prompt that calls a tool. Verify the approval modal shows `name` + `args`, Allow/Deny works, denied tool returns an error. | | |
| TASK-035 | Multi-turn test: send three sequential prompts. Verify conversation history is maintained (agent references earlier turns). | | |
| TASK-036 | Abort test: send a long-running prompt, click Stop (sends `{"type":"abort"}`), verify the agent stops and the UI re-enables input. | | |
| TASK-037 | Disconnect/reconnect test: open stream, send a prompt, close and reopen the browser tab. Verify the session persists and a new prompt continues the conversation. | | |
| TASK-038 | Concurrent-prompt rejection test: open two tabs, send prompts simultaneously. Verify the second prompt receives HTTP 409. | | |

## 5. Open Questions

1. **Markdown rendering**: Should assistant messages be rendered as markdown in the browser (using a micro-library like `marked.js` embedded in the HTML), or as preformatted plain text for MVP?
2. **Conversation history on reconnect**: When the browser reconnects to `/api/stream`, should the server replay prior messages (stored in the session's `messages` slice) as a `history` SSE event? Or is the "continue conversation silently" behavior sufficient?
3. **Code block copy buttons**: Low-cost UX win for a coding assistant — is it in scope for the initial version?
4. **Context-length warning**: `CtrlContextInfo` provides token count and window size. Should the UI show a visual warning when the session is near the context limit?
5. **Authentication**: For users who want to expose the server beyond loopback (e.g. a home server), a simple `--token` flag that checks a `Bearer` header on all API endpoints would add minimal friction and reasonable security. Out of scope for MVP but worth noting.
