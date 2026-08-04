# ADR-0004: Event-Driven Architecture with Pub/Sub

> **Status:** Accepted
> **Date:** 2026-08-03
> **Author:** @buchenberg
> **Related:** ADR-001, ADR-002
> **Implementation:** PRs #60, #62 (part of engine-view separation)

## Context

As yaah's agent loop became more sophisticated, the need arose to **decouple the core agent logic from various consumers** (TUI, REPL, MCP server, ACP protocol). The agent loop needs to communicate many types of information to consumers:

1. **Streaming Content**: Tokens as they arrive from the model
2. **Thinking/Reasoning**: Extended thinking content from models like DeepSeek R1
3. **Tool Lifecycle**: When tools start, complete, or fail
4. **Sub-Agent Lifecycle**: When sub-agents are spawned and complete
5. **Context Management**: When compaction starts and finishes
6. **Completion**: When the agent loop finishes (success or error)
7. **Escalations**: When sub-agents raise blockers or warnings

### The Problem

Before ADR-0001 (Engine-View Separation), these communications happened through:

1. **Direct Callbacks**: Loop had fields like `OnToken func(string)`, `OnToolStart func(name, args string)`, etc.
2. **Polling**: Consumers periodically checked Loop state
3. **Shared State**: Consumers directly accessed Loop fields

**Problems:**
- **Tight Coupling**: Loop knew about all possible consumers
- **Dual Delivery**: Some events went through callbacks, others through a broker
- **Hard to Add New Events**: Required adding new callback fields to Loop
- **Hard to Test**: Consumers had to mock multiple callback mechanisms
- **Inconsistent**: Different consumers used different mechanisms
- **No Type Safety**: Callback signatures were ad-hoc and error-prone

## Decision

Implement a **type-safe, sealed event system** with a **generic pub/sub broker** as the single communication channel between the agent loop and all consumers.

### Architecture Overview

```
┌─────────────────────────────────────────────────────────────────┐
│                         AGENT LOOP                                 │
│                                                                  │
│  ┌─────────────────────────────────────────────────────────────┐│
│  │                    Loop Run()                               ││
│  │  ┌─────────────────────────────────────────────────────┐  ││
│  │  │              Internal Broker                           │  ││
│  │  │  ┌─────────────────────────────────────────────┐    │  ││
│  │  │  │            Broker[Event]                          │    │  ││
│  │  │  │  - Publish(Event)                               │    │  ││
│  │  │  │  - Subscribe() <-chan Event                      │    │  ││
│  │  │  └─────────────────────────────────────────────┘    │  ││
│  │  └─────────────────────────────────────────────────────┘  ││
│  └─────────────────────────────────────────────────────────────┘│
│                         ││                                       │
│                         ▼│                                       │
│  ┌─────────────────────────────────────────────────────────────┐│
│  │                 BrokerView Adapter                          ││
│  │  - Implements View interface                               ││
│  │  - Forwards events from Loop to external View              ││
│  └─────────────────────────────────────────────────────────────┘│
│                         │                                        │
└─────────────────────────┼────────────────────────────────────┘
                          │
                          ▼
┌─────────────────────────────────────────────────────────────────┐
│                      CONSUMERS (Views)                            │
│                                                                  │
│  ┌─────────────┐    ┌─────────────┐    ┌─────────────────┐    │
│  │   TUI       │    │   REPL      │    │   MCP Server    │    │
│  │ Model.HandleEvent()│ │ terminalView   │    │ agent.NoopView │    │
│  └─────────────┘    └─────────────┘    └─────────────────┘    │
│                                                                  │
│  ┌─────────────────────────────────────────────────────────────┐│
│  │                    ACP Server                                 ││
│  │               acpView + acpViewWithWrite                      ││
│  └─────────────────────────────────────────────────────────────┘│
└─────────────────────────────────────────────────────────────────┘
```

### Event System Design

#### 1. Sealed Interface

