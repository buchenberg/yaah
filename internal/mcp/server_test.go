package mcp

import (
	"context"
	"encoding/json"
	"io"
	"testing"
)

// writeMsg writes a newline-delimited JSON-RPC message to w.
// It marshals the message, appends a newline, and writes both.
func writeMsg(t *testing.T, w io.Writer, msg JSONRPCMessage) {
	t.Helper()
	body, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal message: %v", err)
	}
	if _, err := w.Write(body); err != nil {
		t.Fatalf("write body: %v", err)
	}
	if _, err := w.Write([]byte("\n")); err != nil {
		t.Fatalf("write newline: %v", err)
	}
}

// readMsg reads one newline-delimited JSON-RPC message from r.
func readMsg(t *testing.T, r io.Reader) JSONRPCMessage {
	t.Helper()
	reader := NewNewlineReader(r)
	msg, err := reader.ReadMessage()
	if err != nil {
		t.Fatalf("read message: %v", err)
	}
	return msg
}

// startServer launches the server in a goroutine over a pipe pair.
// Returns the write end (stdin to server) and read end (stdout from server)
// along with a cancel func. The caller must cancel to stop the server.
func startServer(t *testing.T, srv *Server) (in io.WriteCloser, out io.Reader, cancel func()) {
	t.Helper()
	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// Ignore serve error; tests check responses directly.
		_ = srv.Serve(ctx, inReader, outWriter)
	}()
	return inWriter, outReader, cancel
}

