// Package tools implements the built-in tools yaah exposes to the
// model: read and bash. Each tool implements the Tool interface so the
// agent loop can dispatch uniformly whether a tool is built-in or comes
// from an MCP server. Additional tools (memory_search, memory_add,
// todowrite) are registered at runtime from the memory and todo packages.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Tool is the interface that all tools (built-in and MCP) must satisfy.
type Tool interface {
	// Name returns the tool name as it appears in the function call.
	Name() string

	// Description returns a short description of what the tool does.
	Description() string

	// Schema returns the JSON Schema for the tool's parameters.
	Schema() json.RawMessage

	// Execute runs the tool with the given JSON-encoded arguments.
	// The context is used for cancellation and timeouts.
	// Returns the result as a string (or an error if something went wrong).
	Execute(ctx context.Context, args string) (string, error)
}

// --- ReadTool ---

// ReadTool reads a file and returns its contents. Offsets and limits are
// applied to the split lines (zero-based for line-local offset, limit caps
// the returned line count).
type ReadTool struct{}

func (t *ReadTool) Name() string { return "read" }
func (t *ReadTool) Description() string {
	return "Reads a file from the local filesystem with optional offset and limit."
}

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

func (t *ReadTool) Execute(ctx context.Context, args string) (string, error) {
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

// dangerousCommands is a coarse deny-list of obviously destructive shell
// patterns. It is NOT a security boundary: model-generated shell can trivially
// evade a substring deny-list (whitespace, shell variables, indirection,
// decoders). Real protection comes from the approval gate (config.Approval);
// this list only catches the most blatant mistakes.
var dangerousCommands = []string{
	"rm -rf /", "rm -rf ~", "rm -rf .",
	"shutdown", "reboot", "halt",
	"mkfs", "mkswap",
	"dd if=", ":(){ :|:& };:",
	"chmod 777 /", "chown -R",
	"remove-item -recurse -force c:\\",
	"format-volume", "stop-computer", "restart-computer",
	"clear-disk", "initialize-disk",
}

// isDangerous reports whether cmd matches a known destructive pattern. It is a
// best-effort guard only (see dangerousCommands).
func isDangerous(cmd string) bool {
	lower := strings.ToLower(cmd)
	for _, dangerous := range dangerousCommands {
		if strings.Contains(lower, dangerous) {
			return true
		}
	}
	return false
}

// bashDefaultTimeout is the default deadline for a single bash command.
const bashDefaultTimeout = 30 * time.Second

// bashMaxOutput caps the bytes returned from a bash command to avoid exhausting
// memory on runaway output.
const bashMaxOutput = 1 << 20 // 1 MiB

func (t *BashTool) Name() string        { return "bash" }
func (t *BashTool) Description() string { return "Executes a shell command and returns its output." }

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

func (t *BashTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("bash: invalid arguments: %w", err)
	}
	if params.Command == "" {
		return "", fmt.Errorf("bash: command is required")
	}

	// Best-effort dangerous-command guard (NOT a security boundary).
	if isDangerous(params.Command) {
		return "", fmt.Errorf("bash: command matches a dangerous pattern; refused (enable approval gating for real protection)")
	}

	timeout := bashDefaultTimeout
	if params.Timeout > 0 {
		timeout = time.Duration(params.Timeout) * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", params.Command)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("bash: timed out after %s", timeout)
	}
	output = truncateOutput(output)
	if err != nil {
		return "", fmt.Errorf("bash: %w\n%s", err, string(output))
	}
	return string(output), nil
}

// truncateOutput caps a command's output to bashMaxOutput with a truncation marker.
func truncateOutput(b []byte) []byte {
	if len(b) <= bashMaxOutput {
		return b
	}
	return append(b[:bashMaxOutput], []byte("\n...[output truncated]...")...)
}

// --- PowerShellTool ---

// PowerShellTool runs a PowerShell command and returns its stdout.
// It tries pwsh (PowerShell 7+, cross-platform) first, then falls back
// to powershell (Windows PowerShell 5.1).
type PowerShellTool struct{}

func (t *PowerShellTool) Name() string { return "powershell" }
func (t *PowerShellTool) Description() string {
	return "Executes a PowerShell command and returns its output."
}

