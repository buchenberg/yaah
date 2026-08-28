package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// MCPTool wraps an MCP server tool as a yaah Tool.
type MCPTool struct {
	tool   ServerTool
	client MCPClient
}

// ServerInfo holds status details about a discovered MCP server.
type ServerInfo struct {
	Name      string // server name (from manifest filename)
	Transport string // "stdio" or "http"
	Command   string // for stdio servers
	URL       string // for http servers
	Connected bool   // initialization succeeded
	ToolCount int    // number of tools exposed
	Error     string // non-empty if connection/init failed
}

// MCPClient is the interface for calling tools on an MCP server.
type MCPClient interface {
	CallTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error)
	Tools() []ServerTool
	Close() error
	Info() ServerInfo
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

// Description returns the tool description from the MCP manifest.
func (t *MCPTool) Description() string {
	if t.tool.Description != "" {
		return t.tool.Description
	}
	return "Remote tool via MCP server."
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

// StartMCPClientsFromConfig starts MCP clients from a map of named Manifests
// (e.g. from config.yaml's mcp_servers section). The stderr parameter controls
// where stdio MCP server stderr is written; pass io.Discard for TUIs.
func StartMCPClientsFromConfig(ctx context.Context, manifests map[string]*Manifest, stderr io.Writer) ([]MCPClient, []*MCPTool, []ServerInfo, error) {
	var clients []MCPClient
	var tools []*MCPTool
	var infos []ServerInfo

	for name, manifest := range manifests {
		var client MCPClient
		var err error

		switch manifest.Transport {
		case "http":
			httpClient := NewHTTPClient(name, manifest.URL, manifest.Headers)
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

		info := client.Info()
		if err != nil {
			info.Connected = false
			info.Error = err.Error()
			infos = append(infos, info)
			continue
		}

		clients = append(clients, client)

		// Wrap each tool. Tools() is part of the MCPClient interface, so
		// no per-transport type assertion is needed here (review B13).
		for _, tool := range client.Tools() {
			tools = append(tools, NewMCPTool(tool, client))
		}
		infos = append(infos, info)
	}

	return clients, tools, infos, nil
}
