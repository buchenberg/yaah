package events

// HookEventType identifies the kind of hook event emitted to the hook directory.
type HookEventType string

const (
	SessionStart   HookEventType = "session.start"
	SessionEnd     HookEventType = "session.end"
	TurnStart      HookEventType = "turn.start"
	ToolStart      HookEventType = "tool.start"
	ToolEnd        HookEventType = "tool.end"
	ConflictCheck  HookEventType = "conflict.check"
	ConflictDetect HookEventType = "conflict.detect"
	ContextPrune   HookEventType = "context.prune"
)

// HookEvent is a structured event emitted to the hook directory as JSONL.
// The entire-agent-yaah binary reads these for transcript and session analysis.
type HookEvent struct {
	Event     HookEventType `json:"event"`
	SessionID string        `json:"session_id"`
	Timestamp int64         `json:"timestamp_ms"`

	// session.start / session.end
	Model string `json:"model,omitempty"`

	// turn.start
	Prompt string `json:"prompt,omitempty"`
	Turn   int    `json:"turn,omitempty"`

	// tool.start / tool.end
	ToolName string `json:"tool_name,omitempty"`
	ToolArgs string `json:"tool_args,omitempty"` // JSON string
	// tool.end only
	ToolResult string `json:"tool_result,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
	ToolError  string `json:"tool_error,omitempty"`

	// session.end only
	ExitReason string `json:"exit_reason,omitempty"`

	// conflict.check / conflict.detect
	ConflictFiles int `json:"conflict_files,omitempty"`

	// context.prune — soft-prune outcome. Emitted on every PostTool mark,
	// even when Committed is false (so "considered, decided not to" is visible).
	PruneReason      string `json:"prune_reason,omitempty"`
	PruneCandidates  int    `json:"prune_candidates,omitempty"`
	PruneMarked      int    `json:"prune_marked,omitempty"`
	PruneReclaimed   int    `json:"prune_reclaimed,omitempty"`
	PruneProtected   int    `json:"prune_protected,omitempty"`
	PruneCommitted   bool   `json:"prune_committed,omitempty"`
	PruneTotalMarked int    `json:"prune_total_marked,omitempty"`
}
