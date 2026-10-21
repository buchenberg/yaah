// Package acp implements an agent (server) for the Agent Client
// Protocol v1 (https://agentclientprotocol.com): newline-delimited
// JSON-RPC 2.0 over stdio, the agent-event-to-update view translator,
// the control-channel policy for a headless client, and the
// session/request_permission bridge for tool approvals.
//
// Supported surface: initialize, session/new, session/load, session/prompt,
// session/cancel, session/set_mode, session/update notifications with
// the agent_message_chunk, agent_thought_chunk, user_message_chunk
// (replay), tool_call, tool_call_update, plan, and usage_update
// variants, and session/request_permission back to the client for
// tool approvals.
package acp

import "encoding/json"

// === JSON-RPC 2.0 wire types ===

// Message is a JSON-RPC 2.0 request, response, or notification. ID is
// kept raw so responses echo the peer's ID exactly (number vs string).
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is a JSON-RPC 2.0 error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Standard JSON-RPC error codes plus ACP-era custom range codes.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
	codeBusy           = -32002 // custom (JSON-RPC server-defined range)
)

// === initialize ===

// InitializeParams are the params of the client's initialize request.
type InitializeParams struct {
	ProtocolVersion    int                 `json:"protocolVersion"`
	ClientCapabilities *ClientCaps         `json:"clientCapabilities,omitempty"`
	ClientInfo         *ImplementationInfo `json:"clientInfo,omitempty"`
}

// ClientCaps are the capabilities the client advertised. Parsed so the
// server can decide, e.g., whether session/request_permission answers
// can be expected to include structured UI; nothing yet gates on them.
type ClientCaps struct {
	Fs       *FSCaps `json:"fs,omitempty"`
	Terminal bool    `json:"terminal,omitempty"`
}

// FSCaps is the client's file-system capability set.
type FSCaps struct {
	ReadTextFile  bool `json:"readTextFile,omitempty"`
	WriteTextFile bool `json:"writeTextFile,omitempty"`
}

// ImplementationInfo names one side of the connection.
type ImplementationInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version,omitempty"`
}

// InitializeResult is the result of the initialize handshake. The
// server speaks protocol version 1 only; authMethods is always empty
// so compliant clients will never send authenticate.
type InitializeResult struct {
	ProtocolVersion   int                `json:"protocolVersion"`
	AgentCapabilities AgentCaps          `json:"agentCapabilities"`
	AgentInfo         ImplementationInfo `json:"agentInfo"`
	AuthMethods       []AuthMethod       `json:"authMethods"`
}

// AgentCaps advertises what the server supports. loadSession is
// advertised because a fresh acp-serve process can rebuild a prior
// conversation from the SQLite session store (internal/memory).
// promptCapabilities extras, MCP transports, and session/resume are
// unsupported (Tier 3).
type AgentCaps struct {
	LoadSession bool `json:"loadSession,omitempty"`
}

// AuthMethod is a placeholder struct; the list is always empty.
type AuthMethod struct {
	Type string `json:"type,omitempty"`
}

// === session/new ===

// SessionNewParams are the params of session/new. Per spec, cwd MUST
// govern the session's filesystem context and mcpServers list servers
// the agent should connect to; both are processed here (cwd becomes the
// process working directory before the underlying session is built;
// mcpServers connection is Tier 3 and currently recorded but unused).
// Field must be absolute.
type SessionNewParams struct {
	Cwd        string         `json:"cwd,omitempty"`
	McpServers []McpServerRef `json:"mcpServers,omitempty"`
}

// McpServerRef is one client-declared MCP server. Only stdio is
// supported (the spec requires it of every agent); HTTP and SSE would
// need mcpCapabilities advertised first.
type McpServerRef struct {
	Type    string        `json:"type,omitempty"`
	Name    string        `json:"name,omitempty"`
	Command string        `json:"command,omitempty"`
	Args    []string      `json:"args,omitempty"`
	Env     []EnvVariable `json:"env,omitempty"`
}

// EnvVariable is one environment variable for an MCP server process.
type EnvVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// SessionNewResult is the result of session/new.
type SessionNewResult struct {
	SessionID string `json:"sessionId"`
}

// === session/load ===

// SessionLoadParams are the params of session/load. The client sends
// the ID of a conversation yaah previously created (the persisted
// memory-DB session id), optionally with a cwd and mcpServers: the cwd
// is applied to the process exactly as in session/new (first cwd
// wins), and mcpServers is recorded but unused (Tier 3). Unknown
// session IDs — ones yaah never created in its session store — are
// rejected with invalid params.
type SessionLoadParams struct {
	SessionID  string         `json:"sessionId"`
	Cwd        string         `json:"cwd,omitempty"`
	McpServers []McpServerRef `json:"mcpServers,omitempty"`
}

// === session/prompt ===

// PromptParams are the params of session/prompt.
type PromptParams struct {
	SessionID string         `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"`
}

// ContentBlock is one block of user prompt content. ACP blocks other
// than text/resource/resource_link are parsed leniently (raw) and
// represented textually so nothing is silently dropped.
type ContentBlock struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	URI      string          `json:"uri,omitempty"`      // resource_link
	Name     string          `json:"name,omitempty"`     // resource_link
	Resource *ResourceBundle `json:"resource,omitempty"` // embedded resource
	Raw      json.RawMessage `json:"-"`
}