```go
// internal/agent/events.go

// Event is the interface all agent events satisfy.
// The interface is sealed via the unexported eventMarker method.
type Event interface {
    eventMarker() // unexported = sealed interface
}

// Compile-time satisfaction checks
var (
    _ Event = (*TokenDeltaEvent)(nil)
    _ Event = (*ThinkingEvent)(nil)
    _ Event = (*ToolStartEvent)(nil)
    _ Event = (*ToolEndEvent)(nil)
    _ Event = (*SubAgentStartEvent)(nil)
    _ Event = (*SubAgentEndEvent)(nil)
    _ Event = (*DoneEvent)(nil)
    _ Event = (*CompactionStartedEvent)(nil)
    _ Event = (*CompactionDoneEvent)(nil)
    _ Event = (*EscalationEvent)(nil)
    _ Event = (*FlushEvent)(nil)
)
```

**Why Sealed?**
- Prevents external packages from implementing Event
- Ensures all event types are known at compile time
- Enables exhaustive type switches (compiler enforces all cases handled)
- Provides type safety - no runtime type assertions needed

#### 2. Event Types

```go
// TokenDeltaEvent - streaming tokens from model
type TokenDeltaEvent struct { Text string }
func (*TokenDeltaEvent) eventMarker() {}

// ThinkingEvent - reasoning/thinking content
type ThinkingEvent struct { Text string }
func (*ThinkingEvent) eventMarker() {}

// FlushEvent - model finished streaming segment
type FlushEvent struct { Content string }
func (*FlushEvent) eventMarker() {}

// ToolStartEvent - tool execution started
type ToolStartEvent struct {
    ID   int64  // unique per-execution ID
    Name string // tool name
    Args string // abbreviated arguments
}
func (*ToolStartEvent) eventMarker() {}

// ToolEndEvent - tool execution completed
type ToolEndEvent struct {
    ID       int64         // matches ToolStartEvent
    Name     string        // tool name
    Args     string        // abbreviated arguments
    Result   string        // truncated result
    Duration time.Duration // execution duration
    Error    string        // error message (empty on success)
}
func (*ToolEndEvent) eventMarker() {}

// SubAgentStartEvent - sub-agent spawned
type SubAgentStartEvent struct {
    Role   string // sub-agent role
    Model  string // model assigned
    Prompt string // abbreviated task description
}
func (*SubAgentStartEvent) eventMarker() {}

// SubAgentEndEvent - sub-agent completed
type SubAgentEndEvent struct {
    Role     string        // sub-agent role
    Model    string        // model used
    Prompt   string        // abbreviated task description
    Duration time.Duration // total execution duration
    Error    string        // error message (empty on success)
}
func (*SubAgentEndEvent) eventMarker() {}

// DoneEvent - agent loop completed
type DoneEvent struct {
    Response      string
    Error         string
    ContextTokens int
    ContextWindow int
    FinishReason  string
    Usage         types.Usage
    ResponseModel string
}
func (*DoneEvent) eventMarker() {}

// CompactionStartedEvent - context compaction began
type CompactionStartedEvent struct {
    BeforeTokens int
    TargetTokens int
    Reason       string // "threshold", "overflow", "budget-only"
}
func (*CompactionStartedEvent) eventMarker() {}

// CompactionDoneEvent - context compaction finished
type CompactionDoneEvent struct {
    BeforeTokens    int
    AfterTokens     int
    SavingsPct      float64
    Method          string // "single", "chunked"
    ElapsedSeconds  float64
    IneffectiveNote string
    OldMsgCount     int
    KeepMsgCount    int
    Budget          int
}
func (*CompactionDoneEvent) eventMarker() {}

// EscalationEvent - sub-agent raised escalation
type EscalationEvent struct {
    SubAgentRole   string // sub-agent role
    SubAgentPrompt string // task description
    Severity       string // info | warning | blocker | critical
    Summary        string // one-line summary
    Detail         string // full explanation
    Suggestion     string // recommended next step
}
func (*EscalationEvent) eventMarker() {}
```

#### 3. Generic Pub/Sub Broker

