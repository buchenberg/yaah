// Package mcp implements the Model Context Protocol (MCP) client for yaah.
// MCP servers are spawned as child processes and communicate over stdio
// using JSON-RPC 2.0 with Content-Length framing. yaah discovers tools
// from MCP servers and makes them available to the agent alongside
// built-in tools.
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

// JSONRPCMessage represents a JSON-RPC 2.0 message.
type JSONRPCMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

// JSONRPCError represents a JSON-RPC error object.
type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// FramedReader reads Content-Length-framed JSON-RPC messages from an io.Reader.
type FramedReader struct {
	reader *bufio.Reader
}

// NewFramedReader creates a new FramedReader.
func NewFramedReader(r io.Reader) *FramedReader {
	return &FramedReader{reader: bufio.NewReader(r)}
}

// ReadMessage reads the next framed message. Returns io.EOF when the
// stream is closed.
func (r *FramedReader) ReadMessage() (JSONRPCMessage, error) {
	var msg JSONRPCMessage

	// Read headers until empty line
	contentLength := -1
	for {
		line, err := r.reader.ReadString('\n')
		if err != nil {
			return msg, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "Content-Length:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:"))
			contentLength, _ = strconv.Atoi(val)
		}
	}

	if contentLength <= 0 {
		return msg, fmt.Errorf("invalid Content-Length: %d", contentLength)
	}

	body := make([]byte, contentLength)
	_, err := io.ReadFull(r.reader, body)
	if err != nil {
		return msg, err
	}

	if err := json.Unmarshal(body, &msg); err != nil {
		return msg, fmt.Errorf("unmarshal JSON-RPC: %w", err)
	}

	return msg, nil
}

// FramedWriter writes Content-Length-framed JSON-RPC messages to an io.Writer.
type FramedWriter struct {
	writer io.Writer
	mu     sync.Mutex
}

// NewFramedWriter creates a new FramedWriter.
func NewFramedWriter(w io.Writer) *FramedWriter {
	return &FramedWriter{writer: w}
}

// WriteMessage marshals and writes a framed message.
func (w *FramedWriter) WriteMessage(msg JSONRPCMessage) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal JSON-RPC: %w", err)
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	if _, err := w.writer.Write([]byte(header)); err != nil {
		return err
	}
	_, err = w.writer.Write(body)
	return err
}
