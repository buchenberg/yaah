package agent

import "time"

type EventType int

const (
	EventTokenDelta EventType = iota
	EventThinking
	EventFlush
	EventToolStart
	EventToolEnd
	EventSubAgentStart
	EventSubAgentEnd
)

type AgentEvent struct {
	Type EventType

	Content string

	ToolName     string
	ToolArgs     string
	ToolResult   string
	ToolDuration time.Duration
	ToolError    string

	SubAgentRole     string
	SubAgentModel    string
	SubAgentPrompt   string
	SubAgentDuration time.Duration
	SubAgentError    string
}
