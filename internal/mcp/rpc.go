package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"
)

// mcpProtocolVersion is the MCP protocol version yaah speaks. Sent
// during the initialize handshake; a server choosing a different
// version is tolerated with a warning (review B13).
const mcpProtocolVersion = "2024-11-05"

// maxToolListPages caps tools/list pagination so a misbehaving server
// that keeps returning a nextCursor cannot loop forever.
const maxToolListPages = 100

// rpcTransport abstracts id-correlated request/response exchange for
// both transports: stdio (persistent stream + pending map) and HTTP
// (one round-trip per request).
type rpcTransport interface {
	roundTrip(ctx context.Context, id int64, method string, params json.RawMessage) (*JSONRPCMessage, error)
	// notify sends a notification; no response is expected.
	notify(ctx context.Context, method string, params json.RawMessage) error
}

// rpcCore carries the MCP protocol logic shared by both transports:
// initialize handshake with version negotiation, paginated tools/list,
// and tools/call (review B13).
type rpcCore struct {
	tx     rpcTransport
	nextID atomic.Int64
}

// call sends a request and returns the response, surfacing JSON-RPC
// errors as Go errors.
func (c *rpcCore) call(ctx context.Context, method string, params any) (*JSONRPCMessage, error) {
	id := c.nextID.Add(1)
	var raw json.RawMessage
	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("marshal %s params: %w", method, err)
		}
		raw = data
	}
	resp, err := c.tx.roundTrip(ctx, id, method, raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("%s error: %s", method, resp.Error.Message)
	}
	return resp, nil
}

// initialize performs the MCP handshake and sends the initialized
// notification. Returns the protocol version the server chose; a
// mismatch with our supported version is logged (review B13).
func (c *rpcCore) initialize(ctx context.Context) (string, error) {
	resp, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": mcpProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "yaah", "version": clientVersion},
	})
	if err != nil {
		return "", err
	}
	var result struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(resp.Result, &result)
	if result.ProtocolVersion != "" && result.ProtocolVersion != mcpProtocolVersion {
		slog.Warn("mcp: server negotiated a different protocol version",
			"supported", mcpProtocolVersion, "server", result.ProtocolVersion)
	}
	if err := c.tx.notify(ctx, "notifications/initialized", nil); err != nil {
		return "", fmt.Errorf("send initialized: %w", err)
	}
	return result.ProtocolVersion, nil
}

// fetchTools lists all server tools, following nextCursor pagination up
// to a sane page cap (review B13).
func (c *rpcCore) fetchTools(ctx context.Context) ([]ServerTool, error) {
	var all []ServerTool
	cursor := ""
	for page := 0; page < maxToolListPages; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		resp, err := c.call(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var result struct {
			Tools      []ServerTool `json:"tools"`
			NextCursor string       `json:"nextCursor"`
		}
		if err := json.Unmarshal(resp.Result, &result); err != nil {
			return nil, fmt.Errorf("parse tools: %w", err)
		}
		all = append(all, result.Tools...)
		if result.NextCursor == "" {
			return all, nil
		}
		cursor = result.NextCursor
	}
	slog.Warn("mcp: tools/list pagination hit the page cap; tool list may be truncated",
		"pages", maxToolListPages)
	return all, nil
}

// callTool invokes a remote tool.
func (c *rpcCore) callTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	resp, err := c.call(ctx, "tools/call", map[string]any{
		"name":      name,
		"arguments": json.RawMessage(args),
	})
	if err != nil {
		return nil, err
	}
	return resp.Result, nil
}
