# ADR-001: Engine-View Separation

> **Status:** Accepted
> **Date:** 2026-08-03
> **Author:** @buchenberg
> **Related:** ADR-002, ADR-004
> **Implementation:** PRs #60, #62
> **Plan:** [.agents/plans/engine-view-separation/PLAN.md](.agents/plans/engine-view-separation/PLAN.md)

## Context

Before this refactoring, the yaah agent loop suffered from **tight coupling** between the core agent logic and various UI consumers (TUI, REPL, MCP). This created several problems:

1. **Dual Delivery Mechanism**: Events were delivered both through direct callbacks on the Loop and via a pub/sub broker, creating confusion about which path to use and leading to potential double-delivery bugs.

2. **God Struct**: A 25-field `AgentMsg` struct served as a catch-all for every piece of information that needed to flow from the agent to UIs. This violated Single Responsibility Principle and made the code hard to understand and maintain.

3. **Long Delivery Pipeline**: The TUI event delivery path required 8 hops through various adapters and translators, making it difficult to trace event flow and introducing latency.

4. **Callback Hell**: Various consumers registered callbacks on the Loop for different events, creating a tangled web of dependencies and making it difficult to add new consumers.

5. **Lack of Type Safety**: The `AgentMsg` struct used string fields and type assertions, making it easy to introduce bugs through typos or incorrect field access.

6. **Testing Difficulty**: The tight coupling made it hard to test the agent loop in isolation from UI concerns.

### Pain Points

```go
// Before: AgentMsg god struct (25+ fields)
type AgentMsg struct {
    TokenText      string
    ThinkingText   string
    FlushContent   string
    ToolName       string
    ToolArgs       string
    ToolResult     string
    ToolDuration   time.Duration
    ToolError      string
    SubAgentRole   string
    SubAgentModel  string
    SubAgentPrompt string
    // ... 15 more fields
}

// Delivery path: Loop → Callback → Translator → Adapter → ... → TUI (8 hops)
```

## Decision

Implement a **strict engine-view separation** with the following design:

### 1. Typed Event System

Replace the monolithic `AgentMsg` with a **sealed interface** and type-safe event structs:

```go
// Sealed interface - compiler enforces exhaustiveness
type Event interface {
    eventMarker() // unexported method = sealed
}

// Individual event types
type TokenDeltaEvent struct { Text string }
type ThinkingEvent struct { Text string }
type ToolStartEvent struct { ID int64; Name, Args string }
type ToolEndEvent struct { ID int64; Name, Args, Result string; Duration time.Duration; Error string }
// ... etc
```

**Benefits:**
- Compile-time type safety
- Exhaustive handling via type switches
- Clear, self-documenting event types
- Easy to add new event types

### 2. Internalized Broker

The Loop now **internally owns** a `pubsub.Broker[Event]` and publishes all events to it. The broker is **not exposed** to callers.

```go
type Loop struct {
    broker     *pubsub.Broker[Event]
    brokerView *BrokerView
    // ...
}

func (l *Loop) publishDone(response *string, runErr *error) {
    var done DoneEvent
    // ... populate fields ...
    l.broker.PublishMustDeliver(&done)
}
```

### 3. View Interface

Consumers implement the simple `View` interface:

```go
type View interface {
    HandleEvent(Event)
}

// BrokerView adapts pub/sub to View
type BrokerView struct {
    broker *Broker[Event]
    view   View
}

func (bv *BrokerView) HandleEvent(evt Event) {
    bv.broker.Publish(evt)
}
```

### 4. Unified Delivery Path

All consumers (TUI, REPL, MCP, ACP) now use the **same pattern**:

```go
// In Loop construction:
loop := agent.NewLoop(provider, registry,
    agent.WithView(myView),  // Set the View
    // ...
)

// Loop internally creates broker and BrokerView
if l.View != nil {
    l.broker = pubsub.NewBroker[Event]()
    l.brokerView = NewBrokerView(l.broker, l.View)
}
```

