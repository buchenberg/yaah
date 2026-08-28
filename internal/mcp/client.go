package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// Manifest represents an MCP server configuration file.
type Manifest struct {
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	URL       string            `json:"url,omitempty"`
	Transport string            `json:"transport,omitempty"` // "stdio" (default) or "http"
	Framing   string            `json:"framing,omitempty"`   // stdio only: "" (auto), "newline", or "framed"
	Headers   map[string]string `json:"headers,omitempty"`   // HTTP transport only
}

// ServerTool represents a tool exposed by an MCP server.
type ServerTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	ServerName  string          `json:"-"` // which server owns this tool
}

// clientVersion is reported as the MCP clientInfo version during the
// initialize handshake. It is set at startup from the build version via
// SetClientVersion; the fallback keeps unversioned builds honest.
var clientVersion = "dev"

// SetClientVersion sets the version string reported in the MCP
// initialize handshake by every stdio and HTTP client. Call once at
// startup with the ldflags-injected build version.
func SetClientVersion(v string) {
	if v != "" {
		clientVersion = v
	}
}

// Client represents a connected MCP server process over stdio.
type Client struct {
	name     string
	manifest Manifest
	cmd      *exec.Cmd
	writer   messageWriter
	reader   messageReader
	stderr   io.Writer // destination for server stderr; defaults to io.Discard

	core rpcCore

	// pending maps in-flight request IDs to their response channels.
	// The read loop routes responses here; interleaved server
	// notifications no longer corrupt a concurrent call (review B13).
	pending sync.Map // int64 -> chan *JSONRPCMessage (buffered 1)

	writeMu sync.Mutex // serialises writer access
	mu      sync.Mutex // guards tools/closed/initialized
	tools   []ServerTool
	closed  bool
	// initialized is set once the handshake completed; Info reports
	// Connected from this, not from "the process struct exists" (B13).
	initialized bool
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
	c := &Client{
		name:     name,
		manifest: manifest,
	}
	c.core.tx = c
	return c
}

// Start spawns the MCP server process, initializes the connection, and
// starts the read loop that dispatches responses to pending calls.
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

	// Default to discarding stderr so MCP server log spam (e.g. Docker
	// Gateway credential helpers, OAuth loops) doesn't corrupt the terminal.
	// Call SetStderr before Start to redirect it elsewhere.
	if c.stderr != nil {
		c.cmd.Stderr = c.stderr
	} else {
		c.cmd.Stderr = io.Discard
	}

	if err := c.cmd.Start(); err != nil {
		return fmt.Errorf("start server: %w", err)
	}

	c.writer = newWriter(stdin, c.manifest.Framing)
	c.reader = newReader(stdout, c.manifest.Framing)
	go c.readLoop()
	return nil
}

// readLoop continuously reads server messages and routes them:
// responses to pending calls, notifications to their handlers.
// Everything else is logged and dropped.
func (c *Client) readLoop() {
	for {
		msg, err := c.reader.ReadMessage()
		if err != nil {
			// Stream closed or reader failure: fail every pending call
			// so waiters do not block forever on a dead server (B13).
			c.failAllPending(err)
			return
		}
		c.dispatch(msg)
	}
}

// dispatch routes one inbound message. Responses (non-nil id) resolve
// the matching pending call; notifications go to their handlers.
func (c *Client) dispatch(msg JSONRPCMessage) {
	if msg.ID != nil {
		chAny, ok := c.pending.LoadAndDelete(*msg.ID)
		if !ok {
			slog.Debug("mcp: response for unknown request id", "server", c.name, "id", *msg.ID)
			return
		}
		ch := chAny.(chan *JSONRPCMessage)
		ch <- &msg
		return
	}
	c.handleNotification(msg)
}

// handleNotification processes server-initiated notifications. Only
// tools/list_changed has an effect (re-fetch tools); the rest are
// logged and dropped.
func (c *Client) handleNotification(msg JSONRPCMessage) {
	switch msg.Method {
	case "notifications/tools/list_changed":
		slog.Debug("mcp: tools changed, re-fetching", "server", c.name)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), mcpHTTPTimeout)
			defer cancel()
			tools, err := c.core.fetchTools(ctx)
			if err != nil {
				slog.Debug("mcp: tools re-fetch failed", "server", c.name, "err", err)
				return
			}
			for i := range tools {
				tools[i].ServerName = c.name
			}
			c.mu.Lock()
			c.tools = tools
			c.mu.Unlock()
		}()
	default:
		slog.Debug("mcp: dropping notification", "server", c.name, "method", msg.Method)
	}
}

// failAllPending resolves every in-flight call with an error. Each
// entry is claimed with LoadAndDelete before sending so a concurrent
// dispatch cannot deliver the real response between the claim and the
// send (the buffered-1 channel would then block this loop forever).
func (c *Client) failAllPending(cause error) {
	c.pending.Range(func(key, value any) bool {
		if _, ok := c.pending.LoadAndDelete(key); !ok {
			return true // already claimed by dispatch
		}
		ch := value.(chan *JSONRPCMessage)
		select {
		case ch <- &JSONRPCMessage{Error: &JSONRPCError{Code: -32000, Message: "mcp server connection lost: " + cause.Error()}}:
		default:
			// Unreachable after a successful claim (buffered 1, single
			// claimed sender); guard against blocking Close regardless.
		}
		return true
	})
}

