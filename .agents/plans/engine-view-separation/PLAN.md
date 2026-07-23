---
name: engine-view-separation
description: Refactor the engine-view boundary with clean interfaces, typed events, and single-delivery architecture
status: completed
---

## Engine-View Separation: Analysis & Refactoring Plan

### 0. Current State Analysis

The agent loop (`internal/agent/agent.go`) communicates with the TUI view (`cmd/yaah/tui.go` → `internal/tui/tui.go`) through a dual-delivery mechanism:

#### 0.1 Dual Delivery: Callbacks + Broker

The `agent.Loop` struct (agent.go:96-323) has **two parallel event delivery mechanisms**:

**Callback fields** (agent.go:104-108):
```go
OnToken    TokenCallback       // token delta stream
OnTool     ToolCallback        // tool start/end
OnSubAgent SubAgentCallback    // sub-agent lifecycle
OnThinking ThinkingCallback    // reasoning tokens
OnFlush    FlushCallback       // flush before tool call
```

**Broker field** (agent.go:110-114):
```go
Broker *pubsub.Broker[AgentEvent]  // decoupled pub/sub
```

In `applyDefaults()` (agent.go:665-690), when a Broker is set, the callbacks are **wrapped** so both fire:

```go
if l.Broker != nil {
    innerToken := onToken
    onToken = func(token string) {
        l.Broker.Publish(AgentEvent{Type: EventTokenDelta, Content: token})
        if innerToken != nil { innerToken(token) }
    }
    // ... same pattern for OnThinking
}
```

This means every event is delivered through **two paths** — a classic sign of architectural drift. The broker was bolted on without removing the callbacks.

#### 0.2 Who Uses What

| Consumer | Broker | Callbacks | File |
|----------|--------|-----------|------|
| TUI | ✅ Yes | ✅ Yes (wrapped) | cmd/yaah/tui.go:546-575 |
| REPL | ❌ No | ✅ Yes | cmd/yaah/agent_frame.go:406-456 |
| Sub-agent runner | ❌ No | ✅ Yes | cmd/yaah/subagent_runner.go:328-329 |

**Only the TUI uses the broker.** The REPL and sub-agent runner use callbacks exclusively. The broker is not the single source of truth — it's an optional add-on that only one consumer opts into.

#### 0.3 The TUI Wiring: 8-Hop Delivery Pipeline

From engine to pixel, a single token delta travels through:

1. `agent.Loop` → publishes to `Broker.Publish()` (agent.go:676)
2. `Broker` → delivers to subscriber channel (pubsub/broker.go:42)
3. Subscription goroutine → receives from channel (tui.go:553)
4. `convertAgentEvent()` → translates `agent.AgentEvent` → `tui.AgentMsg` (tui.go:611-644)
5. `agentCh` channel → sends to forwarder goroutine (tui.go:554)
6. Forwarder goroutine → `p.Send(msg)` (tui.go:508)
7. bubbletea runtime → delivers to `Model.Update()` (tui.go:865-868)
8. `HandleAgentMsg()` → dispatches to UI state mutations (tui.go:669)

That's **8 hops** for a single token delta. Hops 2-6 are infrastructure, not logic.

#### 0.4 `tui.AgentMsg` — The God Struct

Defined at tui.go:69-100, `AgentMsg` is a flat struct with **25 fields**, each optional:

```go
type AgentMsg struct {
    Token          string          // set for token deltas
    Thinking       string          // set for reasoning
    ToolName       string          // set for tool start
    ToolArgs       string
    ToolResult     string          // set for tool end
    ToolResultName string
    ToolDuration   string
    Flush          string          // set before tool calls
    Done           bool            // set on completion
    Response       string
    Err            error
    ContextTokens  int
    ContextWindow  int
    ModelList      []string
    ProviderNames  map[string]string
    Question       *QuestionModal  // set for question dialogs
    ApproveChan    chan bool       // set for approval dialogs
    ApproveName    string
    ApproveArgs    string
    MCPInfos       []ServerInfo
    Todos          []todo.Item
    SubAgentStart  bool            // sub-agent lifecycle
    SubAgentRole   string
    SubAgentLabel  string
    SubAgentEnd    bool
    SubAgentModel  string
    SubAgentDur    string
    SubAgentErr    string
}
```

