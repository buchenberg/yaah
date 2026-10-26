// Package acp implements an agent (server) for the Agent Client
// Protocol v1 (https://agentclientprotocol.com): newline-delimited
// JSON-RPC 2.0 over stdio, the agent-event-to-update view translator,
// the control-channel policy for a headless client, and the
// session/request_permission bridge for tool approvals.
//
// Supported surface: initialize, authenticate, session/new, session/load,
// session/prompt, session/cancel, session/set_mode,
// session/set_config_option, session/update notifications with
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
	codeBusy           = -32002 // no prompt turn may overlap; retry after it ends
	codeInternal       = -32603
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
// server speaks protocol version 1 only and advertises a static catalog
// of agent-typed auth methods (staticAuthMethods); a client may answer
// with authenticate naming one of them.
type InitializeResult struct {
	ProtocolVersion   int                `json:"protocolVersion"`
	AgentCapabilities AgentCaps          `json:"agentCapabilities"`
	AgentInfo         ImplementationInfo `json:"agentInfo"`
	AuthMethods       []AuthMethod       `json:"authMethods"`
}

// AgentCaps advertises what the server supports. loadSession is
// advertised because a fresh acp-serve process can rebuild a prior
// conversation from the SQLite session store (internal/memory);
// Session is advertised with the lifecycle methods the server can
// actually serve (below). promptCapabilities extras, MCP transports,
// additionalDirectories, and session modes are unsupported (Tier 3);
// configOptions are not a capability — they ride on the session
// results and session/set_config_option, and are emitted only when a
// model catalog is wired (see ModelCatalog / NewServer).
type AgentCaps struct {
	LoadSession bool         `json:"loadSession,omitempty"`
	Session     *SessionCaps `json:"sessionCapabilities,omitempty"`
}

// SessionCaps are the optional session-method capabilities.
// session/load itself is governed by LoadSession (legacy binding).
// Each field is a pointer: nil means "not advertised", an empty struct
// means advertised with no options.
type SessionCaps struct {
	List   *SessionListCaps   `json:"list,omitempty"`
	Resume *SessionResumeCaps `json:"resume,omitempty"`
	Close  *SessionCloseCaps  `json:"close,omitempty"`
	Delete *SessionDeleteCaps `json:"delete,omitempty"`
}

// SessionListCaps advertise session/list. No options exist yet; the
// empty struct flips the capability.
type SessionListCaps struct{}

// SessionResumeCaps advertise session/resume (restore without replay).
type SessionResumeCaps struct{}

// SessionCloseCaps advertise session/close (cancel-then-free, sessions
// are no longer usable afterwards).
type SessionCloseCaps struct{}

// SessionDeleteCaps advertise session/delete (remove from the session
// store; unknown ids succeed silently).
type SessionDeleteCaps struct{}

// AuthMethod is one entry of the initialize response's authMethods
// catalog. The schema's terminal variant requires `type: "terminal"`;
// the agent variant has no `type` at all, and a method without one is
// treated as agent-typed. yaah advertises agent-typed methods only (see
// auth.go), so Type stays empty and omitempty drops it — never set it to
// "terminal" unless the client enabled clientCapabilities.auth.terminal.
type AuthMethod struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Type        string `json:"type,omitempty"`
}

