# Plan: Architecture — View Layer Abstraction

**Status:** Implemented
**Goal:** Complete the view layer abstraction so any UI (web, alternative TUI, headless API) can be plugged in without modifying the agent core or the shared session infrastructure.

---

## Background

The agent core (`internal/agent/`) communicates with consumers through two typed contracts that already exist:

| Contract | Type | Location |
|---|---|---|
| Event stream (broker) | `agent.View` / `agent.Event` | `internal/agent/view.go`, `internal/agent/events.go` |
| Control plane (sync) | `types.CtrlMsg` sealed interface | `internal/types/control.go` |

The shared session infrastructure (`agentSession` in `cmd/yaah/agent_frame.go`) holds `view agent.View` and `ctrlCh chan<- types.CtrlMsg` fields with `SetView` / `SetCtrlCh` setters. All compaction and pruning options flow through the shared `runPrompt()` method.

### Current driver comparison

| Capability | TUI (`cmd/yaah/tui.go`) | REPL (`cmd/yaah/repl_loop.go`) |
|---|---|---|
| View wiring | Explicit: `sess.SetView(agentViewFwd{prog})` | Implicit: never called; `runPrompt` falls back to `terminalView{}` |
| Control channel | Explicit: `sess.SetCtrlCh(controlCh)` | None: `ctrlCh` is nil; `compactContext()` prints to stderr |
| Approval | `sess.SetApproveFn(approvalFn)` | Default from config |
| Steer / FollowUp | Closures capturing `sess.steerCh` / `sess.followupCh` directly | Not exposed |
| Todo override | Wired in `tui.go` to send `CtrlTodos` to channel | Falls back to stderr in `OnWrite` |
| Model switching | `sess.SetModel(pName, mName)` | Not available |

### What "pluggable UI" requires

A web WebSocket driver, a Discord adapter, a gRPC streaming API, or an alternative TUI framework must be able to:
1. Receive the event stream (tokens, tool calls, sub-agent events, done)
2. Send user prompts, steers, follow-ups, and compact commands to the session
3. Receive control-plane messages (status, errors, todos, approval requests, model list, done sentinel)
4. Cancel in-flight prompts on disconnect

None of those require changes to `internal/agent/`. The work is entirely in `cmd/yaah/agent_frame.go` and a new `internal/session/` package.

---

## Gaps (numbered for task reference)

| # | Gap | Impact |
|---|---|---|
| G-1 | `agentSession` is a concrete struct with unexported `runPrompt` | External drivers can't call it |
| G-2 | REPL relies on nil-fallback for view, not explicit wiring | Inconsistent, makes the pattern non-obvious |
| G-3 | No `Steer(string)` / `FollowUp(string)` methods on session | Web driver must access raw channels via closures |
| G-4 | No `context.Context` parameter on `runPrompt` | Web drivers can't cancel on disconnect |
| G-5 | `TodoWriteTool.OnWrite` override lives in `tui.go` | Every driver must repeat the override pattern |
| G-6 | No `Session` interface | Drivers depend on concrete `*agentSession`; can't test against a mock |
| G-7 | `steerCh` / `followupCh` are raw fields, not methods | Drivers must have package-internal access or use closure capture |

---

## Tasks

### TASK-001 — Define `Session` interface in `cmd/yaah/agent_frame.go`

**Status:** Not started
**Effort:** Small

Add an exported interface that covers all the methods a driver needs. Keep it in `cmd/yaah/` for now (not `internal/`) to avoid a premature package split.

```go
// Session is the stable contract between UI drivers and the shared
// agent session. Any driver (TUI, REPL, web, gRPC) should depend only
// on this interface, not on *agentSession directly.
type Session interface {
    // Submit sends a user prompt and blocks until the agent finishes.
    // ctx cancellation aborts the in-flight loop.
    RunPrompt(ctx context.Context, prompt string) (string, bool, error)

    // Compact triggers a manual context summarisation.
    Compact()

    // Steer injects a mid-turn steering message.
    Steer(string)

    // FollowUp queues a follow-up prompt for after the current turn.
    FollowUp(string)

    // SetView attaches an event consumer. Must be called before RunPrompt.
    SetView(agent.View)

    // SetCtrlCh attaches the control-plane channel. Must be called before RunPrompt.
    SetCtrlCh(chan<- types.CtrlMsg)

    // SetApproveFn attaches a tool-approval callback.
    SetApproveFn(func(name, args string) bool)

    // SetModel switches provider and model name for subsequent turns.
    SetModel(providerName, modelName string)

    // ProviderName returns the current provider identifier.
    ProviderName() string

    // ModelName returns the current model identifier.
    ModelName() string

    // MCPInfos returns the connected MCP server status list.
    MCPInfos() []mcp.ServerInfo

    // Close tears down the session (DB, MCP clients, OTel).
    Close()
}
```

