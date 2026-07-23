---
name: phase2-remove-callbacks
description: Phase 2: Remove callbacks from Loop, switch Broker to typed Event, wire View consumers
status: approved
---

## Goal
Remove all callback fields from `agent.Loop` (OnToken, OnTool, OnSubAgent, OnThinking, OnFlush), remove legacy `AgentEvent`/`EventType` types, switch `Loop.Broker` to `*pubsub.Broker[Event]`, and update all consumers to use the Broker+BrokerView pattern.

## Changes

### 1. `internal/agent/events.go` — Remove legacy types
- Remove `EventType` type and all `Event*` constants (7 items)
- Remove `AgentEvent` struct
- These were marked deprecated in Phase 1

### 2. `internal/agent/agent.go` — Remove callbacks, switch Broker type
- Remove fields: `OnToken`, `OnTool`, `OnSubAgent`, `OnThinking`, `OnFlush`
- Remove type aliases: `TokenCallback`, `ToolCallback`, `SubAgentCallback`, `ThinkingCallback`, `FlushCallback`
- Change `Broker *pubsub.Broker[AgentEvent]` → `Broker *pubsub.Broker[Event]`
- In `applyDefaults()`: publish typed events directly to Broker (no callback wrapping)
- In `runMiddleware()`: publish `FlushEvent` instead of legacy AgentEvent, remove OnFlush call

### 3. `internal/agent/agent_tools.go` — Use typed events
- Remove all `l.OnTool` and `l.OnSubAgent` callback calls
- Replace `AgentEvent{Type: EventToolStart, ...}` with `&ToolStartEvent{...}`
- Replace `AgentEvent{Type: EventToolEnd, ...}` with `&ToolEndEvent{...}`
- Replace `AgentEvent{Type: EventSubAgentStart, ...}` with `&SubAgentStartEvent{...}`
- Replace `AgentEvent{Type: EventSubAgentEnd, ...}` with `&SubAgentEndEvent{...}`

### 4. `internal/agent/events_test.go` — Remove legacy tests
- Remove `TestLegacyTypesExist`

### 5. `internal/agent/agent_test.go` — Fix callback usage
- Replace OnToken/OnThinking callbacks with Broker+View pattern in test

### 6. `cmd/yaah/agent_frame.go` — TerminalView consumer
- Create a `TerminalView` struct implementing `agent.View` 
- Create `pubsub.Broker[agent.Event]`, wrap with `BrokerView`
- Set `loop.Broker = broker` instead of setting callbacks
- HandleEvent does what callbacks currently do (print tokens, tool calls, sub-agent markers)

### 7. `cmd/yaah/subagent_runner.go` — SubAgentView consumer
- Replace `SubToolCallback` with Broker-based view for sub-agent tool display
- Sub-agents create their own Broker if they need tool display

### 8. `cmd/yaah/tui.go` — Update convertAgentEvent
- Change `pubsub.NewBroker[agent.AgentEvent]()` → `pubsub.NewBroker[agent.Event]()`
- Update `convertAgentEvent` to type-switch on `agent.Event`

### 9. Build, test, vet
- `go build ./...`
- `go test ./... -count=1`
- `go vet ./...`