// AuthenticateParams are the params of the authenticate request.
// methodId must name one of the methods the initialize response
// advertised.
type AuthenticateParams struct {
	MethodID string `json:"methodId"`
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

// SessionNewResult is the result of session/new. configOptions carries
// the session's configuration selectors (the model picker) and is
// omitted when the session has none to advertise — no model catalog is
// wired, or discovery returned nothing.
type SessionNewResult struct {
	SessionID     string                `json:"sessionId"`
	ConfigOptions []SessionConfigOption `json:"configOptions,omitempty"`
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

// SessionLoadResult is the result of session/load. Attaching to a
// persisted conversation re-advertises its configOptions exactly like
// session/new, so a client restored into an existing conversation shows
// the same model picker without a separate request.
type SessionLoadResult struct {
	ConfigOptions []SessionConfigOption `json:"configOptions,omitempty"`
}

// === session/list ===

// SessionInfo is one entry of a session/list result: enough metadata for
// a client to show a session picker. cwd is the canonical absolute path
// the session was created with; updatedAt is the ISO 8601 timestamp of
// the last persisted activity; _meta carries yaah's extensions.
type SessionInfo struct {
	SessionID string       `json:"sessionId"`
	Cwd       string       `json:"cwd,omitempty"`
	Title     string       `json:"title,omitempty"`
	UpdatedAt string       `json:"updatedAt,omitempty"`
	Meta      *SessionMeta `json:"_meta,omitempty"`
}

// SessionMeta is the `_meta` extension object of a SessionInfo entry. ACP
// reserves `_meta` for implementation-defined extras; yaah puts the
// picker's message count there so a client can show session size without
// loading anything. The object is omitted entirely when empty, and
// messageCount is omitted when zero (a session with no persisted
// messages).
type SessionMeta struct {
	MessageCount int `json:"messageCount,omitempty"`
}

// ListSessionsResult is the result of session/list. nextCursor, when
// non-empty, is the opaque token for the following page; omitting it
// (empty here) marks the end of the pages.
type ListSessionsResult struct {
	Sessions   []SessionInfo `json:"sessions"`
	NextCursor string        `json:"nextCursor,omitempty"`
}

// === session lifecycle (resume / close / delete) ===

// SessionResumeParams are the params of session/resume: re-attach to a
// persisted conversation without replaying its history. They mirror the
// session/load params (sessionId required; cwd and mcpServers optional).
type SessionResumeParams struct {
	SessionID  string         `json:"sessionId"`
	Cwd        string         `json:"cwd,omitempty"`
	McpServers []McpServerRef `json:"mcpServers,omitempty"`
}

// SessionTargetParams are the params of session/close and session/delete:
// the only input is the ACP session id.
type SessionTargetParams struct {
	SessionID string `json:"sessionId"`
}

// ListSessionsParams are the params of session/list. cwd (optional)
// filters by exact working directory; cursor pages through the result.
type ListSessionsParams struct {
	Cwd    string `json:"cwd,omitempty"`
	Cursor string `json:"cursor,omitempty"`
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
	// StopMaxTurnRequests reports that the turn ended because the agent
	// exhausted its request budget (the soft tool-turn cap or the hard
	// iteration limit) rather than choosing to finish. Clients use it to
	// offer a continuation instead of treating the turn as complete.
	StopMaxTurnRequests = "max_turn_requests"
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
	UpdateSessionInfo       = "session_info_update"
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

// SessionInfoUpdate is a partial session-metadata update (schema: all
// fields optional, null clears). yaah never clears; it sends the title
// when the persisted session title transitions from empty, with
// updatedAt as the RFC3339 UTC timestamp of the message that produced
// it (session/list emits the same values from the store).
type SessionInfoUpdate struct {
	SessionUpdate string `json:"sessionUpdate"`
	Title         string `json:"title,omitempty"`
	UpdatedAt     string `json:"updatedAt,omitempty"`
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

// === session/set_config_option ===

// SetConfigOptionParams are the params of session/set_config_option:
// configId names one of the session's configOptions, and value is one
// of that option's values. The model picker is yaah's only config
// option, and its values are "<provider>/<model>".
//
// value is a string, not the spec's string|boolean union: yaah
// advertises select options only, so a boolean value is a client error
// and fails decoding as invalid params.
type SetConfigOptionParams struct {
	SessionID string `json:"sessionId"`
	ConfigID  string `json:"configId"`
	Value     string `json:"value"`
}

// SetConfigOptionResult is the result of session/set_config_option: the
// session's whole configOptions set with currentValue updated, so the
// client re-renders every picker from one response. The field is not
// omitempty — the spec requires it — but it is nil when no catalog is
// wired (the same shape session/new reports).
type SetConfigOptionResult struct {
	ConfigOptions []SessionConfigOption `json:"configOptions"`
}

// SessionConfigOption is one session configuration option: a `select`
// of a single current value, keyed by id. yaah advertises exactly one
// (the model picker) and only when a model catalog is wired.
//
// The wire shape flattens the kind into the option, per the spec: id,
// name, description and category alongside type="select",
// currentValue and options.
type SessionConfigOption struct {
	ID           string                      `json:"id"`
	Name         string                      `json:"name"`
	Description  string                      `json:"description,omitempty"`
	Category     string                      `json:"category,omitempty"`
	Type         string                      `json:"type"`
	CurrentValue string                      `json:"currentValue"`
	Options      []SessionConfigSelectOption `json:"options"`
}

// SessionConfigSelectOption is one selectable value of a select-style
// config option. value is what session/set_config_option must echo
// back; name is the human label.
type SessionConfigSelectOption struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Config option types, categories, and the id of the model picker.
const (
	// ConfigOptionTypeSelect is the only type yaah emits. The spec's
	// "boolean" type requires the client to advertise
	// session.configOptions.boolean, which yaah never emits.
	ConfigOptionTypeSelect = "select"

	// ConfigOptionCategoryModel is the spec's reserved category for a
	// model selector; clients use it for placement and shortcuts.
	ConfigOptionCategoryModel = "model"

	// ConfigOptionModelID is the id of the model picker: the only
	// configId session/set_config_option accepts.
	ConfigOptionModelID = "model"
)
