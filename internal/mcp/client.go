package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// Manifest represents an MCP server configuration file.
type Manifest struct {
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	URL       string            `json:"url,omitempty"`
	Transport string            `json:"transport,omitempty"` // "stdio" (default) or "http"
	Framing   string            `json:"framing,omitempty"`   // stdio only: "framed" (default) or "newline"
}

// ServerTool represents a tool exposed by an MCP server.
type ServerTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	ServerName  string          `json:"-"` // which server owns this tool
}

// Client represents a connected MCP server process.
type Client struct {
	name     string
	manifest Manifest
	cmd      *exec.Cmd
	writer   messageWriter
	reader   messageReader
	nextID   atomic.Int64
	mu       sync.Mutex
	tools    []ServerTool
	closed   bool
}

// messageWriter is the interface satisfied by both FramedWriter and NewlineWriter.
type messageWriter interface {
	WriteMessage(msg JSONRPCMessage) error
}

// messageReader is the interface satisfied by both FramedReader and NewlineReader.
type messageReader interface {
	ReadMessage() (JSONRPCMessage, error)
}

// NewClient creates a new MCP client from a manifest.
func NewClient(name string, manifest Manifest) *Client {
	return &Client{
		name:     name,
		manifest: manifest,
	}
}

// Start spawns the MCP server process and initializes the connection.
func (c *Client) Start(ctx context.Context) error {
	c.cmd = exec.CommandContext(ctx, c.manifest.Command, c.manifest.Args...)
	c.cmd.Env = os.Environ()
	for k, v := range c.manifest.Env {
		c.cmd.Env = append(c.cmd.Env, k+"="+v)
	}

	stdin, err := c.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := c.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	c.cmd.Stderr = os.Stderr

	if err := c.cmd.Start(); err != nil {
		return fmt.Errorf("start server: %w", err)
	}

	c.writer = newWriter(stdin, c.manifest.Framing)
	c.reader = newReader(stdout, c.manifest.Framing)

	return nil
}

// newWriter returns the appropriate writer for the given framing mode.
// "" and "framed" → Content-Length LSP framing (MCP spec default).
// "newline"      → raw newline-delimited JSON (Docker MCP gateway).
func newWriter(w io.Writer, framing string) messageWriter {
	if framing == "newline" {
		return NewNewlineWriter(w)
	}
	return NewFramedWriter(w)
}

// newReader returns the appropriate reader for the given framing mode.
func newReader(r io.Reader, framing string) messageReader {
	if framing == "newline" {
		return NewNewlineReader(r)
	}
	return NewFramedReader(r)
}

// Initialize performs the MCP initialize handshake and fetches tools.
func (c *Client) Initialize(ctx context.Context) error {
	id := c.nextID.Add(1)
	params, _ := json.Marshal(map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "yaah", "version": "0.3.0"},
	})

	if err := c.writer.WriteMessage(JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "initialize",
		Params:  params,
	}); err != nil {
		return fmt.Errorf("send initialize: %w", err)
	}

	resp, err := c.reader.ReadMessage()
	if err != nil {
		return fmt.Errorf("read initialize response: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("initialize error: %s", resp.Error.Message)
	}

	// Send initialized notification
	if err := c.writer.WriteMessage(JSONRPCMessage{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}); err != nil {
		return fmt.Errorf("send initialized: %w", err)
	}

	// Fetch tools
	return c.fetchTools(ctx)
}

func (c *Client) fetchTools(ctx context.Context) error {
	id := c.nextID.Add(1)
	if err := c.writer.WriteMessage(JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "tools/list",
	}); err != nil {
		return fmt.Errorf("send tools/list: %w", err)
	}

	resp, err := c.reader.ReadMessage()
	if err != nil {
		return fmt.Errorf("read tools/list: %w", err)
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

// CallTool invokes a tool on the MCP server.
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	id := c.nextID.Add(1)
	params, _ := json.Marshal(map[string]any{
		"name":      name,
		"arguments": json.RawMessage(args),
	})

	if err := c.writer.WriteMessage(JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "tools/call",
		Params:  params,
	}); err != nil {
		return nil, fmt.Errorf("send tools/call: %w", err)
	}

	resp, err := c.reader.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("read tools/call: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("tools/call error: %s", resp.Error.Message)
	}

	return resp.Result, nil
}

// Tools returns the tools discovered from this server.
func (c *Client) Tools() []ServerTool {
	return c.tools
}

// Close shuts down the MCP server process.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	if c.cmd != nil && c.cmd.Process != nil {
		return c.cmd.Process.Kill()
	}
	return nil
}

// LoadManifest reads an MCP manifest file from the given path.
func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", path, err)
	}

	// Default transport
	if m.Transport == "" {
		if m.URL != "" {
			m.Transport = "http"
		} else {
			m.Transport = "stdio"
		}
	}

	// Validate
	switch m.Transport {
	case "stdio":
		if m.Command == "" {
			return nil, fmt.Errorf("manifest %s: command is required for stdio transport", path)
		}
	case "http":
		if m.URL == "" {
			return nil, fmt.Errorf("manifest %s: url is required for http transport", path)
		}
	default:
		return nil, fmt.Errorf("manifest %s: unknown transport %q", path, m.Transport)
	}

	return &m, nil
}

// DiscoverManifests scans directories for MCP server manifest files.
// Returns a map of server name → manifest.
func DiscoverManifests(dirs []string) map[string]*Manifest {
	seen := make(map[string]bool)
	out := make(map[string]*Manifest)

	for _, dir := range dirs {
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
				continue
			}
			name := entry.Name()[:len(entry.Name())-5] // strip .json
			if seen[name] {
				continue
			}
			m, err := LoadManifest(filepath.Join(dir, entry.Name()))
			if err != nil {
				continue
			}
			seen[name] = true
			out[name] = m
		}
	}
	return out
}