**Only 1-3 fields are set per message.** The receiver uses a 200-line if-else chain in `HandleAgentMsg` (tui.go:669-857) to figure out "which kind of message is this?" by checking which fields are non-zero:

```go
func (m *Model) HandleAgentMsg(msg AgentMsg) {
    if msg.Todos != nil { ... return }
    if msg.Err != nil { ... return }
    if msg.Flush != "" { ... return }
    if msg.Token != "" { ... return }
    if msg.Thinking != "" { ... return }
    if msg.SubAgentStart { ... return }
    if msg.SubAgentEnd { ... return }
    if msg.ToolName != "" { ... return }
    if msg.ToolResult != "" || msg.ToolResultName != "" { ... return }
    if msg.Done { ... return }
    if msg.Question != nil { ... return }
    if msg.ApproveChan != nil { ... return }
    // ...
}
```

This is **discriminated union implemented via field presence checks** — a pattern that's fragile, hard to extend, and impossible for the compiler to verify exhaustiveness.

#### 0.5 `convertAgentEvent()` — Lossy Translation Layer

The function at tui.go:611-644 translates structured `agent.AgentEvent` → flat `tui.AgentMsg`:

```go
func convertAgentEvent(evt agent.AgentEvent) tui.AgentMsg {
    switch evt.Type {
    case agent.EventTokenDelta:
        return tui.AgentMsg{Token: evt.Content}
    case agent.EventThinking:
        return tui.AgentMsg{Thinking: evt.Content}
    // ...
    case agent.EventSubAgentStart:
        return tui.AgentMsg{
            SubAgentStart: true,
            SubAgentRole:  evt.SubAgentRole,
            SubAgentLabel: evt.SubAgentPrompt,  // field name changes!
        }
    }
}
```

Note the field name mismatch: `evt.SubAgentPrompt` → `msg.SubAgentLabel`. Then in `HandleAgentMsg`, the sub-agent data is re-assembled into display strings *again* — the TUI re-does formatting work that the REPL's callbacks already do inline (agent_frame.go:438-456).

#### 0.6 `runTUI()` — The 400-Line God Function

`runTUI` (tui.go:120-529) does **everything**:
- OTEL setup (lines 162-193)
- Instructions loading (lines 196-215)
- Tool registry population (lines 217-340)
- Memory/database wiring (lines 231-271)
- MCP client startup (lines 274-295)
- Broker creation and subscription (lines 546-556)
- Question tool handler wiring (lines 481-504)
- Model fetching (lines 513-522)

It's a single function that wires the entire application. There's no separation between "create the engine", "create the view", and "connect them".

#### 0.7 SOLID Assessment

| Principle | Status | Evidence |
|-----------|--------|----------|
| **S**ingle Responsibility | ❌ | `runTUI` has 10+ responsibilities. `AgentMsg` is a god struct. `HandleAgentMsg` handles 12+ message types. |
| **O**pen/Closed | ❌ | Adding a new event type requires modifying `agent.AgentEvent`, `agent.EventType`, `tui.AgentMsg`, `convertAgentEvent()`, AND `HandleAgentMsg()` — 5 changes in 3 files |
| **L**iskov Substitution | ⚠️ | N/A for this boundary, but callback+broker dual delivery means there's no single contract |
| **I**nterface Segregation | ❌ | `AgentMsg` forces every consumer to know about all 25 fields. A token-delta handler shouldn't need to know about approval channels |
| **D**ependency Inversion | ❌ | The TUI depends on concrete `agent.AgentEvent` and `tui.AgentMsg`. There's no `View` or `EventConsumer` interface |

---

