package agent

import (
	"time"

	"github.com/buchenberg/yaah/internal/types"
)

// =============================================================================
// Typed event system (Phase 1 — new API)
// =============================================================================
//
// Event is a sealed interface for all agent events. Each event type is a
// concrete struct implementing eventMarker(). Consumers use type switches:
//
//	switch e := evt.(type) {
//	case *agent.TokenDeltaEvent:
//	    // handle token delta
//	case *agent.ToolEndEvent:
//	    // handle tool completion
//	}
//
// Pointer receivers are used so nil checks work naturally in type switches.

// Event is the interface all agent events satisfy.
// The interface is sealed via the unexported eventMarker method.
type Event interface {
	eventMarker()
}

// TokenDeltaEvent is emitted for each streamed token from the model.
type TokenDeltaEvent struct{ Text string }

func (*TokenDeltaEvent) eventMarker() {}

// ThinkingEvent is emitted when the model outputs reasoning/thinking text
// (e.g. DeepSeek R1 reasoning tokens, Anthropic extended thinking).
type ThinkingEvent struct{ Text string }

func (*ThinkingEvent) eventMarker() {}

// FlushEvent is emitted when the model finishes a streaming segment and
// is about to start a tool call or a new iteration. The view should flush
// accumulated streaming content to the message list.
type FlushEvent struct{ Content string }

func (*FlushEvent) eventMarker() {}

// ToolStartEvent is emitted before a tool begins execution.
type ToolStartEvent struct {
	Name string // tool name (e.g. "read", "grep")
	Args string // abbreviated arguments for display
}

func (*ToolStartEvent) eventMarker() {}

// ToolEndEvent is emitted after a tool completes execution.
type ToolEndEvent struct {
	Name     string        // tool name
	Args     string        // abbreviated arguments
	Result   string        // truncated result
	Duration time.Duration // execution duration
	Error    string        // error message (empty on success)
}

func (*ToolEndEvent) eventMarker() {}

// SubAgentStartEvent is emitted when a sub-agent (spawn_subagent) begins.
type SubAgentStartEvent struct {
	Role   string // sub-agent role (e.g. "developer", "analyst")
	Model  string // model assigned to the sub-agent
	Prompt string // abbreviated task description
}

func (*SubAgentStartEvent) eventMarker() {}

// SubAgentEndEvent is emitted when a sub-agent completes.
type SubAgentEndEvent struct {
	Role     string        // sub-agent role
	Model    string        // model used by the sub-agent
	Prompt   string        // abbreviated task description
	Duration time.Duration // total execution duration
	Error    string        // error message (empty on success)
}

func (*SubAgentEndEvent) eventMarker() {}

// DoneEvent is emitted when the agent loop completes (success or error).
// It carries the final response text (if any), error information,
// context window statistics for the status bar, finish reason from the last
// turn, cumulative token usage, and the response model string.
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

// CompactionStartedEvent is emitted when context compaction begins.
type CompactionStartedEvent struct {
	BeforeTokens int
	TargetTokens int
	Reason       string // "threshold", "overflow", "budget-only"
}

func (*CompactionStartedEvent) eventMarker() {}

// CompactionDoneEvent is emitted when context compaction finishes.
type CompactionDoneEvent struct {
	BeforeTokens    int
	AfterTokens     int
	SavingsPct      float64
	Method          string // "single", "chunked"
	ElapsedSeconds  float64
	IneffectiveNote string // non-empty when compaction was ineffective
}

func (*CompactionDoneEvent) eventMarker() {}

// Compile-time interface satisfaction checks.
var (
	_ Event = (*TokenDeltaEvent)(nil)
	_ Event = (*ThinkingEvent)(nil)
	_ Event = (*FlushEvent)(nil)
	_ Event = (*ToolStartEvent)(nil)
	_ Event = (*ToolEndEvent)(nil)
	_ Event = (*SubAgentStartEvent)(nil)
	_ Event = (*SubAgentEndEvent)(nil)
	_ Event = (*DoneEvent)(nil)
	_ Event = (*CompactionStartedEvent)(nil)
	_ Event = (*CompactionDoneEvent)(nil)
)
