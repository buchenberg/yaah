package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestClient_newlineFraming_handshake — simulates a Docker MCP gateway that
// speaks newline-delimited JSON, not Content-Length. This was the bug that
// caused "invalid character 'C'" warnings and silently dropped the server.
func TestClient_newlineFraming_handshake(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires /bin/sh")
	}
	// Build a minimal in-process stdio server: a shell pipeline that
	// responds to "initialize" with a newline-delimited JSON reply, then
	// responds to "tools/list" with an empty tool list.
	// Fake gateway: a shell that reads line-by-line, matches method name,
	// and replies with a newline-delimited JSON object. The discard of
	// stdin via `cat` would race with `read`; instead we use a single
	// `read` loop that handles both consume-and-respond in one pass.
	script := `while IFS= read -r line; do
  case "$line" in
    *initialize*)
      printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"fake","version":"0"}}}'
      ;;
    *tools/list*)
      printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"tools":[]}}'
      ;;
  esac
done
`

	tmp := t.TempDir()
	scriptPath := filepath.Join(tmp, "fake-mcp.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	client := NewClient("fake", Manifest{
		Command: "/bin/sh",
		Args:    []string{scriptPath},
		// No Framing set — auto-detect from the first byte of the response.
		Transport: "stdio",
	})
	defer client.Close()

	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if got := len(client.Tools()); got != 0 {
		t.Errorf("Tools() = %d, want 0 (empty server)", got)
	}
}

// TestAutoDetectReader_picksNewline — first byte '{' means newline framing
func TestAutoDetectReader_picksNewline(t *testing.T) {
	// Send two newline-delimited messages
	raw := `{"jsonrpc":"2.0","id":1,"result":{"a":1}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"result":{"a":2}}` + "\n"
	r := newReader(strings.NewReader(raw), "")
	for i := 1; i <= 2; i++ {
		msg, err := r.ReadMessage()
		if err != nil {
			t.Fatalf("msg %d: %v", i, err)
		}
		if msg.ID == nil || *msg.ID != int64(i) {
			t.Errorf("msg %d: ID = %v, want %d", i, msg.ID, i)
		}
	}
}

// TestAutoDetectReader_picksFramed — first byte 'C' (Content-Length) means framed
func TestAutoDetectReader_picksFramed(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"result":{"a":1}}`
	raw := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
	r := newReader(strings.NewReader(raw), "")
	msg, err := r.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if msg.ID == nil || *msg.ID != 1 {
		t.Errorf("ID = %v, want 1", msg.ID)
	}
}