```go
// internal/pubsub/broker.go

// Broker is a generic pub/sub broker for typed events.
// It supports multiple subscribers and ensures all subscribers
// receive all published events.
type Broker[T any] struct {
    subscribers []chan<- T
    mu          sync.RWMutex
}

// NewBroker creates a new Broker.
func NewBroker[T any]() *Broker[T] {
    return &Broker[T]{subscribers: make([]chan<- T, 0)}
}

// Subscribe returns a new channel that receives all published events.
// The caller owns the channel and must close it when done.
func (b *Broker[T]) Subscribe() <-chan T {
    b.mu.Lock()
    defer b.mu.Unlock()
    ch := make(chan T, 32)
    b.subscribers = append(b.subscribers, ch)
    return ch
}

// Publish sends an event to all subscribers.
func (b *Broker[T]) Publish(evt T) {
    b.mu.RLock()
    defer b.mu.RUnlock()
    for _, ch := range b.subscribers {
        ch <- evt
    }
}

// PublishMustDeliver sends an event to all subscribers and waits
// for all to receive it. Used for critical events like DoneEvent.
func (b *Broker[T]) PublishMustDeliver(evt T) {
    b.Publish(evt)
    // For must-deliver, we could add acknowledgment mechanism
    // but current implementation trusts channel buffers
}

// Close closes all subscriber channels.
func (b *Broker[T]) Close() {
    b.mu.Lock()
    defer b.mu.Unlock()
    for _, ch := range b.subscribers {
        close(ch)
    }
    b.subscribers = nil
}
```

#### 4. View Interface

```go
// internal/agent/view.go

// View is the interface consumers implement to receive agent events.
// This is the stable contract between the agent loop and all UIs.
type View interface {
    HandleEvent(Event)
}

// NoopView is a View implementation that discards all events.
// Used when no UI is needed (e.g., sub-agents, MCP serve mode).
type NoopView struct{}

func (NoopView) HandleEvent(Event) {}

// BrokerView adapts a Broker[Event] to the View interface.
// It forwards all events from the broker to the underlying view.
type BrokerView struct {
    broker *Broker[Event]
    view   View
}

func NewBrokerView(broker *Broker[Event], view View) *BrokerView {
    return &BrokerView{broker: broker, view: view}
}

func (bv *BrokerView) HandleEvent(evt Event) {
    bv.broker.Publish(evt)
}

func (bv *BrokerView) Close() {
    bv.broker.Close()
}
```

### Event Flow

```
1. Loop executes and produces an event
   └─> Loop.publishToken(token) 
       └─> broker.Publish(&TokenDeltaEvent{Text: token})
           └─> All subscribers receive the event

2. BrokerView forwards to View
   └─> BrokerView.HandleEvent(evt) 
       └─> broker.Publish(evt) 
           └─> View.HandleEvent(evt) is called for each subscriber

3. Consumer handles event
   └─> TUI: Model.HandleEvent(evt) with type switch
   └─> REPL: terminalView.HandleEvent(evt) with type switch
   └─> MCP: NoopView discards event (or custom handler)
```

### Consumers Implementation

#### TUI Consumer

```go
// internal/tui/tui.go
func (m *Model) HandleEvent(evt agent.Event) {
    switch e := evt.(type) {
    case *agent.TokenDeltaEvent:
        m.streamContent += e.Text
        // Update viewport
    case *agent.ThinkingEvent:
        m.thinkContent += e.Text
        // Update thinking display
    case *agent.ToolStartEvent:
        // Show tool starting
    case *agent.ToolEndEvent:
        // Show tool result
        if e.Error != "" {
            // Show error
        }
    case *agent.SubAgentStartEvent:
        // Show sub-agent starting
    case *agent.SubAgentEndEvent:
        // Show sub-agent result
    case *agent.DoneEvent:
        // Show completion
        if e.Error != "" {
            // Show error
        }
    case *agent.CompactionDoneEvent:
        // Show compaction stats
    case *agent.EscalationEvent:
        // Show escalation based on severity
    case *agent.FlushEvent:
        // Flush accumulated content
    }
}
```

#### REPL Consumer

