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

const toolResultMaxLen = 8192

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

// editEntry is a single edit operation within a multi-edit call.
type editEntry struct {
	OldString string `json:"oldString"`
	NewString string `json:"newString"`
}

// EditTool performs exact string replacements in a file, with fuzzy fallback
// when exact match fails. Supports multi-edit via an edits[] array.
type EditTool struct{}

func (t *EditTool) Name() string { return "edit" }
func (t *EditTool) Description() string {
	return "Performs exact string replacements in an existing file with fuzzy matching fallback."
}

func (t *EditTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"filePath": {"type": "string", "description": "The absolute path to the file to edit"},
			"oldString": {"type": "string", "description": "The text to replace"},
			"newString": {"type": "string", "description": "The text to replace with (must differ from oldString)"},
			"replaceAll": {"type": "boolean", "description": "Replace all occurrences (default false)"},
			"edits": {"type": "array", "description": "Array of {oldString, newString} for batch edits", "items": {"type": "object", "properties": {"oldString": {"type": "string"}, "newString": {"type": "string"}}, "required": ["oldString", "newString"]}}
		},
		"required": ["filePath"]
	}`)
}

func (t *EditTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		FilePath   string      `json:"filePath"`
		OldString  string      `json:"oldString"`
		NewString  string      `json:"newString"`
		ReplaceAll bool        `json:"replaceAll"`
		Edits      []editEntry `json:"edits"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("edit: invalid arguments: %w", err)
	}
	if params.FilePath == "" {
		return "", fmt.Errorf("edit: filePath is required")
	}

	if len(params.Edits) > 0 {
		return t.executeMultiEdit(params.FilePath, params.Edits)
	}

	if params.OldString == "" {
		return "", fmt.Errorf("edit: oldString or edits[] is required")
	}
	if params.OldString == params.NewString {
		return "", fmt.Errorf("edit: oldString and newString must differ")
	}

	return t.executeSingleEdit(params.FilePath, params.OldString, params.NewString, params.ReplaceAll)
}

func (t *EditTool) executeSingleEdit(filePath, oldStr, newStr string, replaceAll bool) (string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("edit: %w", err)
	}

	content := string(data)
	origLines := countLines(content)
	count := strings.Count(content, oldStr)

	matched := oldStr
	if count == 0 {
		matched, count = tryFuzzyMatch(content, oldStr)
		if count == 0 {
			return "", fmt.Errorf("edit: oldString not found in %s", filePath)
		}
	}

	if !replaceAll {
		if count > 1 {
			return "", fmt.Errorf("edit: found %d matches for oldString in %s; use replaceAll or provide more context", count, filePath)
		}
		content = strings.Replace(content, matched, newStr, 1)
	} else {
		content = strings.ReplaceAll(content, matched, newStr)
	}

	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("edit: %w", err)
	}

	replaced := count
	if !replaceAll {
		replaced = 1
	}
	newLines := countLines(content)
	return formatEditResult(filePath, replaced, origLines, newLines), nil
}

func (t *EditTool) executeMultiEdit(filePath string, edits []editEntry) (string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("edit: %w", err)
	}

	content := string(data)
	origLines := countLines(content)
	totalReplaced := 0
	var failures []string

	for i, e := range edits {
		if e.OldString == e.NewString {
			failures = append(failures, fmt.Sprintf("edit #%d: oldString and newString must differ", i))
			continue
		}

		matched := e.OldString
		count := strings.Count(content, matched)
		if count == 0 {
			matched, count = tryFuzzyMatch(content, e.OldString)
		}
		if count == 0 {
			failures = append(failures, fmt.Sprintf("edit #%d: oldString not found", i))
			continue
		}
		if count > 1 {
			failures = append(failures, fmt.Sprintf("edit #%d: found %d matches — use more context for disambiguation", i, count))
			continue
		}

		content = strings.Replace(content, matched, e.NewString, 1)
		totalReplaced++
	}

	if totalReplaced == 0 && len(failures) > 0 {
		return "", fmt.Errorf("edit: all %d edits failed:\n%s", len(edits), strings.Join(failures, "\n"))
	}

	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("edit: %w", err)
	}

	newLines := countLines(content)
	msg := formatEditResult(filePath, totalReplaced, origLines, newLines)
	if len(failures) > 0 {
		msg += fmt.Sprintf("\n%d edit(s) failed:\n%s", len(failures), strings.Join(failures, "\n"))
	}
	return msg, nil
}