**Delivery path reduced from 8 hops to 4 hops:**
1. Loop publishes event to internal broker
2. BrokerView forwards to View
3. View implementation handles event
4. Done

### 5. No More Callbacks

All callbacks on Loop were **removed**. Everything flows through the event system.

## Alternatives Considered

### Alternative 1: Keep Callbacks, Add Events

Keep existing callbacks and add events as an additional delivery mechanism.

**Rejected because:**
- Creates two parallel systems to maintain
- Confusion about which to use
- Potential for double-delivery bugs
- Doesn't solve the god struct problem

### Alternative 2: Observer Pattern with Registration

Allow consumers to register callbacks for specific event types.

**Rejected because:**
- Still requires type assertions or reflection
- More complex than simple interface implementation
- Harder to test
- Less explicit about what events are available

### Alternative 3: Channel-Based Events

Return a channel of events from the Loop that consumers can read from.

**Rejected because:**
- Makes lifecycle management complex (who closes the channel?)
- Harder to handle backpressure
- Doesn't integrate well with multiple concurrent loops

## Consequences

### Positive

1. **Type Safety**: Compile-time checking of all event handling via sealed interface and type switches
2. **Simplified Architecture**: Single, clear delivery path for all events
3. **Reduced Complexity**: Pipeline reduced from 8 hops to 4 hops
4. **Better Testability**: Easy to mock View implementations for testing
5. **Extensibility**: New consumers can be added by implementing the View interface
6. **Maintainability**: Code is easier to understand and modify
7. **Performance**: Fewer allocations and copies in the event delivery path

### Negative

1. **Migration Effort**: Required changes to all existing consumers (TUI, REPL, MCP, ACP)
2. **Learning Curve**: New contributors need to understand the event system
3. **Boilerplate**: Each new event type requires defining a struct and implementing `eventMarker()`

## Migration Notes

### Before (PR #59 and earlier)

```go
type AgentMsg struct {
    TokenText    string
    ThinkingText string
    // ... 23 more fields
}

loop := &Loop{
    OnToken: func(text string) {
        // callback
    },
    OnToolStart: func(name, args string) {
        // callback
    },
    // ... 8 more callbacks
}
```

### After (PR #60 onwards)

```go
// TUI implementation
type terminalView struct{}

func (v *terminalView) HandleEvent(evt agent.Event) {
    switch e := evt.(type) {
    case *agent.TokenDeltaEvent:
        fmt.Fprint(os.Stderr, e.Text)
    case *agent.ToolStartEvent:
        // handle tool start
    // ... exhaustive type switch
    }
}

loop := agent.NewLoop(provider, registry,
    agent.WithView(&terminalView{}),
)
```

## References

- [PR #60: Engine-view separation (phase 1)](https://github.com/buchenberg/yaah/pull/60)
- [PR #62: Engine-view separation (phase 2)](https://github.com/buchenberg/yaah/pull/62)
- [Plan: Engine-View Separation](.agents/plans/engine-view-separation/PLAN.md)
- [internal/agent/events.go](internal/agent/events.go) - Event type definitions
- [internal/agent/view.go](internal/agent/view.go) - View interface and BrokerView
- [internal/pubsub/broker.go](internal/pubsub/broker.go) - Generic pub/sub broker

## Consumers Table

| Consumer | View Implementation | File | Notes |
|----------|---------------------|------|-------|
| TUI | `Model.HandleEvent` (type switch) | `internal/tui/tui.go` | Bubble Tea TUI |
| REPL | `terminalView` / `replView` | `cmd/yaah/agent_frame.go` | Terminal REPL |
| Sub-agents | `agent.NoopView` | `cmd/yaah/subagent_runner.go` | No UI, just execution |
| MCP serve | `agent.NoopView` | `cmd/yaah/serve.go` | MCP tool server |
| ACP serve | `acpView` + `acpViewWithWrite` | `cmd/yaah/acp.go`, `cmd/yaah/acp_view.go` | ACP protocol server |

---

*This ADR documents an implemented decision. For background on the refactoring, see the [engine-view-separation plan](.agents/plans/engine-view-separation/PLAN.md).*
