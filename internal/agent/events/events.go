// Package events defines the typed event system for the agent loop. Each
// event type satisfies the sealed Event interface and is delivered to
// agent.View implementations and the internal pubsub broker.
package events

import (
	"fmt"
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
	ID      int64  // unique per-execution ID, shared with the matching ToolEndEvent
	CallID  string // provider tool_call id, when the model supplied one
	Name    string // tool name (e.g. "read", "grep")
	Args    string // abbreviated arguments for display
	RawArgs string // full, unabbreviated JSON arguments (views derive file locations from it)
}

func (*ToolStartEvent) eventMarker() {}

// ToolEndEvent is emitted after a tool completes execution.
type ToolEndEvent struct {
	ID       int64         // unique per-execution ID, shared with the matching ToolStartEvent
	CallID   string        // provider tool_call id, when the model supplied one
	Name     string        // tool name
	Args     string        // abbreviated arguments
	RawArgs  string        // full, unabbreviated JSON arguments (edit-class views derive diffs from it)
	Result   string        // truncated result
	Duration time.Duration // execution duration
	Error    string        // error message (empty on success)
}

func (*ToolEndEvent) eventMarker() {}

// SubAgentStartEvent is emitted when a sub-agent (delegate) begins.
type SubAgentStartEvent struct {
	SubAgentID string // unique sub-agent identifier ("sa-N" foreground, "bg-N" background)
	Role       string // sub-agent role (e.g. "developer", "analyst")
	Model      string // model assigned to the sub-agent
	Prompt     string // abbreviated task description
	// World is the resolved dispatch world ("worktree", "container", or
	// "shared"). Plain strings keep the events package free of a subagent
	// import; empty means unresolved and every surface renders nothing.
	World         string
	WorldDegraded bool
	WorldReason   string
}

func (*SubAgentStartEvent) eventMarker() {}

// SubAgentEndEvent is emitted when a sub-agent completes.
type SubAgentEndEvent struct {
	SubAgentID string        // matches the SubAgentStartEvent's SubAgentID
	Role       string        // sub-agent role
	Model      string        // model used by the sub-agent
	Prompt     string        // abbreviated task description
	Duration   time.Duration // total execution duration
	Error      string        // error message (empty on success)
	Result     string        // truncated final result (empty on error or background)
	// World mirrors SubAgentStartEvent's resolved dispatch world.
	World         string
	WorldDegraded bool
	WorldReason   string
}

func (*SubAgentEndEvent) eventMarker() {}

// EscalationEvent is emitted when a sub-agent raises a structured escalation
// (blocker, critical, warning, or info). The orchestrator should inspect the
// severity and decide whether to halt sibling sub-agents or report to the user.
type EscalationEvent struct {
	SubAgentRole   string // sub-agent role (e.g. "developer", "analyst")
	SubAgentPrompt string // task description
	Severity       string // info | warning | blocker | critical
	Summary        string // one-line summary of the issue
	Detail         string // full explanation
	Suggestion     string // recommended next step
}

func (*EscalationEvent) eventMarker() {}

// TitleSetEvent is emitted when a session's persisted title transitions
// from empty (the first user message that derives one — mirroring
// TouchSession's one-shot rule in the store). UpdatedAt is Unix seconds.
// Consumers forward it so clients show live session names; it fires at
// most once per untitled session.
type TitleSetEvent struct {
	Title     string
	UpdatedAt int64
}

func (*TitleSetEvent) eventMarker() {}

// AgentChangedEvent is emitted at session start when the session runs as a
// persona other than the default build orchestrator (yaah-21uk.10), and on
// every mid-session persona switch including the deprecated plan/build mode
// aliases (yaah-21uk.12 part 2 removed ModeChangedEvent — persona is the
// only axis). Views can show which persona the session actually is
// ("Session persona: plan") instead of the identity being invisible. Plain
// build sessions emit nothing at start — build is the silent default; a
// switch TO build is announced, because the event itself is the change.
type AgentChangedEvent struct {
	// Persona is the resolved persona name ("plan", or a registry
	// persona's file stem).
	Persona string
	// ReadOnly reports that the persona's session must not mutate the
	// workspace (the synthetic plan persona).
	ReadOnly bool
	// Detail is the optional clause appended after the persona in the
	// notice — the regime sentence the next run enforces. Empty on the
	// session-start announcement, which keeps its short wording.
	Detail string
}

