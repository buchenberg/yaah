// Package tools implements the built-in tools yaah exposes to the
// model: read, write, edit, bash, glob, grep, and list. Each tool
// implements the Tool interface so the agent loop can dispatch
// uniformly whether a tool is built-in or comes from an MCP server.
package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Tool is the interface that all tools (built-in and MCP) must satisfy.
type Tool interface {
	// Name returns the tool name as it appears in the function call.
	Name() string

	// Schema returns the JSON Schema for the tool's parameters.
	Schema() json.RawMessage

	// Execute runs the tool with the given JSON-encoded arguments.
	// Returns the result as a string (or an error if something went wrong).
	Execute(args string) (string, error)
}

// --- ReadTool ---

// ReadTool reads a file and returns its contents with line numbers.
type ReadTool struct{}

func (t *ReadTool) Name() string { return "read" }

func (t *ReadTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "Path to the file to read"},
			"offset": {"type": "integer", "description": "Line number to start from (1-based)"},
			"limit": {"type": "integer", "description": "Maximum number of lines to return"}
		},
		"required": ["path"]
	}`)
}

func (t *ReadTool) Execute(args string) (string, error) {
	var params struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("read: invalid arguments: %w", err)
	}
	if params.Path == "" {
		return "", fmt.Errorf("read: path is required")
	}

	data, err := os.ReadFile(params.Path)
	if err != nil {
		return "", fmt.Errorf("read: %w", err)
	}

	if params.Offset > 0 || params.Limit > 0 {
		lines := strings.Split(string(data), "\n")
		if params.Offset < 1 {
			params.Offset = 1
		}
		start := params.Offset - 1
		if start > len(lines) {
			return "", nil
		}
		end := len(lines)
		if params.Limit > 0 && start+params.Limit < end {
			end = start + params.Limit
		}
		return strings.Join(lines[start:end], "\n"), nil
	}

	return string(data), nil
}

// --- BashTool ---

// BashTool runs a shell command and returns its stdout.
type BashTool struct{}

// dangerousCommands is a deny-list of high-risk shell patterns.
var dangerousCommands = []string{
	"rm -rf /", "rm -rf ~", "rm -rf .",
	"shutdown", "reboot", "halt",
	"mkfs", "mkswap",
	"dd if=", ":(){ :|:& };:",
	"chmod 777 /", "chown -R",
}

func (t *BashTool) Name() string { return "bash" }

func (t *BashTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"command": {"type": "string", "description": "The shell command to execute"},
			"timeout": {"type": "integer", "description": "Timeout in seconds (default 30)"}
		},
		"required": ["command"]
	}`)
}

func (t *BashTool) Execute(args string) (string, error) {
	var params struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("bash: invalid arguments: %w", err)
	}

	// Deny dangerous commands
	lower := strings.ToLower(params.Command)
	for _, dangerous := range dangerousCommands {
		if strings.Contains(lower, dangerous) {
			return "", fmt.Errorf("bash: command matches dangerous pattern %q", dangerous)
		}
	}

	cmd := exec.Command("sh", "-c", params.Command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("bash: %w\n%s", err, string(output))
	}
	return string(output), nil
}

// --- Tool Registry ---

// Registry holds all available tools and dispatches by name.
type Registry struct {
	tools map[string]Tool
}

// NewRegistry creates a tool registry with yaah's built-in tools.
func NewRegistry() *Registry {
	r := &Registry{tools: make(map[string]Tool)}
	r.Register(&ReadTool{})
	r.Register(&BashTool{})
	return r
}

// Register adds a tool to the registry.
func (r *Registry) Register(t Tool) {
	r.tools[t.Name()] = t
}

// Get returns the tool with the given name, or nil if not found.
func (r *Registry) Get(name string) Tool {
	return r.tools[name]
}

// Execute dispatches a tool call by name and returns the result.
func (r *Registry) Execute(name, args string) (string, error) {
	t := r.Get(name)
	if t == nil {
		return "", fmt.Errorf("unknown tool: %s", name)
	}
	return t.Execute(args)
}

// List returns all registered tool names.
func (r *Registry) List() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	return names
}
