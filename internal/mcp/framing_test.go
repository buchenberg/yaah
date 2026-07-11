package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestFramedReader_parsesMessage(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05"}}`
	raw := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
	r := strings.NewReader(raw)
	reader := NewFramedReader(r)

	msg, err := reader.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage() error: %v", err)
	}

	if msg.ID != 1 {
		t.Errorf("ID = %d, want 1", msg.ID)
	}
	if msg.Result == nil {
		t.Fatal("Result is nil")
	}
}

func TestFramedReader_returnsEOFOnEmpty(t *testing.T) {
	r := strings.NewReader("")
	reader := NewFramedReader(r)

	_, err := reader.ReadMessage()
	if err != io.EOF {
		t.Errorf("expected EOF, got %v", err)
	}
}

func TestFramedWriter_writesFramedJSON(t *testing.T) {
	var buf bytes.Buffer
	writer := NewFramedWriter(&buf)

	msg := JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2024-11-05"}`),
	}

	if err := writer.WriteMessage(msg); err != nil {
		t.Fatalf("WriteMessage() error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "Content-Length:") {
		t.Errorf("missing Content-Length: %q", output)
	}
	if !strings.Contains(output, "initialize") {
		t.Errorf("missing method: %q", output)
	}
	if !strings.Contains(output, "\r\n\r\n") {
		t.Errorf("missing header/body separator: %q", output)
	}
}

func TestRoundTrip_sendAndReceive(t *testing.T) {
	serverR, clientW := io.Pipe()
	clientR, serverW := io.Pipe()
	defer serverR.Close()
	defer serverW.Close()

	writer := NewFramedWriter(clientW)
	reader := NewFramedReader(clientR)

	go func() {
		sr := NewFramedReader(serverR)
		msg, _ := sr.ReadMessage()
		resp := JSONRPCMessage{
			JSONRPC: "2.0",
			ID:      msg.ID,
			Result:  json.RawMessage(`{"echo":"test"}`),
		}
		sw := NewFramedWriter(serverW)
		sw.WriteMessage(resp)
		serverW.Close()
	}()

	err := writer.WriteMessage(JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      42,
		Method:  "test",
	})
	if err != nil {
		t.Fatalf("WriteMessage() error: %v", err)
	}
	clientW.Close()

	resp, err := reader.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage() error: %v", err)
	}
	if resp.ID != 42 {
		t.Errorf("ID = %d, want 42", resp.ID)
	}
}

func TestFramedReader_handlesMultipleMessages(t *testing.T) {
	msg1 := `{"jsonrpc":"2.0","id":1,"result":{}}`
	msg2 := `{"jsonrpc":"2.0","id":2,"result":{"tools":[]}}`
	raw := "Content-Length: " + fmt.Sprintf("%d", len(msg1)) + "\r\n\r\n" + msg1 +
		"Content-Length: " + fmt.Sprintf("%d", len(msg2)) + "\r\n\r\n" + msg2

	r := strings.NewReader(raw)
	reader := NewFramedReader(r)

	m1, err := reader.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage() 1: %v", err)
	}
	m2, err := reader.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage() 2: %v", err)
	}
	if m1.ID != 1 || m2.ID != 2 {
		t.Errorf("IDs = %d, %d; want 1, 2", m1.ID, m2.ID)
	}
}
