// Package acp implements the ACP (Agent Communication Protocol) server:
// newline-delimited JSON-RPC 2.0 over stdio, the agent-event-to-update
// view translator, and the control-channel policy (auto-answer,
// auto-continue) for machine-to-machine clients.
package acp

import "encoding/json"

// === JSON-RPC wire types for ACP ===

// Message is a JSON-RPC 2.0 request, response, or notification.
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is a JSON-RPC 2.0 error object.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// InitializeResult is the result of the initialize handshake.
type InitializeResult struct {
	ProtocolVersion string     `json:"protocol_version"`
	Capabilities    ServerCaps `json:"capabilities"`
	ServerInfo      ServerInfo `json:"server_info"`
	Instructions    string     `json:"instructions,omitempty"`
}

// ServerCaps describes server capabilities.
type ServerCaps struct {
	Tools     *ToolsCaps `json:"tools,omitempty"`
	Resources *struct{}  `json:"resources,omitempty"`
}

// ToolsCaps describes tool-related capabilities.
type ToolsCaps struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// ServerInfo identifies the server implementation.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// SessionNewResult is the result of session/new.
type SessionNewResult struct {
	SessionID string     `json:"sessionId"`
	Modes     *ModeState `json:"modes,omitempty"`
}

// ModeState describes the session's current and available modes.
type ModeState struct {
	CurrentModeID  string `json:"currentModeId"`
	AvailableModes []Mode `json:"availableModes"`
}

// Mode is a selectable agent mode.
type Mode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// PromptParams are the params of session/prompt.
type PromptParams struct {
	SessionID string         `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"`
}

// ContentBlock is one block of prompt content.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// ToolListEntry is one tool in a tools/list result.
type ToolListEntry struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// Update is a session/update notification payload.
type Update struct {
	SessionUpdate string      `json:"sessionUpdate"`
	Content       *Content    `json:"content,omitempty"`
	ToolCall      *ToolCall   `json:"tool_call,omitempty"`
	ToolResult    *ToolResult `json:"tool_result,omitempty"`
}

// Content is text content carried by an update.
type Content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ToolCall is a tool_call update payload.
type ToolCall struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Args   string `json:"args"`
	Status string `json:"status"`
}

// ToolResult is a tool_result update payload.
type ToolResult struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Result  string `json:"result,omitempty"`
	Error   string `json:"error,omitempty"`
	Ms      int64  `json:"ms,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// SessionUpdate wraps an update with its session ID.
type SessionUpdate struct {
	SessionID string `json:"sessionId"`
	Update    Update `json:"update"`
}

// SessionUpdateMsg is a session/update JSON-RPC notification.
type SessionUpdateMsg struct {
	JSONRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  SessionUpdate `json:"params"`
}