// tryFuzzyMatch attempts progressively looser matching strategies when exact
// match fails. Returns the matched string and count.
func tryFuzzyMatch(content, oldStr string) (string, int) {
	strategies := []struct {
		name string
		fn   func(string) string
	}{
		{"trailing-ws-stripped", func(s string) string {
			lines := strings.Split(s, "\n")
			for i, l := range lines {
				lines[i] = strings.TrimRight(l, " \t")
			}
			return strings.Join(lines, "\n")
		}},
		{"smart-quote-normalized", func(s string) string {
			r := strings.NewReplacer(
				"\u201c", `"`, "\u201d", `"`,
				"\u2018", "'", "\u2019", "'",
				"\u00ab", `"`, "\u00bb", `"`,
				"\u201e", `"`, "\u201a", "'",
				"\u2039", "'", "\u203a", "'",
			)
			return r.Replace(s)
		}},
		{"dash-normalized", func(s string) string {
			r := strings.NewReplacer(
				"\u2013", "-", "\u2014", "--",
				"\u2015", "--", "\u2212", "-",
				"\u2010", "-", "\u2011", "-",
				"\u2012", "-",
			)
			return r.Replace(s)
		}},
		{"whitespace-collapsed", func(s string) string {
			lines := strings.Split(s, "\n")
			for i, l := range lines {
				// Collapse multiple spaces/tabs into single space
				var collapsed strings.Builder
				inSpace := false
				for _, r := range l {
					if r == ' ' || r == '\t' {
						if !inSpace {
							collapsed.WriteByte(' ')
							inSpace = true
						}
					} else {
						collapsed.WriteRune(r)
						inSpace = false
					}
				}
				lines[i] = strings.TrimSpace(collapsed.String())
			}
			return strings.Join(lines, "\n")
		}},
	}

	for _, st := range strategies {
		normalizedContent := st.fn(content)
		normalizedOld := st.fn(oldStr)
		pos := strings.Index(normalizedContent, normalizedOld)
		if pos < 0 {
			continue
		}
		count := strings.Count(normalizedContent, normalizedOld)
		if count > 1 {
			return oldStr, count
		}

		start := contentBytePos(content, st.fn, pos)
		end := start
		for end <= len(content) {
			if st.fn(content[start:end]) == normalizedOld {
				return content[start:end], 1
			}
			end++
			if end-start > len(oldStr)*3 {
				break
			}
		}
	}
	return oldStr, 0
}

func contentBytePos(content string, fn func(string) string, normPos int) int {
	pos := 0
	for i := range len(content) {
		if len(fn(content[:i])) >= normPos {
			return pos
		}
		pos = i
	}
	return len(content)
}

// countLines returns the number of lines in a string.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// formatEditResult formats the result of an edit operation with diff info.
func formatEditResult(filePath string, replaced int, origLines, newLines int) string {
	if origLines != newLines {
		delta := newLines - origLines
		verb := "added"
		if delta < 0 {
			verb = "removed"
			delta = -delta
		}
		return fmt.Sprintf("Replaced %d occurrence(s) in %s (%d → %d lines, %s %d)",
			replaced, filePath, origLines, newLines, verb, delta)
	}
	return fmt.Sprintf("Replaced %d occurrence(s) in %s (%d lines)",
		replaced, filePath, origLines)
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
	r.Register(&LsTool{})
	r.Register(&QuestionTool{})
	r.Register(&WebFetchTool{})
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
