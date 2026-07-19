package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
)

// HTTPClient implements the MCP protocol over HTTP (Streamable HTTP transport).
// Requests are sent as HTTP POST to the server URL; responses come back as
// JSON-RPC in the response body.
type HTTPClient struct {
	name      string
	url       string
	client    *http.Client
	nextID    atomic.Int64
	mu        sync.Mutex
	tools     []ServerTool
	sessionID string
}

// NewHTTPClient creates a new MCP HTTP client.
func NewHTTPClient(name, url string) *HTTPClient {
	return &HTTPClient{
		name:   name,
		url:    url,
		client: &http.Client{},
	}
}

// Initialize performs the MCP initialize handshake over HTTP.
func (c *HTTPClient) Initialize(ctx context.Context) error {
	id := c.nextID.Add(1)
	params, _ := json.Marshal(map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "yaah", "version": "0.3.0"},
	})

	resp, err := c.sendRequest(ctx, JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "initialize",
		Params:  params,
	})
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("initialize error: %s", resp.Error.Message)
	}

	// Send initialized notification
	_, _ = c.sendRequest(ctx, JSONRPCMessage{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	})

	// Fetch tools
	return c.fetchTools(ctx)
}

func (c *HTTPClient) fetchTools(ctx context.Context) error {
	id := c.nextID.Add(1)
	resp, err := c.sendRequest(ctx, JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "tools/list",
	})
	if err != nil {
		return fmt.Errorf("tools/list: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("tools/list error: %s", resp.Error.Message)
	}

	var result struct {
		Tools []ServerTool `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return fmt.Errorf("parse tools: %w", err)
	}

	for i := range result.Tools {
		result.Tools[i].ServerName = c.name
	}
	c.tools = result.Tools
	return nil
}

// CallTool invokes a tool on the MCP server over HTTP.
func (c *HTTPClient) CallTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	id := c.nextID.Add(1)
	params, _ := json.Marshal(map[string]any{
		"name":      name,
		"arguments": json.RawMessage(args),
	})

	resp, err := c.sendRequest(ctx, JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "tools/call",
		Params:  params,
	})
	if err != nil {
		return nil, fmt.Errorf("tools/call: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("tools/call error: %s", resp.Error.Message)
	}

	return resp.Result, nil
}

// Tools returns the tools discovered from this server.
func (c *HTTPClient) Tools() []ServerTool {
	return c.tools
}

// Info returns server status details.
func (c *HTTPClient) Info() ServerInfo {
	return ServerInfo{
		Name:      c.name,
		Transport: "http",
		URL:       c.url,
		Connected: c.sessionID != "" || len(c.tools) > 0,
		ToolCount: len(c.tools),
	}
}

// Close is a no-op for HTTP clients (no persistent connection).
func (c *HTTPClient) Close() error {
	return nil
}

// sendRequest sends a JSON-RPC message via HTTP POST and returns the response.
func (c *HTTPClient) sendRequest(ctx context.Context, msg JSONRPCMessage) (*JSONRPCMessage, error) {
	body, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if c.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", c.sessionID)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Capture session ID from response
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.sessionID = sid
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// Handle 202 Accepted (notification acknowledged, no body)
	if resp.StatusCode == http.StatusAccepted {
		return &JSONRPCMessage{JSONRPC: "2.0"}, nil
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	// Try parsing as JSON-RPC response
	var result JSONRPCMessage
	if err := json.Unmarshal(respBody, &result); err != nil {
		// Some servers return SSE streams — try parsing the first event
		return parseSSEFirstEvent(respBody)
	}

	return &result, nil
}

// parseSSEFirstEvent extracts the first JSON-RPC message from an SSE stream.
func parseSSEFirstEvent(data []byte) (*JSONRPCMessage, error) {
	lines := bytes.Split(data, []byte("\n"))
	for _, line := range lines {
		if bytes.HasPrefix(line, []byte("data: ")) {
			payload := bytes.TrimPrefix(line, []byte("data: "))
			var msg JSONRPCMessage
			if err := json.Unmarshal(payload, &msg); err == nil {
				return &msg, nil
			}
		}
	}
	return nil, fmt.Errorf("no valid JSON-RPC message in response: %s", string(data))
}