### 1. Target Architecture

#### 1.1 Core Principle: Broker as Single Source of Truth

The `pubsub.Broker[AgentEvent]` becomes the **only** way events leave the agent loop. Callbacks are removed. The agent loop publishes typed events; consumers subscribe and receive them.

#### 1.2 Typed Event Hierarchy

Replace the flat `AgentEvent` with a Go interface + concrete types:

```go
// internal/agent/events.go

// Event is the interface all agent events satisfy.
type Event interface {
    eventMarker() // unexported, sealed interface
}

type TokenDeltaEvent  struct { Text string }
type ThinkingEvent    struct { Text string }
type FlushEvent       struct { Content string }
type ToolStartEvent   struct { Name, Args string }
type ToolEndEvent     struct {
    Name     string
    Args     string
    Result   string
    Duration time.Duration
    Error    string
}
type SubAgentStartEvent struct {
    Role   string
    Model  string
    Prompt string
}
type SubAgentEndEvent struct {
    Role     string
    Model    string
    Duration time.Duration
    Error    string
}
```

Benefits:
- Type switch with compiler-checked exhaustiveness
- No field presence guessing
- Each consumer sees only the fields it needs
- Adding a new event type is a new struct — no existing code changes

#### 1.3 View Interface

```go
// internal/agent/view.go

// View consumes agent events. Implementations include the TUI,
// REPL terminal output, and headless/test consumers.
type View interface {
    // HandleEvent processes a single agent event. Called from the
    // agent's goroutine or from a dedicated forwarder goroutine.
    HandleEvent(Event)
}

// ViewCloser is an optional interface for views that need cleanup.
type ViewCloser interface {
    View
    Close()
}
```

The agent loop gets a `View` field instead of callbacks+broker:

```go
type Loop struct {
    // ... other fields ...
    View View  // single consumer of agent events
}
```

Internally, `View` wraps the broker — the loop publishes to the broker, and a goroutine reads from the broker and calls `view.HandleEvent()`.

#### 1.4 Reduced Hop Count

Target pipeline for a token delta:
1. `agent.Loop` → publishes to broker
2. Broker → delivers to subscriber
3. Forwarder → `view.HandleEvent(TokenDeltaEvent{...})`
4. TUI `HandleEvent` → type-switch → state mutation

**4 hops instead of 8.** The `convertAgentEvent` translation layer is gone. The `AgentMsg` god struct is gone. The `agentCh` channel is internal to the view adapter.

#### 1.5 Adapter Pattern for Existing Views

```go
// internal/agent/view.go

// BrokerView adapts a pubsub.Broker subscription into a View.
// It runs a forwarder goroutine that reads from the broker
// subscription and calls HandleEvent on the target view.
type BrokerView struct {
    broker *pubsub.Broker[Event]
    target View
    sub    <-chan Event
    done   chan struct{}
}

func NewBrokerView(broker *pubsub.Broker[Event], target View) *BrokerView {
    sub := broker.Subscribe("view", 256)
    bv := &BrokerView{broker: broker, target: target, sub: sub, done: make(chan struct{})}
    go bv.forward()
    return bv
}

func (bv *BrokerView) forward() {
    defer close(bv.done)
    for evt := range bv.sub {
        bv.target.HandleEvent(evt)
    }
}

func (bv *BrokerView) Close() { bv.broker.Close(); <-bv.done }
```

---

### 2. Phase Breakdown

### Phase 1: Define Typed Event System & View Interface

**Goal:** Introduce `Event` interface and concrete types in `internal/agent/`. Define `View` interface. No existing code changes — these are new types only.

**Files to create/modify:**
- `internal/agent/events.go` — replace flat `AgentEvent` with `Event` interface + concrete types
- `internal/agent/view.go` — `View` interface, `BrokerView` adapter
- `internal/agent/events_test.go` — type-system tests

