package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
)

func int64Ptr(v int64) *int64 { return &v }

// TestFramedReader_parsesMessage — content-length framing (MCP spec default)
func TestFramedReader_parsesMessage(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05"}}`
	raw := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
	r := strings.NewReader(raw)
	reader := NewFramedReader(r)

	msg, err := reader.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage() error: %v", err)
	}
	if msg.ID == nil || *msg.ID != 1 {
		t.Errorf("ID = %v, want 1", msg.ID)
	}
	if msg.Result == nil {
		t.Fatal("Result is nil")
	}
}

// TestFramedReader_returnsEOFOnEmpty
func TestFramedReader_returnsEOFOnEmpty(t *testing.T) {
	r := strings.NewReader("")
	reader := NewFramedReader(r)

	_, err := reader.ReadMessage()
	if err != io.EOF {
		t.Errorf("expected EOF, got %v", err)
	}
}

// TestFramedWriter_writesFramedJSON
func TestFramedWriter_writesFramedJSON(t *testing.T) {
	var buf bytes.Buffer
	writer := NewFramedWriter(&buf)

	msg := JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      int64Ptr(1),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2024-11-05"}`),
	}
	if err := writer.WriteMessage(msg); err != nil {
		t.Fatalf("WriteMessage() error: %v", err)
	}

	// First line must contain Content-Length
	line, err := bufio.NewReader(&buf).ReadString('\n')
	if err != nil {
		t.Fatalf("ReadString: %v", err)
	}
	if !strings.HasPrefix(line, "Content-Length:") {
		t.Errorf("first line = %q, want Content-Length prefix", line)
	}
}

// --- NewLineDelimited (Docker MCP gateway compatibility) ------------------

// TestNewlineWriter_writesRawJSON — Docker MCP gateway rejects Content-Length
// framing on stdio and expects raw newline-delimited JSON instead.
func TestNewlineWriter_writesRawJSON(t *testing.T) {
	var buf bytes.Buffer
	writer := NewNewlineWriter(&buf)

	msg := JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      int64Ptr(1),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2024-11-05"}`),
	}
	if err := writer.WriteMessage(msg); err != nil {
		t.Fatalf("WriteMessage() error: %v", err)
	}

	// Output must be raw JSON ending in \n, with NO Content-Length header.
	out := buf.String()
	if strings.HasPrefix(out, "Content-Length:") {
		t.Errorf("newline writer emitted Content-Length header: %q", out)
	}
	if !strings.HasPrefix(out, "{") {
		t.Errorf("newline writer did not start with '{': %q", out)
	}
	if !strings.HasSuffix(out, "}\n") {
		t.Errorf("newline writer did not end with '}\\n': %q", out)
	}
}

// TestNewlineReader_parsesRawJSON — opposite of the writer.
func TestNewlineReader_parsesRawJSON(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":7,"result":{"ok":true}}`
	r := strings.NewReader(body + "\n")
	reader := NewNewlineReader(r)

	msg, err := reader.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage() error: %v", err)
	}
	if msg.ID == nil || *msg.ID != 7 {
		t.Errorf("ID = %v, want 7", msg.ID)
	}
}

// TestNewlineWriter_concurrentSafe — used concurrently from agent loops.
func TestNewlineWriter_concurrentSafe(t *testing.T) {
	var buf bytes.Buffer
	writer := NewNewlineWriter(&buf)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = writer.WriteMessage(JSONRPCMessage{
				JSONRPC: "2.0",
				ID:      int64Ptr(int64(i)),
				Method:  "ping",
			})
		}(i)
	}
	wg.Wait()

	// Count newlines — must equal number of messages
	lines := bytes.Count(buf.Bytes(), []byte("\n"))
	if lines != 20 {
		t.Errorf("got %d newlines, want 20", lines)
	}
}
