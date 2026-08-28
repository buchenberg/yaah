package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// mcpHTTPTimeout bounds each HTTP transport round-trip. Generous by
// design: MCP tool calls can legitimately run long, but a hung server
// must not stall the caller forever (requests may carry a bare
// background context with no deadline of its own).
const mcpHTTPTimeout = 5 * time.Minute

// HTTPClient implements the MCP protocol over HTTP (Streamable HTTP transport).
// Requests are sent as HTTP POST to the server URL; responses come back as
// JSON-RPC in the response body.
type HTTPClient struct {
	name      string
	url       string
	headers   map[string]string
	client    *http.Client
	mu        sync.Mutex
	tools     []ServerTool
	sessionID string

	core rpcCore
}

// NewHTTPClient creates a new MCP HTTP client. headers are added to every
// outgoing request (e.g. "Authorization" for bearer tokens).
func NewHTTPClient(name, url string, headers map[string]string) *HTTPClient {
	c := &HTTPClient{
		name:    name,
		url:     url,
		headers: headers,
		client:  &http.Client{Timeout: mcpHTTPTimeout},
	}
	c.core.tx = c
	return c
}

// Initialize performs the MCP initialize handshake over HTTP.
func (c *HTTPClient) Initialize(ctx context.Context) error {
	if _, err := c.core.initialize(ctx); err != nil {
		return err
	}
	tools, err := c.core.fetchTools(ctx)
	if err != nil {
		return err
	}
	for i := range tools {
		tools[i].ServerName = c.name
	}
	c.mu.Lock()
	c.tools = tools
	c.mu.Unlock()
	return nil
}

// CallTool invokes a tool on the MCP server over HTTP.
func (c *HTTPClient) CallTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	return c.core.callTool(ctx, name, args)
}

// Tools returns the tools discovered from this server.
func (c *HTTPClient) Tools() []ServerTool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tools
}

// Info returns server status details.
func (c *HTTPClient) Info() ServerInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return ServerInfo{
		Name:      c.name,
		Transport: "http",
		URL:       c.url,
		Connected: c.sessionID != "" || len(c.tools) > 0,
		ToolCount: len(c.tools),
	}
}

// Close terminates the server-side session. The Streamable HTTP
// transport expects a DELETE carrying the session id; skipping it left
// sessions to expire server-side (review B14).
func (c *HTTPClient) Close() error {
	c.mu.Lock()
	sid := c.sessionID
	c.sessionID = ""
	c.mu.Unlock()
	if sid == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.url, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Mcp-Session-Id", sid)
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		slog.Debug("mcp: session DELETE failed", "server", c.name, "err", err)
		return nil
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// roundTrip implements rpcTransport for the HTTP transport. Each POST
// carries one request and returns its response, so correlation is
// inherent to the round-trip.
func (c *HTTPClient) roundTrip(ctx context.Context, id int64, method string, params json.RawMessage) (*JSONRPCMessage, error) {
	return c.post(ctx, JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  method,
		Params:  params,
	})
}

// notify implements rpcTransport for the HTTP transport. Notifications
// expect 202 Accepted (no body).
func (c *HTTPClient) notify(ctx context.Context, method string, params json.RawMessage) error {
	_, err := c.post(ctx, JSONRPCMessage{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	})
	return err
}

// post sends one JSON-RPC message and returns the response message.
func (c *HTTPClient) post(ctx context.Context, msg JSONRPCMessage) (*JSONRPCMessage, error) {
	body, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	c.mu.Lock()
	sid := c.sessionID
	c.mu.Unlock()
	if sid != "" {
		req.Header.Set("Mcp-Session-Id", sid)
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	// Capture session ID from response
	if newSID := resp.Header.Get("Mcp-Session-Id"); newSID != "" {
		c.mu.Lock()
		c.sessionID = newSID
		c.mu.Unlock()
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
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