// ResourceBundle is the inner resource of an embedded context block.
type ResourceBundle struct {
	URI      string `json:"uri,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
}

// PromptResult is the result of session/prompt — sent ONLY when the
// turn is over. StopReason values are defined below.
type PromptResult struct {
	StopReason string `json:"stopReason"`
}

// Stop reasons a turn can end with.
const (
	StopEndTurn   = "end_turn"
	StopCancelled = "cancelled"
	StopRefusal   = "refusal"
)

// === session/update notifications ===

// Update variants (snake_case values on a camelCase discriminator).
const (
	UpdateAgentMessageChunk = "agent_message_chunk"
	UpdateAgentThoughtChunk = "agent_thought_chunk"
	UpdateUserMessageChunk  = "user_message_chunk"
	UpdateToolCall          = "tool_call"
	UpdateToolCallUpdate    = "tool_call_update"
	UpdatePlan              = "plan"
	UpdateUsageUpdate       = "usage_update"
)

// Tool call statuses.
const (
	StatusPending    = "pending"
	StatusInProgress = "in_progress"
	StatusCompleted  = "completed"
	StatusFailed     = "failed"
)

// Tool kinds (session/update tool_call.kind).
const (
	KindRead       = "read"
	KindEdit       = "edit"
	KindDelete     = "delete"
	KindMove       = "move"
	KindSearch     = "search"
	KindExecute    = "execute"
	KindThink      = "think"
	KindFetch      = "fetch"
	KindSwitchMode = "switch_mode"
	KindOther      = "other"
)

// Plan entry priorities and statuses.
const (
	PriorityHigh   = "high"
	PriorityMedium = "medium"
	PriorityLow    = "low"

	PlanPending    = "pending"
	PlanInProgress = "in_progress"
	PlanCompleted  = "completed"
)

// ChunkUpdate is the payload for agent_message_chunk,
// agent_thought_chunk, and the user_message_chunk replay sent when a
// resumed conversation is streamed on session/load.
type ChunkUpdate struct {
	SessionUpdate string       `json:"sessionUpdate"`
	Content       ContentBlock `json:"content"`
}

// UpdateContent is one block of tool-call content (ACP type "content"
// wraps a standard content block; "diff" and "terminal" exist in the
// schema but are not produced by yaah yet).
type UpdateContent struct {
	Type    string       `json:"type"`
	Content ContentBlock `json:"content"`
}

// ToolCallUpdate is the payload for tool_call and tool_call_update.
// toolCallId is always set; on the initial tool_call report title and
// status are also set, and the toolCallId correlates later updates.
type ToolCallUpdate struct {
	SessionUpdate string          `json:"sessionUpdate"`
	ToolCallID    string          `json:"toolCallId"`
	Title         string          `json:"title,omitempty"`
	Name          string          `json:"name,omitempty"`
	Kind          string          `json:"kind,omitempty"`
	Status        string          `json:"status,omitempty"`
	Content       []UpdateContent `json:"content,omitempty"`
	RawInput      json.RawMessage `json:"rawInput,omitempty"`
}

// PlanEntry is one entry of a plan update.
type PlanEntry struct {
	Content  string `json:"content"`
	Priority string `json:"priority,omitempty"`
	Status   string `json:"status,omitempty"`
}

// PlanUpdate is the payload for the plan variant.
type PlanUpdate struct {
	SessionUpdate string      `json:"sessionUpdate"`
	Entries       []PlanEntry `json:"entries"`
}

// UsageUpdate is the payload for the usage_update variant.
type UsageUpdate struct {
	SessionUpdate string `json:"sessionUpdate"`
	Used          int    `json:"used"`
	Size          int    `json:"size"`
}

// updateEnvelope is the session/update notification params shape.
type updateEnvelope struct {
	SessionID string          `json:"sessionId"`
	Update    json.RawMessage `json:"update"`
}

// updateSender delivers one update for the given session. Implementations
// must be safe for concurrent use.
type updateSender func(sessionID string, update any)

// === session/request_permission (client method) ===

// PermissionOption is one choice offered to the client's user.
type PermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"` // allow_once | allow_always | reject_once | reject_always
}

// Permission option kinds.
const (
	PermAllowOnce    = "allow_once"
	PermAllowAlways  = "allow_always"
	PermRejectOnce   = "reject_once"
	PermRejectAlways = "reject_always"
)

// RequestPermissionParams are the params of session/request_permission.
// ToolCall carries the pending call's identifying details.
type RequestPermissionParams struct {
	SessionID string             `json:"sessionId"`
	ToolCall  ToolCallUpdate     `json:"toolCall"`
	Options   []PermissionOption `json:"options"`
}

// RequestPermissionResult is the client's decision.
type RequestPermissionResult struct {
	Outcome struct {
		Outcome  string `json:"outcome,omitempty"` // selected | cancelled
		OptionID string `json:"optionId,omitempty"`
	} `json:"outcome"`
}

// === session/set_mode and session/cancel ===

// SetModeParams are the params of session/set_mode.
type SetModeParams struct {
	SessionID string `json:"sessionId"`
	ModeID    string `json:"modeId"`
}

// CancelParams are the params of the session/cancel notification.
type CancelParams struct {
	SessionID string `json:"sessionId"`
}
