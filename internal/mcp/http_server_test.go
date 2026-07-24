package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// startHTTPTestServer launches an HTTPServer backed by a basic tool on
// 127.0.0.1 with an OS-chosen port, returning the base URL plus a
// cancel func that stops the server.
func startHTTPTestServer(t *testing.T) (string, func()) {
	t.Helper()

	srv := NewServer("test-http", "1.0.0")
	srv.AddTool(ServerToolDef{
		Name:        "echo",
		Description: "Echoes input",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"msg":{"type":"string"}}}`),
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Msg string `json:"msg"`
			}
			_ = json.Unmarshal(args, &p)
			return "echo: " + p.Msg, nil
		},
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // close so HTTPServer can bind the same port

	httpSrv := NewHTTPServer(srv, addr)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- httpSrv.Start(ctx)
	}()

	// Wait for the server to become reachable.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			_ = c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	cleanup := func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(2 * time.Second):
		}
	}

	return "http://" + addr, cleanup
}

// postJSON sends a POST /mcp with the given JSON body and returns the
// response status, body, and the Mcp-Session-Id header (if any).
func postJSON(t *testing.T, baseURL string, body string, sessionID string) (int, []byte, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, baseURL+"/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, respBody, resp.Header.Get("Mcp-Session-Id")
}

// TestHTTPHealth verifies GET /health returns 200 with server info.
func TestHTTPHealth(t *testing.T) {
	base, stop := startHTTPTestServer(t)
	defer stop()

	resp, err := http.Get(base + "/health")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json prefix", ct)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status field = %v, want ok", body["status"])
	}
	if body["name"] != "test-http" {
		t.Errorf("name field = %v, want test-http", body["name"])
	}
}

// TestHTTPInitializeIssuesSession verifies that initialize returns a
// session ID and that the response contains the expected capabilities.
func TestHTTPInitializeIssuesSession(t *testing.T) {
	base, stop := startHTTPTestServer(t)
	defer stop()

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`
	status, respBody, sid := postJSON(t, base, body, "")
	if status != 200 {
		t.Fatalf("status = %d, want 200; body=%s", status, respBody)
	}
	if sid == "" {
		t.Fatal("Mcp-Session-Id header is empty")
	}
	if len(sid) < 16 {
		t.Errorf("session id = %q, expected >=16 hex chars", sid)
	}

	var resp JSONRPCMessage
	if err := json.Unmarshal(respBody, &resp); err != nil {
		t.Fatalf("unmarshal response: %v; body=%s", err, respBody)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %s", resp.Error.Message)
	}
	var result struct {
		ServerInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.ServerInfo.Name != "test-http" {
		t.Errorf("name = %q, want test-http", result.ServerInfo.Name)
	}
}

// TestHTTPInitializeThenToolsList walks the standard MCP handshake
// (initialize → notifications/initialized → tools/list) over HTTP and
// verifies the tools/list returns the registered tool.
func TestHTTPInitializeThenToolsList(t *testing.T) {
	base, stop := startHTTPTestServer(t)
	defer stop()

	// Step 1: initialize.
	status, body, sid := postJSON(t, base,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`,
		"")
	if status != 200 || sid == "" {
		t.Fatalf("initialize failed: status=%d body=%s sid=%q", status, body, sid)
	}

	// Step 2: notifications/initialized.
	status, _, _ = postJSON(t, base,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`, sid)
	if status != http.StatusAccepted {
		t.Errorf("notifications/initialized status = %d, want 202", status)
	}

	// Step 3: tools/list with the session ID.
	status, body, _ = postJSON(t, base,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, sid)
	if status != 200 {
		t.Fatalf("tools/list status = %d body=%s", status, body)
	}
	var resp JSONRPCMessage
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var list struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &list); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(list.Tools) != 1 || list.Tools[0].Name != "echo" {
		t.Errorf("tools = %+v, want exactly [echo]", list.Tools)
	}
}

// TestHTTPUnknownSessionAutoRegistered verifies a request with a stale
// session ID is auto-registered (transparent server restart recovery).
func TestHTTPUnknownSessionAutoRegistered(t *testing.T) {
	base, stop := startHTTPTestServer(t)
	defer stop()

	status, body, _ := postJSON(t, base,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		"deadbeefdeadbeefdeadbeefdeadbeef")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200 (auto-registered)", status, body)
	}
}

// TestHTTPToolsCall invokes a registered tool via HTTP and checks the
// structured content response.
func TestHTTPToolsCall(t *testing.T) {
	base, stop := startHTTPTestServer(t)
	defer stop()

	// Initialize to obtain a session.
	_, _, sid := postJSON(t, base,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`,
		"")

	status, body, _ := postJSON(t, base,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"msg":"hi"}}}`,
		sid)
	if status != 200 {
		t.Fatalf("status = %d body=%s", status, body)
	}

	var resp JSONRPCMessage
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var call struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(resp.Result, &call); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(call.Content) != 1 || call.Content[0].Text != "echo: hi" {
		t.Errorf("content = %+v, want one text item echoing 'hi'", call.Content)
	}
}

// TestHTTPGetReturnsSSEStream verifies GET /mcp opens a 200
// text/event-stream and announces the message endpoint event,
// which is the legacy HTTP+SSE transport entry point.
func TestHTTPGetReturnsSSEStream(t *testing.T) {
	base, stop := startHTTPTestServer(t)
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/mcp", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	if sid := resp.Header.Get("Mcp-Session-Id"); sid == "" {
		t.Error("missing Mcp-Session-Id header on SSE response")
	}

	// Read the first event: must be "endpoint" naming the message URL.
	br := bufio.NewReader(resp.Body)
	event, data, err := readSSEEvent(br)
	if err != nil {
		t.Fatalf("read sse event: %v", err)
	}
	if event != "endpoint" {
		t.Errorf("first event = %q, want endpoint", event)
	}
	if !strings.HasPrefix(data, "/mcp/messages?sessionId=") {
		t.Errorf("endpoint data = %q, want /mcp/messages?sessionId=...", data)
	}
}