// TestInitialize verifies the initialize handshake returns protocol version
// and server identity.
func TestInitialize(t *testing.T) {
	srv := NewServer("test-server", "1.0.0")
	in, out, cancel := startServer(t, srv)
	defer cancel()

	writeMsg(t, in, JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      int64Ptr(1),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"0.1"}}`),
	})

	resp := readMsg(t, out)

	if resp.ID == nil || *resp.ID != 1 {
		t.Errorf("response ID = %v, want 1", resp.ID)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: code=%d message=%s", resp.Error.Code, resp.Error.Message)
	}
	if resp.Result == nil {
		t.Fatal("Result is nil")
	}

	var result struct {
		ProtocolVersion string `json:"protocolVersion"`
		Capabilities    struct {
			Tools struct {
				ListChanged bool `json:"listChanged"`
			} `json:"tools"`
		} `json:"capabilities"`
		ServerInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.ProtocolVersion != "2024-11-05" {
		t.Errorf("protocolVersion = %q, want %q", result.ProtocolVersion, "2024-11-05")
	}
	if result.Capabilities.Tools.ListChanged {
		t.Errorf("capabilities.tools.listChanged = true, want false")
	}
	if result.ServerInfo.Name != "test-server" {
		t.Errorf("serverInfo.name = %q, want %q", result.ServerInfo.Name, "test-server")
	}
	if result.ServerInfo.Version != "1.0.0" {
		t.Errorf("serverInfo.version = %q, want %q", result.ServerInfo.Version, "1.0.0")
	}
}

// TestToolsList verifies that registered tools appear in the tools/list response.
func TestToolsList(t *testing.T) {
	srv := NewServer("test", "1.0")
	srv.AddTool(ServerToolDef{
		Name:        "echo",
		Description: "Echoes the input",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"}}}`),
	})
	srv.AddTool(ServerToolDef{
		Name:        "add",
		Description: "Adds two numbers",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"a":{"type":"number"},"b":{"type":"number"}}}`),
	})

	in, out, cancel := startServer(t, srv)
	defer cancel()

	writeMsg(t, in, JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      int64Ptr(1),
		Method:  "tools/list",
	})

	resp := readMsg(t, out)

	if resp.Error != nil {
		t.Fatalf("unexpected error: code=%d message=%s", resp.Error.Code, resp.Error.Message)
	}
	if resp.Result == nil {
		t.Fatal("Result is nil")
	}

	var result struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(result.Tools) != 2 {
		t.Fatalf("got %d tools, want 2", len(result.Tools))
	}

	// First tool
	if result.Tools[0].Name != "echo" {
		t.Errorf("tools[0].name = %q, want %q", result.Tools[0].Name, "echo")
	}
	if result.Tools[0].Description != "Echoes the input" {
		t.Errorf("tools[0].description = %q, want %q", result.Tools[0].Description, "Echoes the input")
	}

	// Second tool
	if result.Tools[1].Name != "add" {
		t.Errorf("tools[1].name = %q, want %q", result.Tools[1].Name, "add")
	}
	if result.Tools[1].Description != "Adds two numbers" {
		t.Errorf("tools[1].description = %q, want %q", result.Tools[1].Description, "Adds two numbers")
	}

	// Verify inputSchema is present
	if len(result.Tools[0].InputSchema) == 0 {
		t.Error("tools[0].inputSchema is empty")
	}
}

// TestToolsCall verifies that a tool handler is called and its result returned.
func TestToolsCall(t *testing.T) {
	srv := NewServer("test", "1.0")
	srv.AddTool(ServerToolDef{
		Name:        "greet",
		Description: "Greets someone",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`),
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			return "Hello, " + p.Name + "!", nil
		},
	})

	in, out, cancel := startServer(t, srv)
	defer cancel()

	writeMsg(t, in, JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      int64Ptr(1),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"greet","arguments":{"name":"World"}}`),
	})

	resp := readMsg(t, out)

	if resp.ID == nil || *resp.ID != 1 {
		t.Errorf("response ID = %v, want 1", resp.ID)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: code=%d message=%s", resp.Error.Code, resp.Error.Message)
	}

	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("got %d content items, want 1", len(result.Content))
	}
	if result.Content[0].Type != "text" {
		t.Errorf("content[0].type = %q, want %q", result.Content[0].Type, "text")
	}
	if result.Content[0].Text != "Hello, World!" {
		t.Errorf("content[0].text = %q, want %q", result.Content[0].Text, "Hello, World!")
	}
}

// TestToolsCallError verifies that a handler returning an error produces
// isError: true in the response.
func TestToolsCallError(t *testing.T) {
	srv := NewServer("test", "1.0")
	srv.AddTool(ServerToolDef{
		Name:        "failing",
		Description: "Always fails",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
			return "", io.ErrUnexpectedEOF
		},
	})

	in, out, cancel := startServer(t, srv)
	defer cancel()

	writeMsg(t, in, JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      int64Ptr(1),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"failing","arguments":{}}`),
	})

	resp := readMsg(t, out)

	if resp.Error != nil {
		t.Fatalf("expected tool-level error, got JSON-RPC error: code=%d message=%s",
			resp.Error.Code, resp.Error.Message)
	}

	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if !result.IsError {
		t.Error("isError is false, want true")
	}
	if len(result.Content) != 1 {
		t.Fatalf("got %d content items, want 1", len(result.Content))
	}
	if result.Content[0].Type != "text" {
		t.Errorf("content[0].type = %q, want %q", result.Content[0].Type, "text")
	}
	if result.Content[0].Text != "error: unexpected EOF" {
		t.Errorf("content[0].text = %q, want %q", result.Content[0].Text, "error: unexpected EOF")
	}
}

