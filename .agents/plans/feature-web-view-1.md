---
goal: Add a yaah web command that exposes the agent as a local HTTP server with a browser-based chat interface
version: 1.0
date_created: 2026-07-28
owner: yaah
status: Planned
tags: feature, web, http, sse, view
---

# Introduction

![Status: Planned](https://img.shields.io/badge/status-Planned-blue)

The `agent.View` + `Session` interface established by the view/engine separation refactor maps cleanly to a browser-based chat interface. `TokenDeltaEvent` becomes an SSE chunk, `ToolStartEvent`/`ToolEndEvent` become activity cards, `CtrlQuestion`/`CtrlApproval` become blocking modal dialogs, and the final `DoneEvent` marks the end of a turn. No new abstractions are needed — a web driver is structurally identical to the TUI driver: create a session, implement `agent.View`, wire a control channel, run prompts.

This plan adds a `yaah web` subcommand that starts a local HTTP server. The browser receives agent events over a **Server-Sent Events (SSE)** stream and sends commands (prompts, answers, model changes) via **HTTP POST**. All implementation is in pure Go stdlib (`net/http`, `encoding/json`, `embed`) with an embedded vanilla-JS single-page frontend — no new Go module dependencies, no npm build step.

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
- **CON-003**: No JS bundler, no npm, no external CDN. The entire frontend is one HTML file with inline CSS and JS, embedded via `//go:embed`.
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

## 3. New Files

| File | Purpose |
|---|---|
| `cmd/yaah/web.go` | `webCmd` cobra command, HTTP server, request handlers, session lifecycle |
| `cmd/yaah/web_view.go` | `sseView` (implements `agent.View`), `answerMap`, SSE helpers, wire-format structs |
| `cmd/yaah/web/index.html` | Embedded single-page chat UI (vanilla HTML/CSS/JS, ~300 lines) |

Modified: `cmd/yaah/root.go` — register `webCmd` in `init()`.

## 4. Implementation Steps

### Phase 1 — Embedded frontend (`cmd/yaah/web/index.html`)

The frontend is a self-contained HTML file. It renders a chat message list, a text input, a tool-activity panel, and modal dialogs for questions and approvals. It connects to `/api/stream` via `EventSource`, handles each event type via a dispatch table, and submits actions via `fetch('/api/action', {method:'POST',...})`.

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-001 | Create `cmd/yaah/web/` directory and `index.html`. The file must be entirely self-contained (no CDN links). Structure: `<head>` with inline `<style>` for layout (dark sidebar, chat bubbles, spinner, modal overlay), `<body>` with `#messages`, `#input-form`, `#tool-activity`, `#modal`, `<script>` block. | | |
| TASK-002 | Implement the JS `EventSource` connection to `/api/stream`. On `message` event: parse JSON, dispatch to a handler map keyed on `type`. | | |
| TASK-003 | Implement the `token` handler: append text to the current `<div class="message assistant">` in the `#messages` list. If no current assistant message exists, create one. | | |
| TASK-004 | Implement `tool.start` / `tool.end` handlers: add/remove a tool-activity indicator in `#tool-activity`. | | |
| TASK-005 | Implement `done` handler: finalize the current assistant message div; re-enable the input; show error if `error` field set. | | |
| TASK-006 | Implement `ctrl.question` handler: populate `#modal` with header text, question, radio/checkbox options, confirm button; disable input; on confirm, `fetch('/api/action', {type:'answer',id,value})`. | | |
| TASK-007 | Implement `ctrl.approval` handler: populate `#modal` with tool name, args, Allow/Deny buttons; on selection, `fetch('/api/action', {type:'answer',id,value:'true'/'false'})`. | | |
| TASK-008 | Implement `ctrl.status`, `ctrl.error`, `ctrl.todos`, `ctrl.context` handlers: update a status bar at the bottom of the UI. | | |
| TASK-009 | Implement prompt submission: `#input-form` submit handler POSTs `{type:'prompt',text}`. Disable input on submit; re-enable on `done`. Handle 409 (already running) with a user-visible notice. | | |
| TASK-010 | Handle SSE reconnect: `EventSource` auto-reconnects on drop; add a visual `connecting…` indicator while disconnected. | | |

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
