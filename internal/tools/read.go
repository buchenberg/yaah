package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/prompts"
)

// ReadTool reads a file and returns its contents. Offsets and limits are
// applied to the split lines (zero-based for line-local offset, limit caps
// the returned line count).
type ReadTool struct {
	PV *PathValidator
	WS Workspace
}

var (
	_ PathValidatorSetter = (*ReadTool)(nil)
	_ WorkspaceSetter     = (*ReadTool)(nil)
)

func (t *ReadTool) SetPathValidator(pv *PathValidator) { t.PV = pv }
func (t *ReadTool) SetWorkspace(ws Workspace)          { t.WS = ws }

func (t *ReadTool) Name() string        { return "read" }
func (t *ReadTool) Description() string { return prompts.ToolDescription("read") }

func (t *ReadTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"filePath": {"type": "string", "description": "The absolute path to the file to read"},
			"offset": {"type": "integer", "description": "Line number to start from (1-based)"},
			"limit": {"type": "integer", "description": "Maximum number of lines to return"}
		},
		"required": ["filePath"]
	}`)
}

func (t *ReadTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		FilePath string `json:"filePath"`
		Offset   int    `json:"offset"`
		Limit    int    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("read: invalid arguments: %w", err)
	}
	if params.FilePath == "" {
		return "", fmt.Errorf("read: filePath is required")
	}
	ws := workspaceOf(t.WS, t.PV)
	resolved, err := ws.ResolvePath(params.FilePath)
	if err != nil {
		return "", err
	}
	params.FilePath = resolved

	info, err := ws.Stat(ctx, params.FilePath)
	if err != nil {
		return "", fmt.Errorf("read: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("read: %s is a directory, use the ls tool to list its contents", params.FilePath)
	}

	data, err := ws.ReadFile(ctx, params.FilePath)
	if err != nil {
		return "", fmt.Errorf("read: %w", err)
	}

	if params.Offset > 0 || params.Limit > 0 {
		lines := strings.Split(string(data), "\n")
		// Split produces a phantom empty line for a trailing newline; drop it
		// so line counts and the "is there more?" decision match the file.
		if n := len(lines); n > 0 && lines[n-1] == "" {
			lines = lines[:n-1]
		}
		if params.Offset < 1 {
			params.Offset = 1
		}
		start := params.Offset - 1
		if start >= len(lines) {
			if len(lines) == 0 && params.Offset == 1 {
				return "", nil
			}
			return "", fmt.Errorf("read: offset %d is past end of file (%d lines)", params.Offset, len(lines))
		}

		end := len(lines)
		if params.Limit > 0 && start+params.Limit < end {
			end = start + params.Limit
		}
		result := strings.Join(lines[start:end], "\n")
		if end < len(lines) {
			result += truncationMarker
		}
		return result, nil
	}

	return string(data), nil
}