func (t *PowerShellTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"command": {"type": "string", "description": "The PowerShell command to execute"},
			"timeout": {"type": "integer", "description": "Timeout in seconds (default 30)"}
		},
		"required": ["command"]
	}`)
}

// psExecutable returns the best available PowerShell executable.
func psExecutable() string {
	if _, err := exec.LookPath("pwsh"); err == nil {
		return "pwsh"
	}
	return "powershell"
}

func (t *PowerShellTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("powershell: invalid arguments: %w", err)
	}
	if params.Command == "" {
		return "", fmt.Errorf("powershell: command is required")
	}

	if isDangerous(params.Command) {
		return "", fmt.Errorf("powershell: command matches a dangerous pattern; refused")
	}

	timeout := bashDefaultTimeout
	if params.Timeout > 0 {
		timeout = time.Duration(params.Timeout) * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	exe := psExecutable()
	cmd := exec.CommandContext(ctx, exe, "-NoProfile", "-NonInteractive", "-Command", params.Command)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("powershell: timed out after %s", timeout)
	}
	output = truncateOutput(output)
	if err != nil {
		return "", fmt.Errorf("powershell: %w\n%s", err, string(output))
	}
	return string(output), nil
}

// --- WriteTool ---

// WriteTool writes content to a file, overwriting if it exists.
type WriteTool struct{}

func (t *WriteTool) Name() string        { return "write" }
func (t *WriteTool) Description() string { return "Writes content to a file on the local filesystem." }

func (t *WriteTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"content": {"type": "string", "description": "The content to write to the file"},
			"filePath": {"type": "string", "description": "The absolute path to the file to write"}
		},
		"required": ["content", "filePath"]
	}`)
}

func (t *WriteTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Content  string `json:"content"`
		FilePath string `json:"filePath"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("write: invalid arguments: %w", err)
	}
	if params.FilePath == "" {
		return "", fmt.Errorf("write: filePath is required")
	}

	if err := os.WriteFile(params.FilePath, []byte(params.Content), 0o644); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}
	return fmt.Sprintf("Wrote %d bytes to %s", len(params.Content), params.FilePath), nil
}

// --- EditTool ---

// EditTool performs exact string replacements in a file.
type EditTool struct{}

func (t *EditTool) Name() string { return "edit" }
func (t *EditTool) Description() string {
	return "Performs exact string replacements in an existing file."
}

func (t *EditTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"filePath": {"type": "string", "description": "The absolute path to the file to edit"},
			"oldString": {"type": "string", "description": "The text to replace"},
			"newString": {"type": "string", "description": "The text to replace with (must differ from oldString)"},
			"replaceAll": {"type": "boolean", "description": "Replace all occurrences (default false)"}
		},
		"required": ["filePath", "oldString", "newString"]
	}`)
}

func (t *EditTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		FilePath   string `json:"filePath"`
		OldString  string `json:"oldString"`
		NewString  string `json:"newString"`
		ReplaceAll bool   `json:"replaceAll"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("edit: invalid arguments: %w", err)
	}
	if params.FilePath == "" {
		return "", fmt.Errorf("edit: filePath is required")
	}
	if params.OldString == params.NewString {
		return "", fmt.Errorf("edit: oldString and newString must differ")
	}

	data, err := os.ReadFile(params.FilePath)
	if err != nil {
		return "", fmt.Errorf("edit: %w", err)
	}

	content := string(data)
	count := strings.Count(content, params.OldString)
	if count == 0 {
		return "", fmt.Errorf("edit: oldString not found in %s", params.FilePath)
	}

	if !params.ReplaceAll {
		if count > 1 {
			return "", fmt.Errorf("edit: found %d matches for oldString in %s; use replaceAll or provide more context", count, params.FilePath)
		}
		content = strings.Replace(content, params.OldString, params.NewString, 1)
	} else {
		content = strings.ReplaceAll(content, params.OldString, params.NewString)
	}

	if err := os.WriteFile(params.FilePath, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("edit: %w", err)
	}

	replaced := count
	if !params.ReplaceAll {
		replaced = 1
	}
	return fmt.Sprintf("Replaced %d occurrence(s) in %s", replaced, params.FilePath), nil
}

// --- DeleteTool ---

// DeleteTool removes a file from the local filesystem.
type DeleteTool struct{}

func (t *DeleteTool) Name() string        { return "delete" }
func (t *DeleteTool) Description() string { return "Deletes a file from the local filesystem." }

func (t *DeleteTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"filePath": {"type": "string", "description": "The absolute path of the file to delete"}
		},
		"required": ["filePath"]
	}`)
}

func (t *DeleteTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		FilePath string `json:"filePath"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("delete: invalid arguments: %w", err)
	}
	if params.FilePath == "" {
		return "", fmt.Errorf("delete: filePath is required")
	}

	if err := os.Remove(params.FilePath); err != nil {
		return "", fmt.Errorf("delete: %w", err)
	}
	return fmt.Sprintf("Deleted %s", params.FilePath), nil
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
	r.Register(&PowerShellTool{})
	r.Register(&WriteTool{})
	r.Register(&EditTool{})
	r.Register(&DeleteTool{})
	r.Register(&GrepTool{})
	r.Register(&GlobTool{})
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
func (r *Registry) Execute(ctx context.Context, name, args string) (string, error) {
	t := r.Get(name)
	if t == nil {
		return "", fmt.Errorf("unknown tool: %s", name)
	}
	return t.Execute(ctx, args)
}

// List returns all registered tool names.
func (r *Registry) List() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	return names
}