// readSSEEvent parses one SSE event from br: returns event name and
// data field. Multiline data fields are joined with newlines per spec.
func readSSEEvent(br *bufio.Reader) (string, string, error) {
	var event, data string
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return event, data, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			// Empty line terminates the event.
			return event, data, nil
		}
		switch {
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			chunk := strings.TrimPrefix(line, "data:")
			if data == "" {
				data = strings.TrimPrefix(chunk, " ")
			} else {
				data += "\n" + strings.TrimPrefix(chunk, " ")
			}
		}
	}
}

// TestHTTPDeleteClosesSession verifies DELETE /mcp with a valid
// session ID returns 204 and that subsequent requests with that ID
// are rejected.
func TestHTTPDeleteClosesSession(t *testing.T) {
	base, stop := startHTTPTestServer(t)
	defer stop()

	_, _, sid := postJSON(t, base,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`,
		"")

	req, err := http.NewRequest(http.MethodDelete, base+"/mcp", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Mcp-Session-Id", sid)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("delete status = %d, want 204", resp.StatusCode)
	}

	// Subsequent tools/list with the deleted session auto-registers
	// a fresh session (transparent restart recovery), so it succeeds.
	status, _, _ := postJSON(t, base,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, sid)
	if status != http.StatusOK {
		t.Errorf("status after delete = %d, want 200 (auto-registered)", status)
	}
}

// TestHTTPDeleteMissingSessionID verifies DELETE without a session
// header is rejected with 400.
func TestHTTPDeleteMissingSessionID(t *testing.T) {
	base, stop := startHTTPTestServer(t)
	defer stop()

	req, err := http.NewRequest(http.MethodDelete, base+"/mcp", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// TestHTTPPostEmptyBody verifies that a POST with an empty body
// returns 400 rather than hanging or returning an empty 200.
func TestHTTPPostEmptyBody(t *testing.T) {
	base, stop := startHTTPTestServer(t)
	defer stop()

	resp, err := http.Post(base+"/mcp", "application/json", bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// TestHTTPPostInvalidJSON verifies a non-JSON body returns 400.
func TestHTTPPostInvalidJSON(t *testing.T) {
	base, stop := startHTTPTestServer(t)
	defer stop()

	status, body, _ := postJSON(t, base, "not json at all", "")
	if status != http.StatusBadRequest {
		t.Errorf("status = %d body=%s, want 400", status, body)
	}
}

// TestHTTPNonInitializeNoSessionAllowed verifies that tools/list
// without any session ID is accepted (since we don't strictly require
// sessions — initialize is the only method that issues one).
func TestHTTPNonInitializeNoSessionAllowed(t *testing.T) {
	base, stop := startHTTPTestServer(t)
	defer stop()

	status, body, _ := postJSON(t, base,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, "")
	if status != 200 {
		t.Errorf("status = %d body=%s, want 200", status, body)
	}
}

// TestHTTPSSEHandshake walks the full legacy HTTP+SSE transport:
// open GET /mcp, capture the endpoint event, POST a tools/list
// message to that endpoint, and confirm the response arrives as
// an SSE "message" event.
func TestHTTPSSEHandshake(t *testing.T) {
	base, stop := startHTTPTestServer(t)
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/mcp", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	br := bufio.NewReader(resp.Body)
	event, data, err := readSSEEvent(br)
	if err != nil {
		t.Fatalf("read endpoint event: %v", err)
	}
	if event != "endpoint" {
		t.Fatalf("event = %q, want endpoint", event)
	}
	endpoint := data
	sid := resp.Header.Get("Mcp-Session-Id")
	if sid == "" {
		t.Fatal("missing Mcp-Session-Id on SSE response")
	}

	// POST a tools/list message to the announced endpoint.
	msgURL := base + endpoint
	postReq, err := http.NewRequestWithContext(ctx, http.MethodPost, msgURL,
		strings.NewReader(`{"jsonrpc":"2.0","id":42,"method":"tools/list"}`))
	if err != nil {
		t.Fatalf("new post: %v", err)
	}
	postReq.Header.Set("Content-Type", "application/json")
	postResp, err := http.DefaultClient.Do(postReq)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	postBody, _ := io.ReadAll(postResp.Body)
	postResp.Body.Close()
	if postResp.StatusCode != 200 {
		t.Fatalf("post status = %d body=%s", postResp.StatusCode, postBody)
	}
	var httpResp JSONRPCMessage
	if err := json.Unmarshal(postBody, &httpResp); err != nil {
		t.Fatalf("parse http response: %v", err)
	}
	if httpResp.Error != nil {
		t.Fatalf("http response error: %s", httpResp.Error.Message)
	}

	// Same response should also have been pushed to the SSE stream.
	event2, data2, err := readSSEEvent(br)
	if err != nil {
		t.Fatalf("read message event: %v", err)
	}
	if event2 != "message" {
		t.Errorf("second event = %q, want message", event2)
	}
	var sseResp JSONRPCMessage
	if err := json.Unmarshal([]byte(data2), &sseResp); err != nil {
		t.Fatalf("parse sse response: %v", err)
	}
	if sseResp.ID == nil || *sseResp.ID != 42 {
		t.Errorf("sse response id = %v, want 42", sseResp.ID)
	}
	if sseResp.Error != nil {
		t.Errorf("sse response error: %s", sseResp.Error.Message)
	}
}