**Acceptance criteria:**
- `go build ./...` passes (new types are unused but compile)
- Each event type has a `_ Event = (*ConcreteType)(nil)` compile-time assertion
- `View` interface is documented with godoc comments
- `BrokerView` has a test that verifies event forwarding

**Estimated changes:** ~200 lines net new, 0 existing lines modified

### Phase 2: Broker as Single Source of Truth in the Agent Loop

**Goal:** Remove callbacks from `agent.Loop`. Replace with `View` field. The agent publishes typed events through the broker, and the broker's `BrokerView` adapter calls `View.HandleEvent`.

**Files to modify:**
- `internal/agent/agent.go` — remove `OnToken`, `OnTool`, `OnSubAgent`, `OnThinking`, `OnFlush` fields; add `View` field; remove `Broker` field (broker becomes internal to the loop); update `applyDefaults` and `runMiddleware`
- `internal/agent/agent_tools.go` — update publish calls to use new event types
- `internal/agent/agent_test.go` — update tests

**Key design decision:** The broker moves **inside** the agent loop — callers do not create or pass a broker. They pass a `View`, and the loop internally creates a `pubsub.Broker[Event]` and a `BrokerView` adapter.

```go
// agent.go — simplified Loop
type Loop struct {
    // ... other fields ...
    View          View          // single consumer (was: OnToken+OnTool+OnSubAgent+OnThinking+OnFlush+Broker)
    
    // internal (not exported)
    broker        *pubsub.Broker[Event]
    brokerView    *BrokerView
}
```

In `applyDefaults`:
```go
if l.View != nil {
    l.broker = pubsub.NewBroker[Event]()
    l.brokerView = NewBrokerView(l.broker, l.View)
}
// Wrap provider callbacks to publish to broker only (no dual delivery)
onToken = func(token string) {
    l.broker.Publish(TokenDeltaEvent{Text: token})
}
```

**Acceptance criteria:**
- Zero callback fields remain on `agent.Loop`
- All existing tests pass (updated to use `View` interface)
- REPL, TUI, and sub-agent runner compile and function
- `go vet ./...` clean

**Estimated changes:** ~150 lines modified in agent.go, ~50 in agent_tools.go, ~100 in test files

### Phase 3: Refactor TUI onto Typed Events

**Goal:** Replace `tui.AgentMsg` with the `agent.Event` interface. Replace `HandleAgentMsg` if-else chain with type-switched handler. Remove `convertAgentEvent()` entirely.

**Files to modify:**
- `internal/tui/tui.go` — implement `agent.View` on `Model`; remove `AgentMsg`, `HandleAgentMsg`; replace with `HandleEvent(agent.Event)` using type switch
- `internal/tui/component_test.go` — update tests
- `internal/tui/tui_test.go` — update tests
- `cmd/yaah/tui.go` — remove broker creation, `convertAgentEvent`, `agentCh`, forwarder goroutines; pass `m` (the TUI model) directly as the `View` to the loop

**Target code in tui.go:**
```go
// tui.go — Model implements agent.View
func (m *Model) HandleEvent(evt agent.Event) {
    switch e := evt.(type) {
    case *agent.TokenDeltaEvent:
        m.AppendToken(e.Text)
    case *agent.ThinkingEvent:
        m.thinkContent += e.Text
        m.refreshViewport()
        m.scrollToBottom()
    case *agent.FlushEvent:
        // ... flush logic
    case *agent.ToolStartEvent:
        m.SetToolCall(e.Name, e.Args)
    case *agent.ToolEndEvent:
        m.ClearToolCall()
        m.AddToolResult(e.Name, e.Result, e.Args, formatDuration(e.Duration))
    case *agent.SubAgentStartEvent:
        // ... sub-agent start display
    case *agent.SubAgentEndEvent:
        // ... sub-agent end display
    }
}
```

**Target code in runAgentForTUI:**
```go
func runAgentForTUI(prompt string, view agent.View, ...) {
    loop := &agent.Loop{
        // ... other fields ...
        View: view,  // single field, no broker, no callbacks
    }
    response, err := loop.Run(context.Background(), prompt)
    // ... handle response
}
```