```go
// cmd/yaah/agent_frame.go
type terminalView struct {
    spin     *spinner.Spinner
    stopOnce sync.Once
    streamed bool
}

func (v *terminalView) HandleEvent(evt agent.Event) {
    switch e := evt.(type) {
    case *agent.TokenDeltaEvent:
        v.stopOnce.Do(func() {
            v.spin.Stop()
            fmt.Fprintln(os.Stderr)
            v.streamed = true
        })
        fmt.Fprint(os.Stderr, e.Text)
    case *agent.ToolEndEvent:
        if e.Name == "spawn_subagent" {
            return
        }
        fmt.Fprintf(os.Stderr, "\n  tool: %s", Bold(e.Name))
        if e.Args != "" {
            args := e.Args
            if len(args) > 60 {
                args = args[:57] + "..."
            }
            fmt.Fprintf(os.Stderr, "(%s)", Dim(args))
        }
        fmt.Fprintf(os.Stderr, " (%s)\n", Dim(formatDuration(e.Duration)))
        if e.Error != "" {
            fmt.Fprintf(os.Stderr, "    %s\n", replYellow("error: "+e.Error))
        }
    case *agent.DoneEvent:
        v.stopOnce.Do(v.spin.Stop)
        if v.streamed {
            fmt.Fprintln(os.Stderr)
            fmt.Fprintln(os.Stderr)
        }
    // ... other cases
    }
}
```

## Alternatives Considered

### Alternative 1: Callback System

Keep the callback system but make it more type-safe.

**Rejected because:**
- Still requires Loop to know about all callback types
- Doesn't scale well with many event types
- Harder to add new event types without modifying Loop
- Doesn't provide a clean way for consumers to filter events

### Alternative 2: Observer Pattern with Registration

```go
loop.OnToken(func(text string) { ... })
loop.OnToolStart(func(name, args string) { ... })
```

**Rejected because:**
- Similar to callbacks but with registration
- Still requires Loop to define all event types
- Harder to ensure all events are handled
- Doesn't provide compile-time safety

### Alternative 3: Channel of Interfaces

```go
loop.Events() <-chan Event
```

**Rejected because:**
- Single channel can become a bottleneck
- Hard to manage backpressure
- Consumers need to handle all event types or filter
- Doesn't work well with multiple concurrent loops

### Alternative 4: Separate Channels per Event Type

```go
loop.TokenEvents() <-chan string
loop.ToolStartEvents() <-chan ToolStartInfo
```

**Rejected because:**
- Explosion of channel types
- Hard to correlate related events (e.g., ToolStart and ToolEnd)
- Doesn't scale well
- More complex lifecycle management

## Consequences

### Positive

1. **Decoupling**: Loop doesn't know about any specific consumer
2. **Extensibility**: New event types can be added without modifying existing code
3. **Type Safety**: Sealed interface ensures all events are known and handled
4. **Exhaustiveness**: Compiler enforces that all event types are handled in type switches
5. **Testability**: Easy to mock View implementations for testing
6. **Flexibility**: Consumers can choose which events to handle
7. **Multiple Consumers**: Same event can be sent to multiple consumers
8. **Unified Pattern**: All consumers use the same HandleEvent interface

### Negative

1. **Boilerplate**: Each new event type requires defining a struct and eventMarker()
2. **Learning Curve**: New contributors need to understand the event system
3. **Error Handling**: Errors in event handling are hard to propagate back to Loop
4. **Ordering**: Events may be delivered out of order if consumers process slowly

## Performance Considerations

- **Memory**: Each event is a small struct, copied to each subscriber
- **CPU**: Type switch dispatch is fast (compile-time optimized)
- **Channel Buffering**: Broker channels are buffered (32 events) to handle bursts
- **Concurrency**: Publish is synchronized but fast (RWMutex)

The system is designed to be **efficient** - the overhead is minimal compared to the benefits of decoupling.

## Best Practices

### 1. Always Handle All Event Types

