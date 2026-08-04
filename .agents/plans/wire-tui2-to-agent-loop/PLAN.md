---
name: wire-tui2-to-agent-loop
description: Wire TUI2 (tview prototype) to the agent loop — implement agent.View, control messages, callbacks, and replace hardcoded sample data with real agent-driven data
status: approved
---

# Wire TUI2 to the Agent Loop

## Context

`internal/tui2/` is a tview-based visual prototype (33 files, beautiful
component layout) but it is completely disconnected from the agent loop.
It runs `populateSampleData()` with fake messages. The `tui2` cobra
command describes it as *"a visual prototype — it mirrors the yaah TUI
layout but is not yet wired to the agent loop."*

The existing Bubbletea TUI (`internal/tui/`) is fully wired via:
- `agent.View` (HandleEvent for typed agent events)
- `types.CtrlMsg` channel (control-plane: questions, approvals, todos, models)
- Closure-captured callbacks (OnSubmit → RunPrompt, OnAbort → cancel, OnCompact → compact)

TUI2 needs the same three connections to become functional.

## Current TUI2 architecture

```
TUI2 struct (tui2.go)
├── *tview.Application          # tview app instance
├── *tview.Pages                # Modal overlay layer
├── *tview.Flex                 # Root layout
├── chatView                    # Chat message view (TextView)
├── inputArea                   # Input field (InputField)
├── statusBar                   # Bottom bar (TextView)
├── helpPages                   # Help overlay
├── infoPane                    # Right sidebar with tabs
├── modelPicker                 # Model selector
├── approvalModal               # Tool approval dialog
├── questionModal               # Question dialog
├── thinkingSpinner             # Thinking/processing indicator
├── commandBar                  # Slash-command overlay
└── OnSubmit func(string)       # Callback when user submits input
```

### Components with hardcoded sample data

| Component | Hardcoded data |
|-----------|---------------|
| `infopane/infopane.go` | Session info, context window, MCP servers, todos |
| `statusbar/statusbar.go` | Context count, mode, cost |
| `tui2.go` (`populateSampleData()`) | 12 sample messages (user, assistant, tool results, sub-agent lifecycle) |
| `sample.go` | Fake message/text fixtures |

### Components ready to use (just need real data)

| Component | State |
|-----------|-------|
| `question/question.go` | ✅ Modal fully functional, returns answer via callback |
| `approval/approval.go` | ✅ Modal fully functional, returns approve/deny |
| `thinking/thinking.go` | ✅ Spinner component done |
| `todo/todo.go` | ✅ Formatting ready |
| `modelpicker/modelpicker.go` | ✅ Selection ready |
| `command/command.go` | ✅ Overlay ready |

## Step-by-step

### Phase 1: Add event handling — implement `agent.View`

**File: `internal/tui2/tui2.go`** (and possibly a new `events.go`)

Add a `HandleEvent(event agent.Event)` method to the `TUI2` struct. All
UI mutations must go through `a.app.QueueUpdateDraw()` for thread safety
(agent events arrive on a different goroutine).

Each event type maps to a component update:

| Event | Action |
|-------|--------|
| `TokenDeltaEvent` | Append token text to the current assistant message buffer; call `FlushEvent` logic when newline-separated |
| `ThinkingEvent` | Update thinking spinner visibility + label |
| `FlushEvent` | Flush buffered tokens into the chat view TextView |
| `PromptEvent` | Echo user prompt as a new message in chat view |
| `ToolStartEvent` | Add tool-call entry showing name + args; set `pendingTool` state |
| `ToolEndEvent` | Append tool result to the tool-call entry; clear `pendingTool` |
| `SubAgentStartEvent` | Add sub-agent lifecycle entry to chat + sidebar |
| `SubAgentEndEvent` | Update sub-agent entry with result/status |
| `EscalationEvent` | Display escalation banner in chat |
| `DoneEvent` | Mark turn complete; re-enable input |
| `CompactionStartedEvent` | Show compaction indicator |
| `CompactionDoneEvent` | Hide compaction indicator; show compacted summary |
| `ToolConcurrencyEvent` | Show/hide concurrent tool execution info |

**Message buffering:**
The existing TUI buffers tokens in a `pending` string and flushes on
newlines or at `FlushEvent`. TUI2 needs the same pattern — accumulate
token deltas in a buffer, render them into the chat view periodically.

### Phase 2: Wire control-plane messages

**File: new `internal/tui2/control.go`**

Add a `ControlCh chan types.CtrlMsg` field to `TUI2`. Start a goroutine
in `Run()` that reads from this channel and dispatches via
`QueueUpdateDraw`:

| Message | Action |
|---------|--------|
| `CtrlQuestion` | Open `question.Modal`; send answer back on `AnswerCh` |
| `CtrlApproval` | Open `approval.Modal`; send bool back on `ApproveCh` |
| `CtrlModelList` | Update model picker data |
| `CtrlTodos` | Update info pane todo list |
| `CtrlContextInfo` | Update status bar context count; update info pane |
| `CtrlStatus` | Show status toast/notification |
| `CtrlError` | Show error toast/notification |
| `CtrlFallback` | Update provider/model display |
| `CtrlDone` | Close the channel reader goroutine |