**Acceptance criteria:**
- `tui.AgentMsg` type deleted
- `convertAgentEvent` function deleted
- `HandleAgentMsg` replaced by `HandleEvent`
- `agentCh` channel and forwarder goroutines removed
- All TUI tests pass
- Visual behavior unchanged (manual smoke test)

**Estimated changes:** ~300 lines removed, ~100 lines added in tui.go; ~50 lines removed in tui.go command wiring

### Phase 4: Unify REPL on View Interface

**Goal:** The REPL path (`cmd/yaah/agent_frame.go`) uses the same `View` interface as the TUI instead of raw callbacks.

**Files to modify:**
- `cmd/yaah/agent_frame.go` — create a `terminalView` struct implementing `agent.View`; remove callback assignments; pass view to loop
- `cmd/yaah/subagent_runner.go` — use a no-op view instead of empty callbacks

**New type:**
```go
// agent_frame.go
type terminalView struct{}

func (terminalView) HandleEvent(evt agent.Event) {
    switch e := evt.(type) {
    case *agent.TokenDeltaEvent:
        fmt.Fprint(os.Stderr, e.Text)
    case *agent.ToolStartEvent:
        // ... existing tool display logic
    case *agent.ToolEndEvent:
        // ... existing tool display logic
    case *agent.SubAgentStartEvent:
        // ... existing sub-agent display logic
    case *agent.SubAgentEndEvent:
        // ... existing sub-agent display logic
    }
}
```

**Acceptance criteria:**
- REPL output identical to current behavior
- `OnToken`, `OnTool`, `OnSubAgent` assignments in agent_frame.go removed
- Sub-agent runner uses `agent.NoopView` (a provided no-op implementation)
- All REPL one-shot tests pass

**Estimated changes:** ~100 lines added (terminalView), ~30 lines removed in agent_frame.go, ~5 lines changed in subagent_runner.go

### Phase 5: Polish & Documentation

**Goal:** Add tests, documentation, and clean up remaining artifacts.

**Tasks:**
- Add `internal/agent/view_test.go` — tests for BrokerView adapter
- Add test-only `RecordingView` for use in agent loop tests
- Document the `View` contract in `internal/agent/view.go` godoc
- Update `AGENTS.md` with architectural notes about the engine-view boundary
- Remove any remaining references to the old callback types (`TokenCallback`, `ToolCallback`, etc.)
- Consider making `pubsub.Broker` unexported (internal to the agent package)

**Acceptance criteria:**
- 100% coverage on view adapter
- Godoc for `View` interface includes a usage example
- AGENTS.md section about adding new views
- Old callback type aliases removed

---

### 3. Migration Strategy

Each phase is independently mergeable:
- **Phase 1** adds types only — zero risk, can merge immediately
- **Phase 2** is the core refactor — all tests must pass before merge
- **Phase 3** is TUI-only — can be reverted without affecting REPL
- **Phase 4** is REPL-only — can be reverted without affecting TUI
- **Phase 5** is polish only

**Rollback plan:** If Phase 2 breaks something unexpected, the dual-delivery mechanism (callbacks + broker) can be temporarily restored by keeping the old callback fields alongside the new `View` field, with a deprecation notice.

---

### 4. Summary of Architectural Improvements

| Dimension | Before | After |
|-----------|--------|-------|
| Delivery paths | 2 (callbacks + broker) | 1 (broker only) |
| TUI hop count | 8 | 4 |
| Event types | 1 flat struct, 25 fields | 7 typed structs |
| Adding an event | Modify 5 places in 3 files | Add 1 struct, 1 case |
| View abstraction | None | `agent.View` interface |
| REPL vs TUI integration | Completely different | Identical pattern |
| Compiler safety | Field presence checks | Type switch (exhaustive) |
| Testability | Need full TUI or mock callbacks | `RecordingView` for tests |
