package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// MCPTool wraps an MCP server tool as a yaah Tool.
type MCPTool struct {
	tool   ServerTool
	client MCPClient
}

// MCPClient is the interface for calling tools on an MCP server.
type MCPClient interface {
	CallTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error)
	Close() error
}

// NewMCPTool creates a tool wrapper for an MCP server tool.
func NewMCPTool(tool ServerTool, client MCPClient) *MCPTool {
	return &MCPTool{tool: tool, client: client}
}

// Name returns the tool name (prefixed with server name for uniqueness).
func (t *MCPTool) Name() string {
	if t.tool.ServerName != "" {
		return t.tool.ServerName + "_" + t.tool.Name
	}
	return t.tool.Name
}

// Schema returns the tool's input schema.
func (t *MCPTool) Schema() json.RawMessage {
	if len(t.tool.InputSchema) > 0 {
		return t.tool.InputSchema
	}
	return json.RawMessage(`{"type":"object","properties":{}}`)
}

// Execute calls the tool on the MCP server.
func (t *MCPTool) Execute(ctx context.Context, args string) (string, error) {
	result, err := t.client.CallTool(ctx, t.tool.Name, json.RawMessage(args))
	if err != nil {
		return "", fmt.Errorf("mcp tool %s: %w", t.tool.Name, err)
	}

	// Try to extract text content from the result
	var parsed struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(result, &parsed); err == nil && len(parsed.Content) > 0 {
		var text string
		for _, c := range parsed.Content {
			if c.Type == "text" {
				text += c.Text
			}
		}
		if text != "" {
			return text, nil
		}
	}

	// Fallback: return raw JSON
	return string(result), nil
}

// StartMCPClients discovers MCP manifests and starts clients.
// Returns a list of started clients and their tools.
func StartMCPClients(ctx context.Context, dirs []string) ([]MCPClient, []*MCPTool, error) {
	return StartMCPClientsWithStderr(ctx, dirs, os.Stderr)
}

// StartMCPClientsWithStderr is like StartMCPClients but redirects MCP server
// stderr to the given writer. Pass io.Discard to suppress server log output
// (e.g. when running inside a TUI that owns the terminal).
func StartMCPClientsWithStderr(ctx context.Context, dirs []string, stderr io.Writer) ([]MCPClient, []*MCPTool, error) {
	manifests := DiscoverManifests(dirs)
	var clients []MCPClient
	var tools []*MCPTool

	for name, manifest := range manifests {
		var client MCPClient
		var err error

		switch manifest.Transport {
		case "http":
			httpClient := NewHTTPClient(name, manifest.URL)
			err = httpClient.Initialize(ctx)
			client = httpClient
		case "stdio":
			stdioClient := NewClient(name, *manifest)
			stdioClient.SetStderr(stderr)
			err = stdioClient.Start(ctx)
			if err == nil {
				err = stdioClient.Initialize(ctx)
			}
			client = stdioClient
		default:
			continue
		}

		if err != nil {
			// Log but don't fail — MCP servers are optional
			fmt.Fprintf(os.Stderr, "  warning: MCP server %s: %v\n", name, err)
			continue
		}

		clients = append(clients, client)

		// Wrap each tool
		if httpC, ok := client.(*HTTPClient); ok {
			for _, tool := range httpC.Tools() {
				tools = append(tools, NewMCPTool(tool, client))
			}
		} else if stdioC, ok := client.(*Client); ok {
			for _, tool := range stdioC.Tools() {
				tools = append(tools, NewMCPTool(tool, client))
			}
		}
	}

	return clients, tools, nil
}