// roundTrip implements rpcTransport for the stdio transport: register a
// waiter, write the request, and wait for the read loop to route the
// response (or the context to end). Requests are serialized on writeMu;
// responses may interleave freely — correlation is by id (B13).
func (c *Client) roundTrip(ctx context.Context, id int64, method string, params json.RawMessage) (*JSONRPCMessage, error) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return nil, fmt.Errorf("client closed")
	}

	ch := make(chan *JSONRPCMessage, 1)
	c.pending.Store(id, ch)
	defer c.pending.Delete(id)

	c.writeMu.Lock()
	err := c.writer.WriteMessage(JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  method,
		Params:  params,
	})
	c.writeMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("send %s: %w", method, err)
	}

	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		c.pending.Delete(id)
		return nil, ctx.Err()
	}
}

// notify implements rpcTransport for stdio.
func (c *Client) notify(_ context.Context, method string, params json.RawMessage) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.writer.WriteMessage(JSONRPCMessage{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	})
}

// SetStderr sets the writer for MCP server stderr output.
// Set to os.Stderr for debugging, or leave unset to discard.
func (c *Client) SetStderr(w io.Writer) {
	c.stderr = w
}

// newWriter returns the writer for the configured framing mode.
// The MCP specification (2025-06-18) mandates newline-delimited JSON on
// stdio. Some non-spec servers (older @modelcontextprotocol/* releases)
// require Content-Length LSP framing instead. When framing is unset we
// default to newline, the spec-compliant mode.
func newWriter(w io.Writer, framing string) messageWriter {
	if framing == "framed" {
		return NewFramedWriter(w)
	}
	return NewNewlineWriter(w)
}

// newReader returns a reader that auto-detects framing on the first message.
// Spec-compliant servers send "{json}\n" (newline mode). LSP-style servers
// send "Content-Length: N\r\n\r\n{json}" (framed mode). We peek the first
// non-whitespace byte: '{' means newline, anything else means framed.
func newReader(r io.Reader, framing string) messageReader {
	if framing == "newline" {
		return NewNewlineReader(r)
	}
	if framing == "framed" {
		return NewFramedReader(r)
	}
	return &autoDetectReader{reader: bufio.NewReader(r), framed: nil}
}

// autoDetectReader peeks the first non-whitespace byte of the first message
// to decide between Content-Length and newline framing, then delegates to
// the appropriate reader for all subsequent messages.
type autoDetectReader struct {
	reader *bufio.Reader
	framed messageReader
}

func (a *autoDetectReader) ReadMessage() (JSONRPCMessage, error) {
	if a.framed != nil {
		return a.framed.ReadMessage()
	}
	// Consume leading whitespace until the first meaningful byte — the
	// previous 16-byte peek returned EOF on streams that began with
	// more whitespace than the peek window (review B13).
	for {
		b, err := a.reader.ReadByte()
		if err != nil {
			return JSONRPCMessage{}, err
		}
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		case '{':
			if err := a.reader.UnreadByte(); err != nil {
				return JSONRPCMessage{}, err
			}
			a.framed = NewNewlineReader(a.reader)
		default:
			if err := a.reader.UnreadByte(); err != nil {
				return JSONRPCMessage{}, err
			}
			a.framed = NewFramedReader(a.reader)
		}
		return a.framed.ReadMessage()
	}
}

// Initialize performs the MCP initialize handshake and fetches tools.
func (c *Client) Initialize(ctx context.Context) error {
	if _, err := c.core.initialize(ctx); err != nil {
		return err
	}
	if err := c.refetchTools(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	c.initialized = true
	c.mu.Unlock()
	return nil
}

// refetchTools lists tools through the shared core and caches them.
func (c *Client) refetchTools(ctx context.Context) error {
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

// CallTool invokes a tool on the MCP server.
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	return c.core.callTool(ctx, name, args)
}

// Tools returns the tools discovered from this server.
func (c *Client) Tools() []ServerTool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tools
}

// Info returns server status details. Connected reflects the completed
// handshake, not merely a spawned process (review B13).
func (c *Client) Info() ServerInfo {
	c.mu.Lock()
	initialized, closed := c.initialized, c.closed
	toolCount := len(c.tools)
	c.mu.Unlock()

	info := ServerInfo{
		Name:      c.name,
		Transport: "stdio",
		Command:   c.manifest.Command + " " + joinArgs(c.manifest.Args),
		ToolCount: toolCount,
	}
	if initialized && !closed && c.cmd != nil && c.cmd.Process != nil {
		info.Connected = true
	}
	return info
}

func joinArgs(args []string) string {
	s := ""
	for i, a := range args {
		if i > 0 {
			s += " "
		}
		if strings.Contains(a, " ") {
			s += "\"" + a + "\""
		} else {
			s += a
		}
	}
	return s
}

// Close shuts down the MCP server process.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	c.failAllPending(fmt.Errorf("client closed"))
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_ = c.cmd.Wait()
	}
	return nil
}