Add a compile-time check: `var _ Session = (*agentSession)(nil)`.

**Acceptance:** `go build ./cmd/yaah/` passes with the check in place.

---

### TASK-002 — Add `ProviderName()`, `ModelName()`, `MCPInfos()`, `Close()` accessor methods

**Status:** Not started
**Effort:** Trivial

`agentSession` already stores these values. Add read-only accessor methods so drivers can query them without accessing the struct directly.

```go
func (s *agentSession) ProviderName() string { s.mu.RLock(); defer s.mu.RUnlock(); return s.providerName }
func (s *agentSession) ModelName() string    { s.mu.RLock(); defer s.mu.RUnlock(); return s.modelName }
func (s *agentSession) MCPInfos() []mcp.ServerInfo { ... } // return a copy
func (s *agentSession) Close() { s.close() } // exported alias
```

Depends on: TASK-001

---

### TASK-003 — Add `Steer(string)` and `FollowUp(string)` methods

**Status:** Not started
**Effort:** Small

Replace direct channel access in `tui.go` with method calls.

```go
func (s *agentSession) Steer(text string) {
    select {
    case s.steerCh <- text:
    default:
        s.sendCtrl(&types.CtrlStatus{Text: "steer queue full"})
    }
}

func (s *agentSession) FollowUp(text string) {
    select {
    case s.followupCh <- text:
    default:
        s.sendCtrl(&types.CtrlStatus{Text: "follow-up queue full"})
    }
}

// sendCtrl is a helper to avoid the nil-check boilerplate in multiple methods.
func (s *agentSession) sendCtrl(msg types.CtrlMsg) {
    s.mu.RLock()
    ch := s.ctrlCh
    s.mu.RUnlock()
    if ch == nil {
        return
    }
    select {
    case ch <- msg:
    default:
    }
}
```

Update `tui.go` `OnSteer` / `OnFollowUp` callbacks to call `sess.Steer(text)` / `sess.FollowUp(text)`.

Depends on: TASK-001

---

### TASK-004 — Add `ctx context.Context` to `RunPrompt`

**Status:** Not started
**Effort:** Medium

Change `runPrompt(prompt string, view ...agent.View)` to `runPrompt(ctx context.Context, prompt string, view ...agent.View)` and thread the context through to `loop.Run(ctx, prompt)`.

This is already partially there — `loop.Run` takes a `context.Context`. The REPL and TUI both pass `context.Background()` today. After this task, a web driver passes the HTTP request context so the browser's disconnect cancels the in-flight loop.

Update callers:
- `repl_loop.go`: pass `context.Background()` (no change in behavior)
- `tui.go` `OnSubmit`: use `context.WithCancel(context.Background())` — already done for `cancelAgent`; thread the `ctx` through to `RunPrompt`

Depends on: TASK-001

---

### TASK-005 — Move `TodoWriteTool.OnWrite` override into `SetCtrlCh`

**Status:** Not started
**Effort:** Small

Currently `tui.go` overrides `TodoWriteTool.OnWrite` to send `CtrlTodos` to the control channel. Any driver that wants todos routed to its UI must duplicate this. Instead, move the override into `SetCtrlCh`:

```go
func (s *agentSession) SetCtrlCh(ch chan<- types.CtrlMsg) {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.ctrlCh = ch

    // Re-wire the todo tool to send to the new channel.
    if tt := s.toolReg.Get("todowrite"); tt != nil {
        if ttp, ok := tt.(*tools.TodoWriteTool); ok {
            ttp.OnWrite = func() {
                items := ttp.Store.List()
                select {
                case ch <- &types.CtrlTodos{Items: items}:
                default:
                }
            }
        }
    }
}
```

Remove the duplicate override from `tui.go`.

Depends on: TASK-003

---

### TASK-006 — Make REPL an explicit driver (eliminate nil-fallback pattern)

**Status:** Not started
**Effort:** Small