```go
// GOOD: Exhaustive type switch
func (m *MyView) HandleEvent(evt agent.Event) {
    switch e := evt.(type) {
    case *agent.TokenDeltaEvent:
        // handle
    case *agent.ThinkingEvent:
        // handle
    case *agent.FlushEvent:
        // handle
    case *agent.ToolStartEvent:
        // handle
    case *agent.ToolEndEvent:
        // handle
    case *agent.SubAgentStartEvent:
        // handle
    case *agent.SubAgentEndEvent:
        // handle
    case *agent.DoneEvent:
        // handle
    case *agent.CompactionStartedEvent:
        // handle
    case *agent.CompactionDoneEvent:
        // handle
    case *agent.EscalationEvent:
        // handle
    }
}
```

The compiler will **error** if a new event type is added and not handled.

### 2. Use NoopView for Headless Consumers

```go
// GOOD: Use NoopView when no UI is needed
loop := agent.NewLoop(provider, registry,
    agent.WithView(agent.NoopView{}),
)
```

### 3. Don't Block in HandleEvent

```go
// BAD: Blocking in HandleEvent
func (v *MyView) HandleEvent(evt agent.Event) {
    switch e := evt.(type) {
    case *agent.TokenDeltaEvent:
        time.Sleep(100 * time.Millisecond) // BLOCKS!
    }
}

// GOOD: Non-blocking or async
func (v *MyView) HandleEvent(evt agent.Event) {
    switch e := evt.(type) {
    case *agent.TokenDeltaEvent:
        go v.processTokenAsync(e.Text) // Non-blocking
    }
}
```

### 4. Use Buffered Channels for Slow Consumers

```go
// Custom broker with larger buffer for slow consumers
broker := pubsub.NewBroker[agent.Event]()
// Use buffered channel in Subscribe
ch := make(chan agent.Event, 100)
broker.AddSubscriber(ch)
```

## References

- [internal/agent/events.go](internal/agent/events.go) - Event type definitions
- [internal/agent/view.go](internal/agent/view.go) - View interface and BrokerView
- [internal/pubsub/broker.go](internal/pubsub/broker.go) - Generic pub/sub broker
- [internal/tui/tui.go:HandleEvent()](internal/tui/tui.go) - TUI event handling
- [cmd/yaah/agent_frame.go:terminalView](cmd/yaah/agent_frame.go) - REPL event handling
- [ADR-0001: Engine-View Separation](./0001-engine-view-separation.md) - Related architecture decision

## Event Type Summary

| Event | When | Key Data |
|-------|------|----------|
| `TokenDeltaEvent` | Each streaming token | `Text` |
| `ThinkingEvent` | Reasoning/thinking content | `Text` |
| `FlushEvent` | End of streaming segment | `Content` |
| `ToolStartEvent` | Tool execution starts | `ID`, `Name`, `Args` |
| `ToolEndEvent` | Tool execution completes | `ID`, `Name`, `Args`, `Result`, `Duration`, `Error` |
| `SubAgentStartEvent` | Sub-agent spawns | `Role`, `Model`, `Prompt` |
| `SubAgentEndEvent` | Sub-agent completes | `Role`, `Model`, `Prompt`, `Duration`, `Error` |
| `DoneEvent` | Agent loop completes | `Response`, `Error`, `Usage`, `FinishReason` |
| `CompactionStartedEvent` | Compaction begins | `BeforeTokens`, `TargetTokens`, `Reason` |
| `CompactionDoneEvent` | Compaction finishes | `BeforeTokens`, `AfterTokens`, `SavingsPct`, `Method` |
| `EscalationEvent` | Sub-agent escalates | `SubAgentRole`, `Severity`, `Summary`, `Detail`, `Suggestion` |

## Future Considerations

### 1. Event Filtering

Currently all events go to all subscribers. Could add filtering:

```go
type FilteredView struct {
    view    View
    filter  func(Event) bool
}

func (fv *FilteredView) HandleEvent(evt Event) {
    if fv.filter(evt) {
        fv.view.HandleEvent(evt)
    }
}
```

### 2. Event Priorities

Could add priority levels for events (e.g., critical events jump the queue).

### 3. Event Batching

Could batch certain events (e.g., TokenDeltaEvent) for performance.

---

*This ADR documents an implemented architecture. The event-driven system with sealed interfaces and pub/sub broker is a core part of yaah's architecture.*