The channel goroutine pattern (from the existing `cmd/yaah/tui.go`:

```go
go func() {
    for msg := range controlCh {
        // Dispatch on CtrlDone sentinel to return
        if _, ok := msg.(*types.CtrlDone); ok {
            return
        }
        app.QueueUpdateDraw(func() {
            tui2.handleControlMsg(msg)
        })
    }
}()
```

### Phase 3: Add agent callbacks

**File: `internal/tui2/tui2.go`**

Add callback fields to `TUI2`:

```go
type TUI2 struct {
    // ... existing fields ...
    
    // Agent callbacks — set by cmd/yaah/tui2.go before Run()
    OnSubmit  func(prompt string)          // called when user submits input
    OnAbort   func()                       // called on Ctrl+C / Escape
    OnCompact func()                       // called on compact request
    OnClear   func()                       // called on clear request
    
    // Control channel — fed by cmd/yaah/tui2.go
    ControlCh chan types.CtrlMsg
    
    // MCP server info — set before Run()
    MCPInfos []mcp.ServerInfo
}
```

Wire `OnSubmit` to replace the current `populateSampleData()` call in
the input handler. Wire `OnAbort` to the Escape/Ctrl+C handler.

### Phase 4: Replace hardcoded data with real data

| File | Change |
|------|--------|
| `tui2.go` | Remove `populateSampleData()` call from input handler; use `OnSubmit` instead |
| `sample.go` | Remove entire file (no longer needed) |
| `infopane/infopane.go` | Replace hardcoded session/context/MCP/todo data with fields set via Setter methods |
| `statusbar/statusbar.go` | Replace hardcoded context/mode/cost with update methods |
| `tui2.go` help overlay | Hook up real keybindings from `keymap.go` |

Add setter methods to components that currently render hardcoded data:

```go
func (ip *InfoPane) SetSession(name, provider, model string)
func (ip *InfoPane) SetContext(tokens, window int)
func (ip *InfoPane) SetMCP(servers []mcp.ServerInfo)
func (ip *InfoPane) SetTodos(items []todo.Item)
func (sb *StatusBar) SetContext(tokens, window int)
func (sb *StatusBar) SetMode(mode string)
func (sb *StatusBar) SetCost(cost float64)
```

### Phase 5: Wire in cmd/yaah/tui2.go

**File: `cmd/yaah/tui2.go`**

Replace the stub `runTUI2` with real wiring following the existing
`cmd/yaah/tui.go` pattern:

1. Create `agent.Loop` with the appropriate config
2. Create `TUI2` instance
3. Set `OnSubmit` → `agentLoop.RunPrompt(prompt)`
4. Set `OnAbort` → `agentLoop.Cancel()`
5. Set `OnCompact` → `agentLoop.Compact()`
6. Set `ControlCh` to the session's control channel
7. Set `MCPInfos` from resolved MCP servers
8. Create `BrokerView` wrapping TUI2 as `agent.View`
9. Call `tui2.Run()` — this blocks until the UI exits
10. On exit, close the broker view and agent loop

### Phase 6: Thread safety audit

All agent events arrive via `HandleEvent` on the forwarder goroutine
(from `BrokerView`). All UI mutations in `HandleEvent` must use
`QueueUpdateDraw`. Control messages arrive on the control channel
goroutine — also must use `QueueUpdateDraw`.

**Rule:** Any method that touches a tview primitive (SetText, AddButton,
etc.) must be called inside `QueueUpdateDraw`.

### Phase 7: Quality gates

```bash
go build ./...                        # must compile
go test ./internal/tui2/...           # run any existing tests
go vet ./internal/tui2/...            # must be clean
staticcheck ./internal/tui2/...       # must be clean
```

Then manually test with: `yaah tui2` — should connect to agent loop,
accept prompts, display streaming tokens, show tool calls, etc.

## What does NOT change

- All component packages under `internal/tui2/components/` — they stay as-is, only receive new setter methods
- `internal/tui2/colors/` — untouched
- `internal/tui2/lolcat/` — untouched
- `internal/tui/` — untouched (existing TUI unchanged)
- `internal/agent/` — untouched (View interface is stable)
- `cmd/yaah/tui.go` — untouched (existing TUI wiring unchanged)

## Risks

| Risk | Likelihood | Mitigation |
|------|:---:|-----------|
| Thread safety bugs (tview mutations outside QueueUpdateDraw) | High | Audit every HandleEvent case; tview panics on cross-goroutine mutations make this easy to catch |
| Token buffering causes flicker | Medium | Reference existing TUI's buffering approach (pending string, flush on newlines) |
| Question/approval modal deadlock | Medium | Ensure AnswerCh/ApproveCh are written before QueueUpdateDraw returns |
| tview.Application.Run() blocks startup | Low | Run() is the event loop — all setup must happen before it's called |
| MCP server info unavailable at startup | Low | Accept nil and update later via control message