The REPL never calls `sess.SetView()`, relying on a nil-check in `runPrompt` to fall back to `terminalView{}`. This is inconsistent with the TUI pattern and obscures intent.

Change `repl_loop.go` to explicitly wire:

```go
sess.SetView(&terminalView{})
// no ctrlCh — REPL is headless
```

And in `runPrompt`, convert the nil-fallback into a panic guard:

```go
// runPrompt requires a view. Call SetView before RunPrompt.
inner := v
if inner == nil {
    inner = agent.NoopView{}
}
```

The nil-fallback is preserved as `NoopView` (rather than `terminalView`) to make headless mode (`yaah serve`) correct — serve mode intentionally suppresses terminal output.

Depends on: TASK-002

---

### TASK-007 — Document the driver pattern with `terminalView` and `agentViewFwd` as examples

**Status:** Not started
**Effort:** Small (comments + docs only)

Add a comment block at the top of `agent_frame.go` describing the driver contract:

```
// UI Driver Pattern
//
// To attach a new UI to a session:
//
//  1. Create a chan types.CtrlMsg and call sess.SetCtrlCh(ch).
//     This wires todos and status messages to your channel.
//
//  2. Implement agent.View (HandleEvent) and call sess.SetCtrlCh(your view).
//     Events: TokenDelta, Thinking, Flush, ToolStart, ToolEnd,
//             SubAgentStart, SubAgentEnd, Done.
//
//  3. Read from the control channel in a goroutine and dispatch
//     CtrlStatus, CtrlError, CtrlQuestion, CtrlApproval,
//     CtrlModelList, CtrlTodos, CtrlContextInfo, CtrlDone.
//
//  4. Call sess.RunPrompt(ctx, text) for each turn.
//     ctx cancellation aborts the in-flight agent loop.
//
// See: terminalView (REPL), agentViewFwd (Bubble Tea TUI).
```

Also add a `cmd/yaah/web_example_test.go` (build-tag `ignore`) with a stub showing the minimum WebSocket driver skeleton.

---

### TASK-008 — Sketch WebSocket driver (`cmd/yaah/web.go`) — optional / future

**Status:** Not started (deferred)
**Effort:** Large (separate PR)

A concrete WebSocket driver that implements the `Session` interface contract:

```
cmd/yaah/
  web.go           — HTTP server, WebSocket upgrade, wsDriver struct
  web_view.go      — wsDriver implements agent.View (HandleEvent → JSON push)
  web_ctrl.go      — reads ctrlCh, marshals CtrlMsg to JSON frames
```

Wire in `root_cmd.go` behind `--web` flag or as a new `yaah web` subcommand.

Protocol (JSON over WebSocket):
- Client → server: `{"type":"prompt","text":"..."}`, `{"type":"steer","text":"..."}`, `{"type":"compact"}`, `{"type":"abort"}`
- Server → client: `{"type":"token","text":"..."}`, `{"type":"tool","name":"...","args":"..."}`, `{"type":"status","text":"..."}`, `{"type":"done","response":"..."}`

Depends on: TASK-001 through TASK-007

---

## Sequencing

```
TASK-001  (Session interface)
    └── TASK-002  (accessors)
        └── TASK-006  (explicit REPL wiring)
    └── TASK-003  (Steer/FollowUp methods)
        └── TASK-005  (TodoWriteTool in SetCtrlCh)
    └── TASK-004  (ctx on RunPrompt)
    └── TASK-007  (driver docs)
        └── TASK-008  (web driver — future)
```

TASK-001 is the gate. TASK-002 through TASK-005 can proceed in parallel once TASK-001 is done. TASK-006 and TASK-007 are cleanup that can land any time after TASK-002.

## Risk notes

- `runPrompt` is in `package yaah` (the `cmd` package). It cannot be imported by `internal/` packages. All driver code lives at the `cmd/yaah/` level, which is correct — drivers are an integration concern, not a library concern. No package restructuring is needed for TASK-001 through TASK-007.
- The `steerCh` / `followupCh` channels are created once in `newAgentSession()` and closed in `close()`. The new `Steer()` / `FollowUp()` methods must not be called after `Close()`. This invariant already exists implicitly; TASK-003 should document it with a comment.
- The `TodoWriteTool` override in TASK-005 runs inside a `SetCtrlCh` lock. The `OnWrite` closure must not acquire the same lock (it currently doesn't — it only calls `ch <-`).
