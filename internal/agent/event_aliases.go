// Package agent is the core agent loop package. Event types live in the
// events sub-package; this file re-exports them so existing consumers using
// agent.Event / agent.SubAgentStartEvent etc. are unchanged.
package agent

import "github.com/buchenberg/yaah/internal/agent/events"

// Event types
type Event = events.Event
type TokenDeltaEvent = events.TokenDeltaEvent
type ThinkingEvent = events.ThinkingEvent
type FlushEvent = events.FlushEvent
type ToolStartEvent = events.ToolStartEvent
type ToolEndEvent = events.ToolEndEvent
type SubAgentStartEvent = events.SubAgentStartEvent
type SubAgentEndEvent = events.SubAgentEndEvent
type DoneEvent = events.DoneEvent
type CompactionStartedEvent = events.CompactionStartedEvent
type CompactionDoneEvent = events.CompactionDoneEvent
type EscalationEvent = events.EscalationEvent

// Hook types
type HookEmitter = events.HookEmitter
type HookEvent = events.HookEvent
type HookEventType = events.HookEventType

// NewHookEmitter is a thin wrapper around events.NewHookEmitter so external
// consumers are unchanged by the events sub-package extraction.
var NewHookEmitter = events.NewHookEmitter