// TestToolsCallUnknownTool verifies that calling a non-existent tool returns
// isError: true with an appropriate message.
func TestToolsCallUnknownTool(t *testing.T) {
	srv := NewServer("test", "1.0")

	in, out, cancel := startServer(t, srv)
	defer cancel()

	writeMsg(t, in, JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      int64Ptr(7),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"nope","arguments":{}}`),
	})

	resp := readMsg(t, out)

	if resp.ID == nil || *resp.ID != 7 {
		t.Errorf("response ID = %v, want 7", resp.ID)
	}
	if resp.Error != nil {
		t.Fatalf("expected tool-level error, got JSON-RPC error: code=%d message=%s",
			resp.Error.Code, resp.Error.Message)
	}

	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if !result.IsError {
		t.Error("isError is false, want true")
	}
	if len(result.Content) != 1 {
		t.Fatalf("got %d content items, want 1", len(result.Content))
	}
	if result.Content[0].Text != "error: unknown tool: nope" {
		t.Errorf("content[0].text = %q, want %q", result.Content[0].Text, "error: unknown tool: nope")
	}
}

// TestUnknownMethod verifies that an unrecognized method returns a
// JSON-RPC Method not found error (-32601).
func TestUnknownMethod(t *testing.T) {
	srv := NewServer("test", "1.0")
	in, out, cancel := startServer(t, srv)
	defer cancel()

	writeMsg(t, in, JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      int64Ptr(1),
		Method:  "bogus/do_stuff",
	})

	resp := readMsg(t, out)

	if resp.ID == nil || *resp.ID != 1 {
		t.Errorf("response ID = %v, want 1", resp.ID)
	}
	if resp.Error == nil {
		t.Fatal("expected JSON-RPC error, got nil")
	}
	if resp.Error.Code != -32601 {
		t.Errorf("error code = %d, want %d", resp.Error.Code, -32601)
	}
	if resp.Error.Message != "Method not found: bogus/do_stuff" {
		t.Errorf("error message = %q, want %q", resp.Error.Message, "Method not found: bogus/do_stuff")
	}
	if resp.Result != nil {
		t.Error("Result should be nil on error")
	}
}

// TestNotification verifies that notifications (messages without an ID) do
// not produce a response. The test sends a notification followed by a ping
// and verifies only one response is received (for the ping).
func TestNotification(t *testing.T) {
	srv := NewServer("test", "1.0")
	in, out, cancel := startServer(t, srv)
	defer cancel()

	// Send a notification (no ID means notification)
	writeMsg(t, in, JSONRPCMessage{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	})

	// Send a ping request that should produce a response
	writeMsg(t, in, JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      int64Ptr(42),
		Method:  "ping",
	})

	// Read one response — should be the ping, not the notification
	resp := readMsg(t, out)
	if resp.ID == nil || *resp.ID != 42 {
		t.Errorf("response ID = %v, want 42 (notification should not produce a response)", resp.ID)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: code=%d message=%s", resp.Error.Code, resp.Error.Message)
	}
	if resp.Result == nil {
		t.Fatal("ping result is nil")
	}
}

// TestPing verifies that a ping request returns an empty result.
func TestPing(t *testing.T) {
	srv := NewServer("test", "1.0")
	in, out, cancel := startServer(t, srv)
	defer cancel()

	writeMsg(t, in, JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      int64Ptr(1),
		Method:  "ping",
	})

	resp := readMsg(t, out)
	if resp.Error != nil {
		t.Fatalf("unexpected error: code=%d message=%s", resp.Error.Code, resp.Error.Message)
	}
	if resp.Result == nil {
		t.Fatal("Result is nil")
	}
	// The result should be an empty object
	var empty struct{}
	if err := json.Unmarshal(resp.Result, &empty); err != nil {
		t.Fatalf("ping result should be empty object, got: %s", string(resp.Result))
	}
}

// TestToolsCallMissingParams verifies that a tools/call without params
// returns an appropriate error.
func TestToolsCallMissingParams(t *testing.T) {
	srv := NewServer("test", "1.0")
	srv.AddTool(ServerToolDef{
		Name:        "echo",
		Description: "Echo",
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
			return "ok", nil
		},
	})

	in, out, cancel := startServer(t, srv)
	defer cancel()

	// Send tools/call with nil params
	writeMsg(t, in, JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      int64Ptr(1),
		Method:  "tools/call",
	})

	resp := readMsg(t, out)
	if resp.Error != nil {
		t.Fatalf("expected tool-level error, got JSON-RPC error: code=%d message=%s",
			resp.Error.Code, resp.Error.Message)
	}

	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if !result.IsError {
		t.Error("isError is false, want true")
	}
	if len(result.Content) != 1 {
		t.Fatalf("got %d content items, want 1", len(result.Content))
	}
	if result.Content[0].Text != "error: missing params" {
		t.Errorf("content[0].text = %q, want %q", result.Content[0].Text, "error: missing params")
	}
}

// TestFullHandshake performs the complete MCP handshake:
// initialize → notifications/initialized → tools/list → tools/call → EOF.
func TestFullHandshake(t *testing.T) {
	srv := NewServer("handshake-test", "2.0")
	srv.AddTool(ServerToolDef{
		Name:        "reverse",
		Description: "Reverses a string",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}}}`),
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Input string `json:"input"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			runes := []rune(p.Input)
			for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
				runes[i], runes[j] = runes[j], runes[i]
			}
			return string(runes), nil
		},
	})

	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- srv.Serve(ctx, inReader, outWriter)
	}()

	// Step 1: initialize
	t.Log("step 1: initialize")
	writeMsg(t, inWriter, JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      int64Ptr(1),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test-client","version":"1.0"}}`),
	})
	resp := readMsg(t, outReader)
	if resp.ID == nil || *resp.ID != 1 {
		t.Fatalf("initialize: response ID = %v, want 1", resp.ID)
	}
	if resp.Error != nil {
		t.Fatalf("initialize: unexpected error: %s", resp.Error.Message)
	}

	// Step 2: initialized notification (no response expected)
	t.Log("step 2: notifications/initialized")
	writeMsg(t, inWriter, JSONRPCMessage{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	})

	// Step 3: tools/list
	t.Log("step 3: tools/list")
	writeMsg(t, inWriter, JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      int64Ptr(2),
		Method:  "tools/list",
	})
	resp = readMsg(t, outReader)
	if resp.ID == nil || *resp.ID != 2 {
		t.Fatalf("tools/list: response ID = %v, want 2", resp.ID)
	}
	if resp.Error != nil {
		t.Fatalf("tools/list: unexpected error: %s", resp.Error.Message)
	}
	var listResult struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &listResult); err != nil {
		t.Fatalf("tools/list: unmarshal: %v", err)
	}
	if len(listResult.Tools) != 1 {
		t.Fatalf("tools/list: got %d tools, want 1", len(listResult.Tools))
	}
	if listResult.Tools[0].Name != "reverse" {
		t.Errorf("tools/list: tool name = %q, want %q", listResult.Tools[0].Name, "reverse")
	}

	// Step 4: tools/call
	t.Log("step 4: tools/call")
	writeMsg(t, inWriter, JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      int64Ptr(3),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"reverse","arguments":{"input":"hello"}}`),
	})
	resp = readMsg(t, outReader)
	if resp.ID == nil || *resp.ID != 3 {
		t.Fatalf("tools/call: response ID = %v, want 3", resp.ID)
	}
	if resp.Error != nil {
		t.Fatalf("tools/call: unexpected error: %s", resp.Error.Message)
	}
	var callResult struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(resp.Result, &callResult); err != nil {
		t.Fatalf("tools/call: unmarshal: %v", err)
	}
	if len(callResult.Content) != 1 {
		t.Fatalf("tools/call: got %d content items, want 1", len(callResult.Content))
	}
	if callResult.Content[0].Text != "olleh" {
		t.Errorf("tools/call: result text = %q, want %q", callResult.Content[0].Text, "olleh")
	}

	// Step 5: EOF — close stdin to signal the server to stop
	t.Log("step 5: EOF")
	inWriter.Close()
	outWriter.Close()

	// Wait for server to finish
	if err := <-done; err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
}

// TestContextCancellation verifies that Serve returns when the context is
// cancelled.
func TestContextCancellation(t *testing.T) {
	srv := NewServer("test", "1.0")

	inReader, inWriter := io.Pipe()
	_, outWriter := io.Pipe()

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- srv.Serve(ctx, inReader, outWriter)
	}()

	// Cancel the context
	cancel()

	// Server should return context.Canceled
	err := <-done
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}

	// Close pipes to avoid resource leaks
	inWriter.Close()
	outWriter.Close()
}