func (*AgentChangedEvent) eventMarker() {}

// Notice renders the one-line in-band notice every view shows, so all
// consumers describe the persona identically (mirrors StartedSummary). A
// switch notice carries the Detail clause (regime sentence, deprecation);
// the session-start announcement keeps the bare wording with the
// read-only marker.
func (e *AgentChangedEvent) Notice() string {
	n := "Session persona: " + e.Persona
	if e.Detail != "" {
		return n + " — " + e.Detail
	}
	if e.ReadOnly {
		n += " (read-only)"
	}
	return n
}

// WakeEvent announces a harness wake: an armed watch fired between turns and
// the session is about to run the injected prompt. Text is that prompt,
// including the predicate's evidence, so every view can show what woke the
// session rather than only the model's response.
type WakeEvent struct {
	Text string
}

func (*WakeEvent) eventMarker() {}

// DoneEvent is emitted when the agent loop completes (success or error).
// It carries the final response text (if any), error information,
// context window statistics for the status bar, finish reason from the last
// turn, cumulative token usage, and the response model string.
//
// ContextTokens is the char/4 estimate of the full conversation history.
// LastPromptTokens is the provider-shaped prompt count for the last turn,
// used by compaction's raw gate. LastPromptTokensInclusive is the
// cache-inclusive context size; views should prefer it. (Compaction
// separately subtracts the cached subset.)
type DoneEvent struct {
	Response                  string
	Error                     string
	ContextTokens             int // char/4 estimate of all messages
	ContextWindow             int
	LastPromptTokens          int // provider-shaped prompt tokens (compaction raw gate)
	LastPromptTokensInclusive int // cache-inclusive provider prompt size (views)
	FinishReason              string
	Usage                     types.Usage
	ResponseModel             string
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
	OldMsgCount     int    // messages compacted (summarized or dropped)
	KeepMsgCount    int    // messages preserved verbatim
	Budget          int    // preserve-budget tokens for the tail
}

func (*CompactionDoneEvent) eventMarker() {}

// StartedSummary renders the single shared compaction-start line used
// by every view. Views previously formatted three divergent variants
// of this message (review B12).
func (e *CompactionStartedEvent) StartedSummary() string {
	return fmt.Sprintf("compacting (%d→%d tokens, %s)", e.BeforeTokens, e.TargetTokens, e.Reason)
}

// DoneSummary renders the single shared compaction-done line used by
// every view (review B12).
func (e *CompactionDoneEvent) DoneSummary() string {
	note := ""
	if e.IneffectiveNote != "" {
		note = " " + e.IneffectiveNote
	}
	return fmt.Sprintf("compacted %.0f%% (%d→%d tokens, %s, %.1fs)%s",
		e.SavingsPct*100, e.BeforeTokens, e.AfterTokens, e.Method, e.ElapsedSeconds, note)
}

// Compile-time interface satisfaction checks.
var (
	_ Event = (*TokenDeltaEvent)(nil)
	_ Event = (*ThinkingEvent)(nil)
	_ Event = (*FlushEvent)(nil)
	_ Event = (*ToolStartEvent)(nil)
	_ Event = (*ToolEndEvent)(nil)
	_ Event = (*SubAgentStartEvent)(nil)
	_ Event = (*SubAgentEndEvent)(nil)
	_ Event = (*AgentChangedEvent)(nil)
	_ Event = (*WakeEvent)(nil)
	_ Event = (*DoneEvent)(nil)
	_ Event = (*CompactionStartedEvent)(nil)
	_ Event = (*CompactionDoneEvent)(nil)
)
