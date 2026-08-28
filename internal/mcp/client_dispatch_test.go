package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// helperEnv gates the in-process fake MCP server used to exercise the
// stdio client dispatch loop (TestHelperProcess below).
const helperEnv = "GO_MCP_HELPER_PROCESS"

// fakeServer starts the test binary as a subprocess acting as an MCP
// stdio server in the given mode.
func fakeServer(t *testing.T, mode string) *Client {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=TestHelperProcess", "-test.v=false")
	cmd.Env = append(os.Environ(), helperEnv+"=1", "MCP_FAKE_MODE="+mode)
	c := NewClient("fake-"+mode, Manifest{
		Command:   exe,
		Args:      []string{"-test.run=TestHelperProcess"},
		Transport: "stdio",
		Framing:   "newline",
	})
	c.cmd = cmd
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	c.cmd.Stderr = os.Stderr
	if err := c.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	c.writer = NewNewlineWriter(stdin)
	c.reader = NewNewlineReader(stdout)
	go c.readLoop()
	return c
}

// TestHelperProcess is not a real test: it runs as the subprocess MCP
// server for fakeServer. Mode comes from MCP_FAKE_MODE.
func TestHelperProcess(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		return
	}
	mode := os.Getenv("MCP_FAKE_MODE")
	in := bufio.NewReader(os.Stdin)
	out := bufio.NewWriter(os.Stdout)
	var mu sync.Mutex
	respond := func(msg string) {
		mu.Lock()
		defer mu.Unlock()
		out.WriteString(msg + "\n")
		out.Flush()
	}
	for {
		line, err := in.ReadString('\n')
		if err != nil {
			return
		}
		var req JSONRPCMessage
		if json.Unmarshal([]byte(line), &req) != nil {
			continue
		}
		if req.ID == nil {
			continue // notification from client
		}
		id := *req.ID
		switch mode {
		case "interleave":
			// Emit a notification BEFORE the response: with the old
			// synchronous read the notification was consumed as the
			// response (review B13).
			respond(`{"jsonrpc":"2.0","method":"notifications/progress","params":{"n":1}}`)
			respond(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{}}`, id))
		case "paginate":
			switch req.Method {
			case "initialize":
				respond(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"fake","version":"0"}}}`, id))
			case "tools/list":
				var params struct {
					Cursor string `json:"cursor"`
				}
				_ = json.Unmarshal(req.Params, &params)
				if params.Cursor == "" {
					respond(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"page1_tool","description":"first page"}],"nextCursor":"page2"}}`, id))
				} else {
					respond(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"page2_tool","description":"second page"}]}}`, id))
				}
			default:
				respond(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{}}`, id))
			}
		case "silent":
			// Never respond: callers must give up via ctx, not hang.
		default:
			return
		}
	}
}

// TestClient_interleavedNotification pins the id-correlation fix: a
// server notification arriving before the response must not be
// consumed as the call's response (review B13).
func TestClient_interleavedNotification(t *testing.T) {
	c := fakeServer(t, "interleave")
	if err := c.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	res, err := c.CallTool(context.Background(), "echo", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res == nil {
		t.Fatal("CallTool returned nil result")
	}
	if !c.initialized {
		t.Error("client should report initialized after handshake")
	}
	if !c.Info().Connected {
		t.Error("Info().Connected should be true after a completed handshake")
	}
}

// TestClient_toolsListPagination pins the pagination loop: tools from
// every page are collected (review B13).
func TestClient_toolsListPagination(t *testing.T) {
	c := fakeServer(t, "paginate")
	if err := c.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	tools := c.Tools()
	if len(tools) != 2 {
		t.Fatalf("Tools() = %d tools, want 2 (one per page)", len(tools))
	}
	if tools[0].Name != "page1_tool" || tools[1].Name != "page2_tool" {
		t.Errorf("tools = %q, %q; want page1_tool, page2_tool", tools[0].Name, tools[1].Name)
	}
	for _, tool := range tools {
		if tool.ServerName != c.name {
			t.Errorf("tool %q missing server name", tool.Name)
		}
	}
}

// TestClient_callHonoursContextDeadline pins the per-call deadline: a
// silent server must not block a call past its context (review B13).
func TestClient_callHonoursContextDeadline(t *testing.T) {
	c := fakeServer(t, "silent")
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := c.CallTool(ctx, "echo", json.RawMessage(`{}`)); err == nil {
		t.Fatal("CallTool on a silent server should fail with the context error")
	}
}

// TestAutoDetectReader_whitespacePrefixed pins the whitespace-EOF fix:
// leading whitespace beyond the old 16-byte peek window is consumed
// instead of surfacing as EOF (review B13).
func TestAutoDetectReader_whitespacePrefixed(t *testing.T) {
	raw := strings.Repeat(" \r\n\t", 20) + `{"jsonrpc":"2.0","id":1,"result":{"a":1}}` + "\n"
	r := newReader(strings.NewReader(raw), "")
	msg, err := r.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if msg.ID == nil || *msg.ID != 1 {
		t.Errorf("ID = %v, want 1", msg.ID)
	}
}

// TestHTTPClient_lifecycle pins the HTTP transport against an
// httptest Streamable-HTTP server: session capture, paginated
// tools/list, and DELETE on Close (review B13/B14).
func TestHTTPClient_lifecycle(t *testing.T) {
	var mu sync.Mutex
	session := "sess-123"
	deleted := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			mu.Lock()
			deleted = r.Header.Get("Mcp-Session-Id")
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var req JSONRPCMessage
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.ID == nil {
			// Notification: acknowledged without a body.
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Mcp-Session-Id", session)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"fake","version":"0"}}}`, *req.ID)
		case "tools/list":
			var params struct {
				Cursor string `json:"cursor"`
			}
			_ = json.Unmarshal(req.Params, &params)
			if params.Cursor == "" {
				fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"a"}],"nextCursor":"2"}}`, *req.ID)
			} else {
				fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"b"}]}}`, *req.ID)
			}
		default:
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{}}`, *req.ID)
		}
	}))
	defer srv.Close()

	c := NewHTTPClient("fake", srv.URL, nil)
	if err := c.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if got := len(c.Tools()); got != 2 {
		t.Errorf("Tools() = %d, want 2 (paginated)", got)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	mu.Lock()
	gotDeleted := deleted
	mu.Unlock()
	if gotDeleted != session {
		t.Errorf("DELETE session id = %q, want %q", gotDeleted, session)
	}
}
